-- log_contract.lua
-- Fluent Bit Lua filter implementing Contract v3 validation, field extraction,
-- time selection, truncation, redaction, and routing.

REQUIRED = { "time", "level", "msg", "service.name", "service.version" }

RESERVED = {
    "observed_time",
    "log.iostream",
    "k8s.namespace.name",
    "k8s.pod.name",
    "k8s.pod.uid",
    "k8s.container.name",
    "k8s.node.name",
    "_time",
    "_msg",
    "_stream",
    "_stream_id",
    "_route"
}

PATTERNS = {
    ["bearer"] = "([Bb][Ee][Aa][Rr][Ee][Rr]%s+[^%s\"']+)",
    ["google-key"] = "(AIza[%w_-]+)",
    ["jwt"] = "eyJ[%w_-]+%.[%w_-]+%.[%w_-]+",
    ["secret-field"] = "sensitive-field",
    ["sk-key"] = "(sk%-[%w_-]+)"
}

local NODE_NAME = os.getenv("NODE_NAME") or "local-node"

local legacy_containers = nil

function load_legacy_containers()
    if legacy_containers ~= nil then
        return legacy_containers
    end
    legacy_containers = {}
    local paths = {}
    local env_path = os.getenv("LEGACY_CONTAINERS_FILE")
    if env_path and env_path ~= "" then
        table.insert(paths, env_path)
    end
    table.insert(paths, "/fluent-bit/etc/legacy-containers.txt")
    table.insert(paths, "deploy/platform/observability/fluent-bit/legacy-containers.txt")

    for _, p in ipairs(paths) do
        local f = io.open(p, "r")
        if f then
            for line in f:lines() do
                line = line:match("^%s*(.-)%s*$")
                if line ~= "" and not line:match("^#") then
                    legacy_containers[line] = true
                end
            end
            f:close()
            return legacy_containers
        end
    end
    return legacy_containers
end

local function is_sensitive(key)
    if type(key) ~= "string" then return false end
    local k = key:lower()
    return k:find("pass") ~= nil or
           k:find("secret") ~= nil or
           k:find("token") ~= nil or
           k:find("api[_-]?key") ~= nil or
           k:find("authorization") ~= nil or
           k:find("cookie") ~= nil or
           k:find("credential") ~= nil or
           k:find("private[_-]?key") ~= nil or
           k:find("access[_-]?key") ~= nil
end

local function has_redacted(s)
    if type(s) ~= "string" then return false end
    return s:find("[REDACTED", 1, true) ~= nil
end

local function redact(s)
    if type(s) ~= "string" then return s end

    -- 1. Bearer
    s = s:gsub("(%f[%a])" .. PATTERNS["bearer"], "%1[REDACTED:bearer]")
    s = s:gsub("^" .. PATTERNS["bearer"], "[REDACTED:bearer]")

    -- 2. Quoted KV (JSON style: "key": "val" or "key": 123)
    s = s:gsub('(%b"")(%s*[:=]%s*)(%b"")', function(k_quoted, sep, val_quoted)
        local k = k_quoted:sub(2, -2)
        if is_sensitive(k) then
            if has_redacted(val_quoted) then
                return k_quoted .. sep .. val_quoted
            else
                return k_quoted .. sep .. '"[REDACTED:secret-field]"'
            end
        end
        return k_quoted .. sep .. val_quoted
    end)

    -- Python dict style single quoted ('key': 'val')
    s = s:gsub("(%b'')(%s*[:=]%s*)(%b'')", function(k_quoted, sep, val_quoted)
        local k = k_quoted:sub(2, -2)
        if is_sensitive(k) then
            if has_redacted(val_quoted) then
                return k_quoted .. sep .. val_quoted
            else
                return k_quoted .. sep .. "'[REDACTED:secret-field]'"
            end
        end
        return k_quoted .. sep .. val_quoted
    end)

    -- Quoted KV bare scalar e.g. {"token_count":42}
    s = s:gsub('(%b"")(%s*[:=]%s*)([^%s,{}%[%]"\'\n\r]+)', function(k_quoted, sep, scalar)
        local k = k_quoted:sub(2, -2)
        if is_sensitive(k) then
            if has_redacted(scalar) then
                return k_quoted .. sep .. scalar
            else
                return k_quoted .. sep .. '"[REDACTED:secret-field]"'
            end
        end
        return k_quoted .. sep .. scalar
    end)

    -- 3. Header KV e.g. X-Api-Key: secret123 def
    s = s:gsub('([%w_.-]+)(%s*:%s*)([^\r\n]+)', function(k, sep, val)
        if is_sensitive(k) then
            local trimmed = val:match("^%s*(.-)%s*$")
            if has_redacted(trimmed) then
                return k .. sep .. val
            else
                return k .. sep .. "[REDACTED:secret-field]"
            end
        end
        return k .. sep .. val
    end)

    -- 4. Assign KV e.g. password=hunter2 or password="abc" or password="secret123",
    s = s:gsub('([%w_.-]+)(%s*=%s*)(%b"")', function(k, sep, val_quoted)
        if is_sensitive(k) then
            if has_redacted(val_quoted) then
                return k .. sep .. val_quoted
            else
                return k .. sep .. "[REDACTED:secret-field]"
            end
        end
        return k .. sep .. val_quoted
    end)

    -- Escaped quotes e.g. password=\"abc\" or password=\"hunter 2\"
    s = s:gsub('([%w_.-]+)(%s*=%s*)(\\"[^\r\n]-")', function(k, sep, val_quoted)
        if is_sensitive(k) then
            return k .. sep .. "[REDACTED:secret-field]"
        end
        return k .. sep .. val_quoted
    end)

    s = s:gsub('([%w_.-]+)(%s*=%s*)([^%s&,;)\r\n]+)', function(k, sep, val)
        if is_sensitive(k) then
            if has_redacted(val) then
                return k .. sep .. val
            else
                return k .. sep .. "[REDACTED:secret-field]"
            end
        end
        return k .. sep .. val
    end)

    -- 5. JWT
    s = s:gsub(PATTERNS["jwt"], "[REDACTED:jwt]")

    -- 6. Google key
    s = s:gsub(PATTERNS["google-key"], function(m)
        if #m >= 34 then return "[REDACTED:google-key]" end
        return m
    end)

    -- 7. sk- key
    s = s:gsub("(%f[%a])" .. PATTERNS["sk-key"], function(prefix, m)
        if #m >= 19 then return prefix .. "[REDACTED:sk-key]" end
        return prefix .. m
    end)
    s = s:gsub("^" .. PATTERNS["sk-key"], function(m)
        if #m >= 19 then return "[REDACTED:sk-key]" end
        return m
    end)

    return s
