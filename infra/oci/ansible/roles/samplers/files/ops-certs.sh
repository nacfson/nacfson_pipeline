#!/bin/sh
set -eu

VM=${OPS_VM:?}
OUT=${OPS_SAMPLES:-/var/lib/ops-agent/samples}
STATE=${OPS_STATE:-/var/lib/ops-agent/state}
TLS=${OPS_TLS_DIR:-/etc/kafka/tls}
LOKI_TLS=${OPS_LOKI_TLS_DIR:-/etc/loki/tls}
WARN_DAYS=${OPS_CERT_WARN_DAYS:-14}

mkdir -p "$OUT" "$STATE"
now=$(date +%s)
min=""
for dir in "$TLS" "$LOKI_TLS"; do
	if [ -z "$dir" ] || [ ! -d "$dir" ]; then
		continue
	fi
	for cert in "$dir"/*.crt; do
		if [ ! -f "$cert" ]; then
			continue
		fi
		[ "$(basename "$cert")" = ca.crt ] && continue
		end=$(date -d "$(openssl x509 -enddate -noout -in "$cert" | cut -d= -f2)" +%s)
		days=$(((end - now) / 86400))
		if [ -z "$min" ] || [ "$days" -lt "$min" ]; then
			min=$days
		fi
		id=$(basename "$cert" .crt)
		expiring=$([ "$days" -lt "$WARN_DAYS" ] && echo 1 || echo 0)
		was=$(cat "$STATE/cert-$id" 2>/dev/null || echo 0)
		if [ "$expiring" != "$was" ]; then
			echo "$expiring" >"$STATE/cert-$id"
			if [ "$expiring" = 1 ]; then
				kind=cert.expiring state=warn detail="$id has $days days left"
			else
				kind=cert.renewed state=ok detail="$id renewed, $days days left"
			fi
			printf '{"schema":1,"kind":"%s","state":"%s","subject":"cert/%s/%s","vm":"%s","observed_by":"%s","source":"cert-check","detail":"%s"}\n' \
				"$kind" "$state" "$VM" "$id" "$VM" "$VM" "$detail" >>"$OUT/events.jsonl"
		fi
	done
done
if [ -n "$min" ]; then
	printf '{"schema":1,"source":"vm/%s","metric":"cert.days_left.min","value":%s,"unit":"days"}\n' "$VM" "$min" >>"$OUT/metrics.jsonl"
fi
