local VM = os.getenv("OPS_VM") or "unknown"
local REPLICAS = tonumber(os.getenv("OPS_REPLICATION") or "3")
local DETAIL_MAX = 1024

local UNIT_STARTED = "39f53479d3a045ac8e11786248231fbf"
local UNIT_FAILED = "d9b373ed55a64feb8242e02dbe79a49c"
local UNIT_STOPPED = "9d1aaa27d60140bd96365438aad20286"

local function split_map(s)
    local out = {}
    for key, value in string.gmatch(s or "", "([^,=]+)=([^,]+)") do
        out[key] = value
    end
    return out
end

local function split_set(s)
    local out = {}
    for item in string.gmatch(s or "", "([^,]+)") do
        out[item] = true
    end
    return out
end

local NODES = split_map(os.getenv("OPS_NODES") or "")
local WATCHED = split_set(os.getenv("OPS_UNITS") or "kafka.service,wg-quick@wg0.service,ops-agent.service")

local function event(kind, state, subject, vm, source, detail)
    return {
        schema = 1,
        kind = kind,
        state = state,
        subject = subject,
        vm = vm,
        observed_by = VM,
        source = source,
        detail = string.sub(detail or "", 1, DETAIL_MAX),
    }
end

local function broker_vm(id)
    return NODES[id] or ("node-" .. id)
end

local function replica_count(list)
    local n = 0
    for _ in string.gmatch(list, "%d+") do
        n = n + 1
    end
    return n
end

local function partition_event(topic, partition, isr, msg)
    if string.sub(topic, 1, 2) == "__" then
        return -1, 0, nil
    end
    local subject = "kafka/partition/" .. topic .. "-" .. partition
    if replica_count(isr) >= REPLICAS then
        return 2, 0, event("partition.isr_expand", "up", subject, VM, "kafka-broker-log", msg)
    end
    return 2, 0, event("partition.isr_shrink", "down", subject, VM, "kafka-broker-log", msg)
end

function kafka_line(tag, ts, record)
    local msg = record["MESSAGE"] or record["log"]
    if type(msg) ~= "string" then
        return -1, ts, record
    end
    local broker = string.match(msg, "Fencing broker (%d+) at epoch")
        or string.match(msg, "The request from broker (%d+) to shut down has been granted")
        or string.match(msg, "Replayed BrokerRegistrationChangeRecord modifying the registration for broker (%d+):.*fenced=1,")
    if broker then
        return 2, ts, event("broker.fenced", "down", "kafka/broker-" .. broker, broker_vm(broker), "kafka-controller-log", msg)
    end
    broker = string.match(msg, "The request from broker (%d+) to unfence has been granted")
        or string.match(msg, "Replayed BrokerRegistrationChangeRecord modifying the registration for broker (%d+):.*fenced=%-1,")
    if broker then
        return 2, ts, event("broker.unfenced", "up", "kafka/broker-" .. broker, broker_vm(broker), "kafka-controller-log", msg)
    end
    local topic, partition, isr = string.match(msg, "%[Partition (%S+)%-(%d+) broker=%d+%] Shrinking ISR from [%d,]+ to ([%d,]+)")
    if not topic then
        topic, partition, isr = string.match(msg, "%[Partition (%S+)%-(%d+) broker=%d+%] ISR updated to ([%d,]+)")
    end
    if topic then
        local code, _, out = partition_event(topic, partition, isr, msg)
        return code, ts, out or record
    end
    return -1, ts, record
end

function unit_record(tag, ts, record)
    local unit = record["UNIT"] or record["USER_UNIT"]
    if not unit or not WATCHED[unit] then
        return -1, ts, record
    end
    local id = record["MESSAGE_ID"]
    local subject = "unit/" .. VM .. "/" .. unit
    if id == UNIT_STARTED then
        return 2, ts, event("unit.active", "up", subject, VM, "systemd", record["MESSAGE"])
    end
    if id == UNIT_FAILED or id == UNIT_STOPPED then
        return 2, ts, event("unit.failed", "down", subject, VM, "systemd", record["MESSAGE"])
    end
    return -1, ts, record
end

function heartbeat(tag, ts, record)
    return 2, ts, event("agent.heartbeat", "ok", "vm/" .. VM, VM, "heartbeat", "")
end
