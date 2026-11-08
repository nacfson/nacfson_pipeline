-- Standalone Lua runner for log_contract.lua
-- Tests T017, T029, T049, T050

local script_path = "deploy/platform/observability/fluent-bit/log_contract.lua"

local f = io.open(script_path, "r")
if not f then
    print("[FAIL] " .. script_path .. " does not exist")
    os.exit(1)
end
f:close()

dofile(script_path)

-- Verify that the live legacy-containers.txt file parses as empty after the v3
-- cutover, then seed synthetic entries on the cached table for unit tests that
-- exercise the legacy routing branch.
local live_legacy = load_legacy_containers()
if next(live_legacy) ~= nil then
    error("expected live legacy-containers.txt to have no active entries after v3 cutover", 2)
end
live_legacy["proj-pn/backend"] = true
live_legacy["proj-blog/blog-web"] = true

local function assert_eq(actual, expected, msg)
    if actual ~= expected then
        error(string.format("%s: expected %q, got %q", msg or "assertion failed", tostring(expected), tostring(actual)), 2)
    end
end

local function assert_true(cond, msg)
    if not cond then
        error(msg or "assertion failed", 2)
    end
end

-- Helper to decode vectors.json
local function load_vectors()
    local path = "services/error-pipeline/internal/redact/testdata/vectors.json"
    local fh = assert(io.open(path, "r"), "cannot open " .. path)
    local vectors = {}
    local cur_input = nil

    local function unescape_json(str)
        str = str:gsub("\\\\", "\\\1")
        str = str:gsub('\\"', '"')
        str = str:gsub("\\n", "\n")
        str = str:gsub("\\r", "\r")
        str = str:gsub("\\t", "\t")
        str = str:gsub("\\\1", "\\")
        return str
    end

    for line in fh:lines() do
        local inp = line:match('^%s*"input"%s*:%s*"(.*)"%s*,?%s*$')
        if inp then
            cur_input = unescape_json(inp)
        end
        local out = line:match('^%s*"output"%s*:%s*"(.*)"%s*,?%s*$')
        if out and cur_input then
            local cur_output = unescape_json(out)
            table.insert(vectors, { input = cur_input, output = cur_output })
            cur_input = nil
        end
    end
    fh:close()
    return vectors
end

print("[INFO] Running Lua log_contract tests...")

-- Test 1: T017 Platform fields set from path keys and NODE_NAME
do
    local ts = { sec = 1793782810, nsec = 125481302 }
    local record = {
        ns = "proj-pn",
        pod = "backend-7d9f-abc",
        uid = "3f2c-aa",
        container = "backend",
        stream = "stderr",
        filepath = "/var/log/pods/proj-pn_backend-7d9f-abc_3f2c-aa/backend/0.log",
        time = "2026-11-04T09:00:10.125481302Z",
        level = "info",
        msg = "started",
        ["service.name"] = "pn-backend",
        ["service.version"] = "v1.0.0"
    }
    local code, out_ts, out_rec = process("proj.test", ts, record)
    assert_eq(out_rec["k8s.namespace.name"], "proj-pn", "namespace")
    assert_eq(out_rec["k8s.pod.name"], "backend-7d9f-abc", "pod")
    assert_eq(out_rec["k8s.pod.uid"], "3f2c-aa", "uid")
    assert_eq(out_rec["k8s.container.name"], "backend", "container")
    assert_true(out_rec["k8s.node.name"] ~= nil, "node name present")
    assert_eq(out_rec["log.iostream"], "stderr", "iostream")
    assert_eq(out_rec["observed_time"], "2026-11-04T09:00:10.125481302Z", "observed_time")
    assert_eq(out_rec["ns"], nil, "temporary ns deleted")
    assert_eq(out_rec["pod"], nil, "temporary pod deleted")
    assert_eq(out_rec["uid"], nil, "temporary uid deleted")
    assert_eq(out_rec["container"], nil, "temporary container deleted")
    assert_eq(out_rec["filepath"], nil, "temporary filepath deleted")
    assert_eq(out_rec["stream"], nil, "temporary stream deleted")
    print("  [PASS] Test 1: Platform fields extracted and temporary keys removed")
end

