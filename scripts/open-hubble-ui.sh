#!/usr/bin/env bash
# Private Loopback Tunnel & Automated Live Verification for Hubble UI (Spec 010: FR-010, FR-013)
#
# Usage:
#   ./scripts/open-hubble-ui.sh [--port <local-port>]   # Open foreground tunnel on 127.0.0.1:12000
#   ./scripts/open-hubble-ui.sh --verify                # Run 5-point live cluster & flow verification

set -euo pipefail

PORT="12000"
MODE="tunnel"

usage() {
  cat <<EOF
Usage: $0 [--port <local-port>] [--verify]

Options:
  --port <port>   Local loopback port to bind on 127.0.0.1 (default: 12000)
  --verify        Run non-interactive 5-point live Cilium + Hubble verification (FR-013)
  -h, --help      Show this help message
EOF
}

while [ $# -gt 0 ]; do
  case "$1" in
    --port)
      if [ $# -lt 2 ]; then
        echo "Error: --port requires a port number" >&2
        exit 2
      fi
      PORT="$2"
      shift 2
      ;;
    --verify)
      MODE="verify"
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo "Error: Unknown argument '$1'" >&2
      usage >&2
      exit 2
      ;;
  esac
done

if ! command -v kubectl >/dev/null 2>&1; then
  echo "Error: kubectl is required but not found in PATH." >&2
  exit 1
fi

