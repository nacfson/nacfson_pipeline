#!/bin/sh
set -eu

ROOT=$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)
DIR="$ROOT/tests/kafka/rehearsal"
RUN="$DIR/.run"
COMPOSE_FILE="$DIR/compose.generated.yaml"

usage() {
	echo "usage: $0 up | down | capture-logs | test all | test <story1|story2|story3|alerts|security|declared|ops_events_lua>" >&2
	exit 2
}

identities_for() {
	python3 - "$1" <<'EOF'
import sys
import yaml
doc = yaml.safe_load(open("infra/kafka/cluster.yaml", encoding="utf-8"))
node = next(n for n in doc["nodes"] if n["node_id"] == int(sys.argv[1]))
broker_index = sorted(n["node_id"] for n in doc["nodes"] if "broker" in n["roles"])
names = []
if "broker" in node["roles"]:
    names.append(f"broker-{broker_index.index(node['node_id']) + 1}")
if "controller" in node["roles"]:
    names.append(f"controller-{node['node_id']}")
print(" ".join(names))
EOF
}

node_ids() {
	python3 -c 'import yaml; print(" ".join(str(n["node_id"]) for n in yaml.safe_load(open("infra/kafka/cluster.yaml"))["nodes"]))'
}

all_identities() {
	python3 -c 'import yaml; print(" ".join(i["name"] for i in yaml.safe_load(open("infra/kafka/cluster.yaml"))["identities"]))'
}

issue() {
	name=$1
	sans=$2
	out=$3
	ext="$RUN/ca/$name.ext"
	{
		echo "basicConstraints=CA:FALSE"
		echo "keyUsage=digitalSignature"
		echo "extendedKeyUsage=serverAuth,clientAuth"
		if [ -n "$sans" ]; then echo "subjectAltName=$sans"; fi
	} >"$ext"
	openssl req -new -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes \
		-keyout "$out/$name.key" -subj "/CN=$name" -out "$RUN/ca/$name.csr" 2>/dev/null
	openssl x509 -req -in "$RUN/ca/$name.csr" -CA "$RUN/ca/ca.crt" -CAkey "$RUN/ca/ca.key" \
		-CAcreateserial -days 2 -extfile "$ext" -out "$out/$name.crt" 2>/dev/null
	cat "$out/$name.key" "$out/$name.crt" >"$out/$name.pem"
}

certificates() {
	mkdir -p "$RUN/ca" "$RUN/clients"
	openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes -days 2 \
		-keyout "$RUN/ca/ca.key" -out "$RUN/ca/ca.crt" -subj "/CN=kafka-rehearsal-ca" 2>/dev/null
	for id in $(node_ids); do
		tls="$RUN/tls/node-$id"
		mkdir -p "$tls"
		cp "$RUN/ca/ca.crt" "$tls/ca.crt"
		for name in $(identities_for "$id"); do
			issue "$name" "DNS:node-$id,DNS:$(echo "$name" | sed 's/^controller-.*/node-'"$id"'/')" "$tls"
		done
	done
	cp "$RUN/ca/ca.crt" "$RUN/clients/ca.crt"
	for name in $(all_identities); do
		issue "$name" "" "$RUN/clients"
		{
			echo "security.protocol=SSL"
			echo "ssl.keystore.type=PEM"
			echo "ssl.keystore.location=/etc/kafka-node/clients/$name.pem"
			echo "ssl.truststore.type=PEM"
			echo "ssl.truststore.location=/etc/kafka-node/clients/ca.crt"
		} >"$RUN/clients/$name.properties"
	done
	chmod -R go-rwx "$RUN"
	podman unshare chown -R 1000:1000 "$RUN/tls" "$RUN/clients"
}

up() {
	if [ -d "$RUN" ]; then
		echo "rehearsal already up; run '$0 down' first" >&2
		exit 1
	fi
	cd "$ROOT"
	python3 scripts/kafka-declarations.py render --target compose
	certificates
	python3 -m tests.kafka.harness format
	podman-compose -f "$COMPOSE_FILE" up -d
	python3 -m tests.kafka.harness wait
	python3 -m tests.kafka.harness declare
}

down() {
	cd "$ROOT"
	podman-compose -f "$COMPOSE_FILE" down --volumes >/dev/null 2>&1 || true
	if [ -d "$RUN" ]; then
		podman unshare rm -rf "$RUN"
	fi
	echo "rehearsal removed"
}

[ $# -ge 1 ] || usage
case "$1" in
up) up ;;
down) down ;;
capture-logs)
	cd "$ROOT"
	exec python3 -m tests.kafka.harness capture-logs
	;;
test)
	[ $# -ge 2 ] || usage
	suite=$2
	shift 2
	cd "$ROOT"
	if [ "$suite" = all ]; then
		set -- tests.kafka.test_story1 tests.kafka.test_declared tests.kafka.test_story2 tests.kafka.test_security tests.kafka.test_alerts tests.kafka.test_ops_events_lua tests.kafka.test_story3
		exec python3 -m unittest -v "$@"
	fi
	exec python3 -m unittest -v "tests.kafka.test_$suite" "$@"
	;;
*) usage ;;
esac
