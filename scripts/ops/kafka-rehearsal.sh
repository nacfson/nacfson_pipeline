#!/bin/sh
set -eu

ROOT=$(CDPATH='' cd -- "$(dirname -- "$0")/../.." && pwd)
DIR="$ROOT/tests/kafka/rehearsal"
RUN="$DIR/.run"
COMPOSE_FILE="$DIR/compose.generated.yaml"
OBS_COMPOSE_FILE="$DIR/compose.observability.yaml"

usage() {
	echo "usage: $0 up [--with-observability] | down [--with-observability] | capture-logs | test all | test <story1|story2|story3|alerts|security|declared|ops_events_lua|observability_pipeline>" >&2
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
		-addext "basicConstraints=critical,CA:TRUE" \
		-addext "keyUsage=critical,keyCertSign,cRLSign" \
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

observability_setup() {
	mkdir -p \
		"$RUN/obs-tls/loki" \
		"$RUN/obs-tls/vector" \
		"$RUN/obs-tls/error-detector" \
		"$RUN/obs-tls/ops-relay" \
		"$RUN/obs-tls/test-client" \
		"$RUN/data/loki/chunks" \
		"$RUN/data/loki/rules" \
		"$RUN/data/loki/compactor" \
		"$RUN/data/loki/wal" \
		"$RUN/data/vector" \
		"$RUN/bin"

	cp "$RUN/ca/ca.crt" "$RUN/obs-tls/loki/ca.crt"
	issue "loki-a1-2" "DNS:loki,DNS:a1-2,IP:127.0.0.1" "$RUN/obs-tls/loki"
	chmod 0755 "$RUN/obs-tls/loki"
	chmod 0644 "$RUN/obs-tls/loki/ca.crt" "$RUN/obs-tls/loki/loki-a1-2.crt"
	chmod 0600 "$RUN/obs-tls/loki/loki-a1-2.key" "$RUN/obs-tls/loki/loki-a1-2.pem"
	podman unshare chown -R 10001:10001 "$RUN/obs-tls/loki" "$RUN/data/loki"

	podman unshare sh -c '
		set -eu
		clients="$1"
		obs="$2"
		data="$3"

		cp "$clients/ca.crt" "$obs/vector/ca.crt"
		cp "$clients/vector-a1-2.crt" "$obs/vector/vector-a1-2.crt"
		cp "$clients/vector-a1-2.key" "$obs/vector/vector-a1-2.key"
		chmod 0700 "$obs/vector" "$data/vector"
		chmod 0644 "$obs/vector/ca.crt"
		chmod 0640 "$obs/vector/vector-a1-2.crt"
		chmod 0600 "$obs/vector/vector-a1-2.key"
		chown -R 10002:10002 "$obs/vector" "$data/vector"

		cp "$clients/ca.crt" "$obs/error-detector/ca.crt"
		cp "$clients/error-detector-a1-2.crt" "$obs/error-detector/tls.crt"
		cp "$clients/error-detector-a1-2.key" "$obs/error-detector/tls.key"
		chmod 0750 "$obs/error-detector"
		chmod 0644 "$obs/error-detector/ca.crt"
		chmod 0640 "$obs/error-detector/tls.crt"
		chmod 0600 "$obs/error-detector/tls.key"
		chown -R 65532:65532 "$obs/error-detector"

		cp "$clients/ca.crt" "$obs/ops-relay/ca.crt"
		cp "$clients/ops-relay.crt" "$obs/ops-relay/tls.crt"
		cp "$clients/ops-relay.key" "$obs/ops-relay/tls.key"
		chmod 0750 "$obs/ops-relay"
		chmod 0644 "$obs/ops-relay/ca.crt"
		chmod 0640 "$obs/ops-relay/tls.crt"
		chmod 0600 "$obs/ops-relay/tls.key"
		chown -R 65532:65532 "$obs/ops-relay"

		cp "$clients/ca.crt" "$obs/test-client/ca.crt"
		cp "$clients/vector-a1-2.crt" "$obs/test-client/client.crt"
		cp "$clients/vector-a1-2.key" "$obs/test-client/client.key"
		chown 0:0 "$obs/test-client" "$obs/test-client/ca.crt" "$obs/test-client/client.crt" "$obs/test-client/client.key"
		chmod 0700 "$obs/test-client"
		chmod 0644 "$obs/test-client/ca.crt" "$obs/test-client/client.crt"
		chmod 0600 "$obs/test-client/client.key"
	' sh "$RUN/clients" "$RUN/obs-tls" "$RUN/data"

	(
		cd "$ROOT/tests/kafka/fakenotify"
		CGO_ENABLED=0 GO111MODULE=off go build -o "$RUN/bin/fakenotify" main.go
	)
	chmod 0755 "$RUN/bin/fakenotify"
}

up() {
	with_obs=0
	while [ $# -gt 0 ]; do
		case "$1" in
		--with-observability) with_obs=1 ;;
		*) usage ;;
		esac
		shift
	done
	if [ -d "$RUN" ]; then
		echo "rehearsal already up; run '$0 down' first" >&2
		exit 1
	fi
	cd "$ROOT"
	python3 scripts/ci/kafka-declarations.py render --target compose
	certificates
	python3 -m tests.kafka.harness format
	podman-compose -f "$COMPOSE_FILE" up -d
	python3 -m tests.kafka.harness wait
	python3 -m tests.kafka.harness declare
	if [ "$with_obs" = 1 ]; then
		observability_setup
		podman-compose -f "$COMPOSE_FILE" -f "$OBS_COMPOSE_FILE" up -d loki vector fakenotify error-detector ops-relay
	fi
}

down() {
	with_obs=0
	while [ $# -gt 0 ]; do
		case "$1" in
		--with-observability) with_obs=1 ;;
		*) usage ;;
		esac
		shift
	done
	cd "$ROOT"
	if [ "$with_obs" = 1 ] || [ -d "$RUN/obs-tls" ]; then
		podman-compose -f "$COMPOSE_FILE" -f "$OBS_COMPOSE_FILE" down --volumes >/dev/null 2>&1 || true
		podman rm -f kafka-rehearsal-loki kafka-rehearsal-vector kafka-rehearsal-fakenotify kafka-rehearsal-error-detector kafka-rehearsal-ops-relay >/dev/null 2>&1 || true
	fi
	podman-compose -f "$COMPOSE_FILE" down --volumes >/dev/null 2>&1 || true
	if [ -d "$RUN" ]; then
		podman unshare rm -rf "$RUN"
	fi
	echo "rehearsal removed"
}

[ $# -ge 1 ] || usage
cmd=$1
shift
case "$cmd" in
up) up "$@" ;;
down) down "$@" ;;
capture-logs)
	cd "$ROOT"
	exec python3 -m tests.kafka.harness capture-logs
	;;
test)
	[ $# -ge 1 ] || usage
	suite=$1
	shift
	cd "$ROOT"
	if [ "$suite" = all ]; then
		set -- tests.kafka.test_story1 tests.kafka.test_declared tests.kafka.test_story2 tests.kafka.test_security tests.kafka.test_alerts tests.kafka.test_ops_events_lua tests.kafka.test_story3
		exec python3 -m unittest -v "$@"
	fi
	exec python3 -m unittest -v "tests.kafka.test_$suite" "$@"
	;;
*) usage ;;
esac