-- Test 2: T017 msg falls back to log for non-JSON lines (legacy container)
do
    local ts = { sec = 1793782810, nsec = 0 }
    local record = {
        ns = "proj-pn",
        pod = "backend-1",
        uid = "u-1",
        container = "backend", -- in legacy-containers.txt
        log = "plain log line message",
    }
    local code, out_ts, out_rec = process("proj.test", ts, record)
    assert_eq(out_rec["msg"], "plain log line message", "msg falls back to log")
    assert_eq(out_rec["log"], nil, "raw log deleted on main path")
    assert_eq(out_rec["_route"], "main", "legacy routes to main")
    print("  [PASS] Test 2: msg falls back to log on main path")
end

-- Test 3: T017 _time selection within +-5m vs skew > 5m
do
    local ts = { sec = 1793782810, nsec = 0 } -- CRI time
    -- Case A: valid time within 10 seconds of CRI
    local rec_ok = {
        ns = "proj-other",
        pod = "p1",
        uid = "u1",
        container = "c1",
        time = "2026-11-04T09:00:15Z",
        level = "info",
        msg = "ok",
        ["service.name"] = "test",
        ["service.version"] = "v1.0.0"
    }
    local code_ok, out_ts_ok, out_rec_ok = process("proj.test", ts, rec_ok)
    assert_eq(code_ok, 1, "return code 1 for new timestamp")
    assert_eq(out_rec_ok["time"], nil, "project time deleted when accepted as _time")

    -- Case B: skewed time (10 minutes away)
    local rec_skew = {
        ns = "proj-other",
        pod = "p1",
        uid = "u1",
        container = "c1",
        time = "2026-11-04T09:10:10Z",
        level = "info",
        msg = "skewed",
        ["service.name"] = "test",
        ["service.version"] = "v1.0.0"
    }
    local code_skew, out_ts_skew, out_rec_skew = process("proj.test", ts, rec_skew)
    assert_eq(code_skew, 2, "return code 2 keeps CRI timestamp")
    assert_eq(out_rec_skew["time"], "2026-11-04T09:10:10Z", "project time kept when rejected for _time")
    -- Case C: missing or invalid timezone
    local rec_bad_tz = {
        ns = "proj-other", pod = "p1", uid = "u1", container = "c1",
        time = "2026-11-04T09:00:15", -- missing timezone
        level = "info", msg = "bad tz",
        ["service.name"] = "test", ["service.version"] = "v1.0.0"
    }
    local code_bad_tz, _, out_rec_bad_tz = process("proj.test", ts, rec_bad_tz)
    assert_eq(code_bad_tz, 2, "return code 2 for missing timezone")
    assert_eq(out_rec_bad_tz["time"], "2026-11-04T09:00:15", "project time kept when missing timezone")
    print("  [PASS] Test 3: _time selection and time skew fallback")
end