end

local function truncate_stacktrace(str)
    if type(str) ~= "string" or #str <= 8192 then
        return str
    end
    local original_len = #str
    local slice = str:sub(1, 8192)
    local last_nl = slice:match(".*()\n")
    local cut_pos = 8192
    if last_nl and last_nl > 1 then
        cut_pos = last_nl - 1
    end
    local truncated_bytes = original_len - cut_pos
    return str:sub(1, cut_pos) .. "\n…[truncated " .. tostring(truncated_bytes) .. " bytes]"
end

local function parse_rfc3339(ts_str)
    if type(ts_str) ~= "string" then return nil end
    local year, month, day, hour, min, sec, frac, tz = ts_str:match("^(%d%d%d%d)%-(%d%d)%-(%d%d)[Tt ](%d%d):(%d%d):(%d%d)(%.?%d*)(.*)$")
    if not year then return nil end
    if not tz or tz == "" then return nil end

    year, month, day = tonumber(year), tonumber(month), tonumber(day)
    hour, min, sec = tonumber(hour), tonumber(min), tonumber(sec)

    local nsec = 0
    if frac and #frac > 1 then
        local frac_str = frac:sub(2)
        if #frac_str > 9 then
            frac_str = frac_str:sub(1, 9)
        else
            frac_str = frac_str .. string.rep("0", 9 - #frac_str)
        end
        nsec = tonumber(frac_str) or 0
    end

    local epoch = os.time({
        year = year,
        month = month,
        day = day,
        hour = hour,
        min = min,
        sec = sec,
        isdst = false,
    })
    if not epoch then return nil end

    local utc_date = os.date("!*t", epoch)
    local local_date = os.date("*t", epoch)
    local tz_diff = os.difftime(os.time(local_date), os.time(utc_date))
    epoch = epoch + tz_diff

    if tz == "Z" or tz == "z" then
        -- UTC
    else
        local sign, tz_h, tz_m = tz:match("^([+-])(%d%d):(%d%d)$")
        if not sign then return nil end
        tz_h, tz_m = tonumber(tz_h), tonumber(tz_m)
        if tz_h > 23 or tz_m > 59 then return nil end
        local offset_sec = tz_h * 3600 + tz_m * 60
        if sign == "+" then
            epoch = epoch - offset_sec
        else
            epoch = epoch + offset_sec
        end
    end

    return epoch, nsec
end

function process(tag, timestamp, record)
    -- Stage 1: Strip reserved keys before any platform assignments (FR-011, FR-012)
    for _, key in ipairs(RESERVED) do
        record[key] = nil
    end
    record["k8s"] = nil
    for k, _ in pairs(record) do
        if k:sub(1, 4) == "k8s." or (k:sub(1, 1) == "_" and k ~= "_time" and k ~= "_msg" and k ~= "_route") then
            record[k] = nil
        end
    end

    -- Stage 2: Extract platform fields
    local ns = record["ns"] or record["namespace"] or record["project"] or ""
    local pod = record["pod"] or ""
    local uid = record["uid"] or record["pod_uid"] or ""
    local container = record["container"] or ""
    local iostream = record["stream"] or "stdout"

    -- Remove temporary extraction keys
    record["ns"] = nil
    record["namespace"] = nil
    record["project"] = nil
    record["pod"] = nil
    record["uid"] = nil
    record["pod_uid"] = nil
    record["container"] = nil
    record["filepath"] = nil
    record["stream"] = nil

    local cri_sec = 0
    local cri_nsec = 0
    if type(timestamp) == "table" then
        cri_sec = timestamp.sec or timestamp[1] or 0
        cri_nsec = timestamp.nsec or timestamp[2] or 0
    elseif type(timestamp) == "number" then
        cri_sec = math.floor(timestamp)
        cri_nsec = math.floor((timestamp - cri_sec) * 1e9)
    end
    local observed_time = os.date("!%Y-%m-%dT%H:%M:%S", cri_sec) .. string.format(".%09dZ", cri_nsec)
    local node_name = NODE_NAME

    record["k8s.namespace.name"] = ns
    record["k8s.pod.name"] = pod
    record["k8s.pod.uid"] = uid
    record["k8s.container.name"] = container
    record["k8s.node.name"] = node_name
    record["log.iostream"] = iostream
    record["observed_time"] = observed_time

    -- Stage 3: Routing decision (can it be refined?)
    local container_id = ns .. "/" .. container
    local legacy_set = load_legacy_containers()
    local is_legacy = legacy_set[container_id] == true

    local raw_log = record["log"] or record["msg"] or ""
    local is_crash = false
    if type(raw_log) == "string" then
        if raw_log:find("^panic:") or raw_log:find("^fatal error:") or
           raw_log:find("^Exception in thread ") or raw_log:find("^Traceback %(most recent call last%):") then
            is_crash = true
        end
    end

    local is_nginx_error = false
    if type(raw_log) == "string" then
        local ts_prefix, lvl = raw_log:match("^(%d%d%d%d/%d%d/%d%d %d%d:%d%d:%d%d) %[([%a]+)%]")
        if ts_prefix and (lvl == "error" or lvl == "crit" or lvl == "alert" or lvl == "emerg") then
            is_nginx_error = true
        end
    end

    local has_all_required = true
    for _, req_key in ipairs(REQUIRED) do
        if record[req_key] == nil or record[req_key] == "" then
            has_all_required = false
            break
        end
    end

    local is_refined = is_legacy or is_crash or is_nginx_error or has_all_required

    -- Quarantine path: unrefined records
    if not is_refined then
        local q_record = {
            ["msg"] = redact(raw_log),
            ["k8s.namespace.name"] = ns,
            ["k8s.pod.name"] = pod,
            ["k8s.pod.uid"] = uid,
            ["k8s.container.name"] = container,
            ["k8s.node.name"] = node_name,
            ["log.iostream"] = iostream,
            ["observed_time"] = observed_time,
            ["_route"] = "quarantine"
        }
        return 2, timestamp, q_record
    end

    -- Main path: refined records
    if record["msg"] == nil or record["msg"] == "" then
        record["msg"] = raw_log
    end

    -- Stacktrace truncation
    if type(record["exception.stacktrace"]) == "string" then
        record["exception.stacktrace"] = truncate_stacktrace(record["exception.stacktrace"])
    end
    if is_crash and type(record["msg"]) == "string" then
        record["msg"] = truncate_stacktrace(record["msg"])
    end

    -- Time selection (FR-015)
    local code = 2
    local out_ts = timestamp
    if record["time"] ~= nil then
        local proj_sec, proj_nsec = parse_rfc3339(record["time"])
        if proj_sec and math.abs(proj_sec - cri_sec) <= 300 then
            record["time"] = nil
            code = 1
            if type(timestamp) == "table" then
                out_ts = { sec = proj_sec, nsec = proj_nsec }
            else
                out_ts = proj_sec + (proj_nsec / 1e9)
            end
        else
            code = 2
        end
    end

    -- Redaction of string values
    for k, v in pairs(record) do
        if type(v) == "string" then
            local red = redact(v)
            if is_sensitive(k) and not has_redacted(red) then
                record[k] = "[REDACTED:secret-field]"
            else
                record[k] = red
            end
        elseif is_sensitive(k) then
            record[k] = "[REDACTED:secret-field]"
        end
    end

    record["log"] = nil
    record["_route"] = "main"

    return code, out_ts, record
end
