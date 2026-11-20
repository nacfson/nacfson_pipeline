#!/bin/sh
set -eu

VM=${OPS_VM:?}
OUT=${OPS_SAMPLES:-/var/lib/ops-agent/samples}
STATE=${OPS_STATE:-/var/lib/ops-agent/state}
EXPORTER=${OPS_EXPORTER:-http://127.0.0.1:7071/metrics}
DATA=${OPS_DATA_DIR:-/var/lib/kafka}
CAP_BYTES=${OPS_SAMPLES_CAP_BYTES:-8388608}

mkdir -p "$OUT" "$STATE"
metrics="$OUT/metrics.jsonl"
events="$OUT/events.jsonl"

for f in "$metrics" "$events"; do
	if [ -f "$f" ] && [ "$(wc -c <"$f")" -gt "$CAP_BYTES" ]; then
		: >"$f"
	fi
done

emit() {
	printf '{"schema":1,"source":"vm/%s","metric":"%s","value":%s,"unit":"%s"}\n' "$VM" "$1" "$2" "$3" >>"$metrics"
}

event() {
	printf '{"schema":1,"kind":"%s","state":"%s","subject":"%s","vm":"%s","observed_by":"%s","source":"sample","detail":"%s"}\n' \
		"$1" "$2" "$3" "$VM" "$VM" "$4" >>"$events"
}

scrape=$(curl -fsS --max-time 5 "$EXPORTER" 2>/dev/null || true)
pick() {
	printf '%s\n' "$scrape" | awk -v name="$1" '$1 == name { print $2; exit }'
}
sum_gc() {
	printf '%s\n' "$scrape" | awk '$1 ~ /^jvm_gc_collection_time_ms_total[{]/ { s += $2; n++ } END { if (n) printf "%.0f", s }'
}

for pair in \
	kafka.brokers.active:kafka_controller_activebrokercount:count \
	kafka.brokers.fenced:kafka_controller_fencedbrokercount:count \
	kafka.controller.active:kafka_controller_activecontrollercount:count \
	kafka.partitions.offline:kafka_controller_offlinepartitionscount:count \
	kafka.partitions.under_replicated:kafka_server_underreplicatedpartitions:count \
	kafka.partitions.under_min_isr:kafka_server_underminisrpartitioncount:count \
	kafka.produce.latency_p99:kafka_produce_total_time_ms_p99:ms \
	kafka.bytes_in:kafka_server_bytesinpersec_one_minute_rate:bytes/s \
	kafka.bytes_out:kafka_server_bytesoutpersec_one_minute_rate:bytes/s \
	kafka.heap.used:jvm_heap_used_bytes:bytes; do
	metric=${pair%%:*}
	rest=${pair#*:}
	name=${rest%%:*}
	unit=${rest#*:}
	value=$(pick "$name")
	if [ -n "$value" ]; then
		emit "$metric" "$value" "$unit"
	fi
done

gc=$(sum_gc)
if [ -n "$gc" ]; then
	prev=$(cat "$STATE/gc" 2>/dev/null || echo "$gc")
	echo "$gc" >"$STATE/gc"
	emit kafka.gc.time "$(awk -v a="$gc" -v b="$prev" 'BEGIN { d = (a - b) * 2; if (d < 0) d = 0; printf "%.0f", d }')" "ms per min"
fi

emit os.mem.available "$(awk '/^MemAvailable:/ { printf "%.0f", $2 * 1024 }' /proc/meminfo)" bytes

ratio=$(df -P "$DATA" | awk 'NR == 2 { printf "%.4f", ($2 - $3) / $2 }')
emit os.disk.free_ratio "$ratio" ratio

read -r _ u n s i w q sq st _ </proc/stat
total=$((u + n + s + i + w + q + sq + st))
prev=$(cat "$STATE/cpu" 2>/dev/null || echo "$total $st")
echo "$total $st" >"$STATE/cpu"
emit os.cpu.steal "$(echo "$prev" | awk -v t="$total" -v s="$st" '{ dt = t - $1; ds = s - $2; printf "%.4f", (dt > 0 ? ds / dt : 0) }')" ratio

low=$(awk -v r="$ratio" 'BEGIN { print (r < 0.20) ? 1 : 0 }')
was=$(cat "$STATE/disk-low" 2>/dev/null || echo 0)
if [ "$low" != "$was" ]; then
	echo "$low" >"$STATE/disk-low"
	if [ "$low" = 1 ]; then
		event disk.low warn "disk/$VM" "free space on $DATA is below 20%"
	else
		event disk.ok ok "disk/$VM" "free space on $DATA is back above 20%"
	fi
fi