-- Test 4: T017 FR-018c Truncation of stack traces and panics > 8 KiB
do
    local ts = { sec = 1793782810, nsec = 0 }
    -- Build 20 KiB trace
    local lines = { "goroutine 1 [running]:", "main.fail()" }
    while #table.concat(lines, "\n") < 20480 do
        table.insert(lines, "\t/app/main.go:42 +0x123")
    end
    local big_trace = table.concat(lines, "\n")
    local big_len = #big_trace

    local rec = {
        ns = "proj-test",
        pod = "p1",
        uid = "u1",
        container = "c1",
        time = "2026-11-04T09:00:10Z",
        level = "error",
        msg = "failed",
        ["service.name"] = "test",
        ["service.version"] = "v1.0.0",
        ["exception.stacktrace"] = big_trace
    }
    local _, _, out_rec = process("proj.test", ts, rec)
    local trunc = out_rec["exception.stacktrace"]
    assert_true(#trunc < big_len, "stacktrace was truncated")
    assert_true(trunc:find("^goroutine 1 %[running%]:"), "kept first line")
    local marker = trunc:match("…%[truncated (%d+) bytes%]")
    assert_true(marker ~= nil, "has truncated marker")
    local cut_bytes = tonumber(marker)
    -- Verify cut position + cut_bytes == original length
    local cut_slice = trunc:sub(1, trunc:find("\n…%[truncated") - 1)
    assert_true(#cut_slice <= 8192, "slice within 8192 bytes")
    assert_eq(#cut_slice + cut_bytes, big_len, "byte count calculation exact")

    -- Trace of exactly 8192 bytes unchanged
    local exact_trace = string.rep("x", 8191) .. "\n"
    assert_eq(#exact_trace, 8192)
    local rec_exact = {
        ns = "proj-test",
        pod = "p1",
        uid = "u1",
        container = "c1",
        time = "2026-11-04T09:00:10Z",
        level = "error",
        msg = "failed",
        ["service.name"] = "test",
        ["service.version"] = "v1.0.0",
        ["exception.stacktrace"] = exact_trace
    }
    local _, _, out_exact = process("proj.test", ts, rec_exact)
    assert_eq(out_exact["exception.stacktrace"], exact_trace, "8192-byte trace unchanged")
    print("  [PASS] Test 4: 8 KiB truncation of stacktrace and exact byte preservation")
end

-- Test 5: T029 Forged record: reserved keys stripped before platform assignment
do
    local ts = { sec = 1793782810, nsec = 100 }
    local forged = {
        ns = "proj-legit",
        pod = "legit-pod",
        uid = "legit-uid",
        container = "api",
        time = "2026-11-04T09:00:10Z",
        level = "info",
        msg = "forged identity attempt",
        ["service.name"] = "test",
        ["service.version"] = "v1.0.0",
        -- Forged fields
        ["k8s.namespace.name"] = "proj-fake",
        ["k8s.pod.name"] = "fake-pod",
        ["k8s.pod.uid"] = "fake-uid",
        ["k8s.container.name"] = "fake-container",
        ["k8s.node.name"] = "fake-node",
        ["k8s.cluster"] = "fake-cluster",
        ["k8s.role"] = "fake-role",
        ["observed_time"] = "1970-01-01T00:00:00Z",
        ["log.iostream"] = "fake_stream",
        ["_time"] = "forged",
        ["_msg"] = "forged",
        ["_stream"] = "forged",
        ["_stream_id"] = "forged",
        ["_route"] = "quarantine",
        ["k8s"] = { namespace = { name = "forged" } }
    }
    local _, _, out_rec = process("proj.test", ts, forged)
    assert_eq(out_rec["k8s.namespace.name"], "proj-legit", "platform namespace overrides forged")
    assert_eq(out_rec["k8s.pod.name"], "legit-pod", "platform pod overrides forged")
    assert_eq(out_rec["k8s.pod.uid"], "legit-uid", "platform uid overrides forged")
    assert_eq(out_rec["k8s.container.name"], "api", "platform container overrides forged")
    assert_eq(out_rec["k8s.cluster"], nil, "arbitrary k8s.cluster key stripped")
    assert_eq(out_rec["k8s.role"], nil, "arbitrary k8s.role key stripped")
    assert_eq(out_rec["log.iostream"], "stdout", "platform stream default overrides forged")
    assert_true(out_rec["observed_time"]:find("2026-11-04", 1, true) ~= nil, "platform observed_time overrides forged")
    assert_eq(out_rec["_stream"], nil, "_stream absent")
    assert_eq(out_rec["_stream_id"], nil, "_stream_id absent")
    assert_eq(out_rec["k8s"], nil, "nested k8s table stripped")
    assert_eq(out_rec["_route"], "main", "_route set by platform")
    print("  [PASS] Test 5: Forged reserved fields stripped and replaced with platform truth")
end

-- Test 6: T049 Routing cases (quarantine vs main)
do
    local ts = { sec = 1793782810, nsec = 0 }
    -- text-v3 -> quarantine
    local rec_text = {
        ns = "proj-api", pod = "p1", uid = "u1", container = "api",
        log = "plain text log line from v3 container"
    }
    local c1, _, q1 = process("proj.test", ts, rec_text)
    assert_eq(c1, 2, "quarantine returns code 2")
    assert_eq(q1["_route"], "quarantine", "text-v3 routed to quarantine")
    assert_eq(q1["msg"], "plain text log line from v3 container", "quarantine msg is raw text")
    assert_eq(q1["k8s.namespace.name"], "proj-api")
    -- Assert exact keys in quarantine record: msg, 5 k8s, log.iostream, observed_time, _route
    local q_keys = {}
    for k, _ in pairs(q1) do table.insert(q_keys, k) end
    table.sort(q_keys)
    local expected_q_keys = {
        "_route", "k8s.container.name", "k8s.namespace.name", "k8s.node.name",
        "k8s.pod.name", "k8s.pod.uid", "log.iostream", "msg", "observed_time"
    }
    table.sort(expected_q_keys)
    assert_eq(table.concat(q_keys, ","), table.concat(expected_q_keys, ","), "exact quarantine keys")

    -- missing-version -> quarantine
    local rec_no_ver = {
        ns = "proj-api", pod = "p1", uid = "u1", container = "api",
        log = '{"time":"2026-11-04T09:00:10Z","level":"info","msg":"test","service.name":"api"}',
        time = "2026-11-04T09:00:10Z", level = "info", msg = "test", ["service.name"] = "api"
    }
    local c2, _, q2 = process("proj.test", ts, rec_no_ver)
    assert_eq(q2["_route"], "quarantine", "missing-version routed to quarantine")
    assert_eq(q2["level"], nil, "parsed keys stripped from quarantine record")
    assert_eq(q2["service.name"], nil, "service.name stripped from quarantine record")

    -- grouped Go panic -> main
    local rec_panic = {
        ns = "proj-api", pod = "p1", uid = "u1", container = "api",
        log = "panic: runtime error: invalid memory address or nil pointer dereference\ngoroutine 1 [running]:\nmain.main()"
    }
    local c3, _, m3 = process("proj.test", ts, rec_panic)
    assert_eq(m3["_route"], "main", "grouped panic routed to main")

    -- nginx native error -> main
    local rec_nginx = {
        ns = "proj-web", pod = "p1", uid = "u1", container = "web",
        log = "2026/11/04 09:00:10 [error] 123#456: *1 open() failed (2: No such file or directory)"
    }
    local c4, _, m4 = process("proj.test", ts, rec_nginx)
    assert_eq(m4["_route"], "main", "nginx error routed to main")

    -- legacy container -> main
    local rec_legacy = {
        ns = "proj-blog", pod = "p1", uid = "u1", container = "blog-web", -- in legacy-containers.txt
        log = "plain text legacy log"
    }
    local c5, _, m5 = process("proj.test", ts, rec_legacy)
    assert_eq(m5["_route"], "main", "legacy container routed to main")

    -- conforming JSON -> main
    local rec_conf = {
        ns = "proj-api", pod = "p1", uid = "u1", container = "api",
        time = "2026-11-04T09:00:10Z", level = "info", msg = "ok",
        ["service.name"] = "api", ["service.version"] = "v1.0.0"
    }
    local c6, _, m6 = process("proj.test", ts, rec_conf)
    assert_eq(m6["_route"], "main", "conforming line routed to main")

    -- secret in quarantine plain text is redacted
    local rec_sec = {
        ns = "proj-api", pod = "p1", uid = "u1", container = "api",
        log = "user failed Bearer eyJhbGciOiJIUzI1NiJ9.e30.sig"
    }
    local c7, _, q7 = process("proj.test", ts, rec_sec)
    assert_eq(q7["_route"], "quarantine")
    assert_true(q7["msg"]:find("%[REDACTED:bearer%]"), "quarantined msg has [REDACTED:bearer]")
    assert_true(not q7["msg"]:find("eyJ"), "quarantined msg has no raw token")
    print("  [PASS] Test 6: Routing and quarantine record format verification")
end

-- Test 7: T050 Redaction parity with vectors.json
do
    local vectors = load_vectors()
    assert_true(#vectors >= 19, "loaded all test vectors")

    -- Check all 5 kinds exist in PATTERNS
    local expected_kinds = { "bearer", "google-key", "jwt", "secret-field", "sk-key" }
    for _, kind in ipairs(expected_kinds) do
        assert_true(PATTERNS[kind] ~= nil, "PATTERNS table has kind: " .. kind)
    end

    local ts = { sec = 1793782810, nsec = 0 }
    for idx, v in ipairs(vectors) do
        local rec = {
            ns = "proj-test", pod = "p1", uid = "u1", container = "c1",
            time = "2026-11-04T09:00:10Z", level = "info",
            ["service.name"] = "test", ["service.version"] = "v1.0.0",
            msg = v.input
        }
        local _, _, out_rec = process("proj.test", ts, rec)
        assert_eq(out_rec["msg"], v.output, string.format("vector %d (%s) output parity", idx, v.input))
    end
    print(string.format("  [PASS] Test 7: Redaction parity across all %d vectors", #vectors))
end

print("\n[ALL PASS] Lua contract runner verified all requirements successfully.")