run_verify() {
  echo "=== [1/5] Verifying Cilium & Hubble control plane rollout & zero Grafana/Prometheus/Loki pods ==="
  kubectl -n kube-system rollout status daemonset/cilium --timeout=120s
  kubectl -n kube-system rollout status deployment/cilium-operator --timeout=120s
  kubectl -n kube-system rollout status deployment/hubble-relay --timeout=120s
  kubectl -n kube-system rollout status deployment/hubble-ui --timeout=120s
  EXTRA_OBS_PODS="$(kubectl get pods -A --no-headers 2>/dev/null | grep -Ei 'grafana|prometheus|loki' || true)"
  if [ -n "${EXTRA_OBS_PODS}" ]; then
    echo "FAIL: Found unexpected Grafana/Prometheus/Loki pods in Phase 1:" >&2
    printf '%s\n' "${EXTRA_OBS_PODS}" >&2
    exit 1
  fi
  echo "  ✓ Cilium + Hubble rollout complete and 0 Grafana/Prometheus/Loki pods running"

  echo "=== [2/5] Verifying OpenBao CiliumNetworkPolicy & zero 0.0.0.0/0 egress ==="
  kubectl -n vault get ciliumnetworkpolicy openbao-apiserver-egress >/dev/null
  OPENBAO_EGRESS="$(kubectl -n vault get networkpolicy openbao-ingress-policy -o jsonpath='{.spec.egress}')"
  if printf '%s' "${OPENBAO_EGRESS}" | grep -q '0\.0\.0\.0/0'; then
    echo "FAIL: NetworkPolicy/openbao-ingress-policy still contains 0.0.0.0/0 egress" >&2
    exit 1
  fi
  echo "  ✓ CiliumNetworkPolicy/openbao-apiserver-egress present and 0.0.0.0/0 egress removed"

  echo "=== [3/5] Verifying Hubble services are ClusterIP-only (:9965 metrics enabled) with zero public IngressRoutes ==="
  UI_TYPE="$(kubectl -n kube-system get svc hubble-ui -o jsonpath='{.spec.type}')"
  RELAY_TYPE="$(kubectl -n kube-system get svc hubble-relay -o jsonpath='{.spec.type}')"
  METRICS_PORT="$(kubectl -n kube-system get svc hubble-metrics -o jsonpath='{.spec.ports[0].port}')"
  if [ "${UI_TYPE}" != "ClusterIP" ] || [ "${RELAY_TYPE}" != "ClusterIP" ] || [ "${METRICS_PORT}" != "9965" ]; then
    echo "FAIL: Expected ClusterIP for hubble-ui (${UI_TYPE}) and hubble-relay (${RELAY_TYPE}), and port 9965 on hubble-metrics (${METRICS_PORT})" >&2
    exit 1
  fi
  INGRESS_ROUTES="$(kubectl get ingressroute -A -o jsonpath='{range .items[*]}{.metadata.namespace}/{.metadata.name}:{.spec.routes[*].services[*].name}{"\n"}{end}' 2>/dev/null || true)"
  if printf '%s\n' "${INGRESS_ROUTES}" | grep -Eq 'hubble-ui|hubble-relay|hubble-metrics'; then
    echo "FAIL: Found public IngressRoute referencing Hubble service:" >&2
    printf '%s\n' "${INGRESS_ROUTES}" | grep -E 'hubble-ui|hubble-relay|hubble-metrics' >&2
    exit 1
  fi
  echo "  ✓ hubble-ui, hubble-relay, and hubble-metrics (:9965) are ClusterIP-only with zero public IngressRoutes"

  echo "=== [4/5] Verifying Hubble UI HTTP 200 reachability over 127.0.0.1:${PORT} tunnel & :9965/metrics endpoint ==="
  PF_LOG="$(mktemp /tmp/hubble-ui-pf-XXXXXX.log)"
  kubectl -n kube-system port-forward --address 127.0.0.1 svc/hubble-ui "${PORT}:80" >"${PF_LOG}" 2>&1 &
  PF_PID=$!
  # Invoked only by the EXIT trap below. ShellCheck does not treat trap
  # callbacks as calls (https://www.shellcheck.net/wiki/SC2329).
  # shellcheck disable=SC2329
  cleanup_pf() {
    kill "${PF_PID}" >/dev/null 2>&1 || true
    rm -f "${PF_LOG}"
  }
  trap cleanup_pf EXIT

  HTTP_OK=0
  for _ in $(seq 1 15); do
    if curl -fsS --max-time 3 "http://127.0.0.1:${PORT}/" >/dev/null 2>&1; then
      HTTP_OK=1
      break
    fi
    sleep 1
  done
  if [ "${HTTP_OK}" -ne 1 ]; then
    echo "FAIL: Hubble UI did not respond with HTTP 200 on http://127.0.0.1:${PORT}/" >&2
    cat "${PF_LOG}" >&2 || true
    exit 1
  fi
  METRICS_CFG="$(kubectl -n kube-system get configmap cilium-config -o jsonpath='{.data.hubble-metrics-server}')"
  if [ "${METRICS_CFG}" != ":9965" ]; then
    echo "FAIL: Expected hubble-metrics-server ':9965' in cilium-config, got '${METRICS_CFG}'" >&2
    exit 1
  fi
  echo "  ✓ Hubble UI responded with HTTP 200 on http://127.0.0.1:${PORT}/ and :9965/metrics is active"

  echo "=== [5/5] Verifying live flow visibility via Hubble Relay ==="
  RELAY_POD_IP="$(kubectl -n kube-system get pod -l k8s-app=hubble-relay -o jsonpath='{.items[0].status.podIP}' 2>/dev/null || true)"
  FLOW_OUT="$(kubectl -n kube-system exec ds/cilium -c cilium-agent -- \
    hubble observe --server "${RELAY_POD_IP:-hubble-relay.kube-system.svc.cluster.local}:4245" --last 5 -o jsonpb 2>/dev/null || true)"
  if ! printf '%s\n' "${FLOW_OUT}" | grep -Eq '"verdict":\s*"(FORWARDED|DROPPED)"'; then
    echo "FAIL: Did not observe FORWARDED or DROPPED flow verdicts from Hubble Relay" >&2
    exit 1
  fi
  echo "  ✓ Observed live flows with explicit verdicts from Hubble Relay"
  echo "ALL 5 HUBBLE VERIFICATION CHECKS PASSED."
}

if [ "${MODE}" = "verify" ]; then
  run_verify
  exit 0
fi

echo "Verifying Hubble Relay and Hubble UI readiness in kube-system..."
kubectl -n kube-system rollout status deployment/hubble-relay --timeout=60s >/dev/null
kubectl -n kube-system rollout status deployment/hubble-ui --timeout=60s >/dev/null

echo "=================================================================="
echo "Hubble UI Private Loopback Tunnel"
echo "  Local URL:  http://127.0.0.1:${PORT}"
echo "  Remote SSH: ssh -L ${PORT}:127.0.0.1:${PORT} <vps-host>"
echo "Press Ctrl+C to close the tunnel."
echo "=================================================================="

exec kubectl -n kube-system port-forward --address 127.0.0.1 svc/hubble-ui "${PORT}:80"
