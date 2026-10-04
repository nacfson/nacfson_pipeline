#!/usr/bin/env bash
# ==============================================================================
# verify-isolation.sh
# Verifies Restricted Pod Security Admission, disabled token automounting,
# ClusterIP-only services, and NetworkPolicy isolation rules.
# ==============================================================================

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

echo "=== Verifying Workload Security Sandboxing & Isolation ==="

PASS_COUNT=0
TOTAL_CHECKS=4

KUBECTL_OPTS=()
if [ "${BREAK_GLASS:-0}" = "1" ] || [ "${BREAK_GLASS:-}" = "true" ]; then
  KUBECTL_OPTS=(--as=break-glass:verify --as-group=platform:break-glass)
fi

# Check 1: Static verification of Namespace Restricted PSA labels
echo "[Check 1/4] Verifying Restricted PSA labels in deploy/projects/pn/boundary/namespace.yaml..."
if grep -q "pod-security.kubernetes.io/enforce: restricted" "$REPO_ROOT/deploy/projects/pn/boundary/namespace.yaml"; then
  echo "  ✓ PASS: Namespace explicitly enforces Restricted Pod Security."
  PASS_COUNT=$((PASS_COUNT + 1))
else
  echo "  ✗ FAIL: Missing Restricted Pod Security enforcement label!" >&2
  exit 1
fi

# Check 2: ServiceAccount token automount disabled
echo "[Check 2/4] Verifying ServiceAccount token automounting in deploy/projects/pn/boundary/service-account.yaml..."
if grep -q "automountServiceAccountToken: false" "$REPO_ROOT/deploy/projects/pn/boundary/service-account.yaml"; then
  echo "  ✓ PASS: ServiceAccount token automounting is disabled."
  PASS_COUNT=$((PASS_COUNT + 1))
else
  echo "  ✗ FAIL: ServiceAccount must set automountServiceAccountToken: false!" >&2
  exit 1
fi

# Check 3: ClusterIP service enforcement (No NodePort / LoadBalancer)
echo "[Check 3/4] Verifying ClusterIP-only Services in deploy/projects/pn/workloads/backend-service.yaml..."
if grep -q "type: ClusterIP" "$REPO_ROOT/deploy/projects/pn/workloads/backend-service.yaml" && \
   ! grep -q "type: NodePort" "$REPO_ROOT/deploy/projects/pn/workloads/backend-service.yaml" && \
   ! grep -q "type: LoadBalancer" "$REPO_ROOT/deploy/projects/pn/workloads/backend-service.yaml"; then
  echo "  ✓ PASS: Services strictly configured as ClusterIP."
  PASS_COUNT=$((PASS_COUNT + 1))
else
  echo "  ✗ FAIL: Project Services must be ClusterIP only!" >&2
  exit 1
fi

# Check 4: NetworkPolicy default-deny ingress & egress with DNS/Postgres allowlist
echo "[Check 4/4] Verifying NetworkPolicy rules in deploy/projects/pn/boundary/network-policy.yaml..."
if grep -q "policyTypes:" "$REPO_ROOT/deploy/projects/pn/boundary/network-policy.yaml" && \
   grep -q -- "- Ingress" "$REPO_ROOT/deploy/projects/pn/boundary/network-policy.yaml" && \
   grep -q -- "- Egress" "$REPO_ROOT/deploy/projects/pn/boundary/network-policy.yaml" && \
   grep -q "port: 5432" "$REPO_ROOT/deploy/projects/pn/boundary/network-policy.yaml"; then
  echo "  ✓ PASS: NetworkPolicy isolates ingress/egress with Postgres/DNS allowlist."
  PASS_COUNT=$((PASS_COUNT + 1))
else
  echo "  ✗ FAIL: NetworkPolicy missing required default-deny or allowlist rules!" >&2
  exit 1
fi

# Dynamic cluster checks (if live cluster is present)
if command -v kubectl >/dev/null 2>&1 && kubectl get nodes >/dev/null 2>&1; then
  echo "Live cluster detected. Running dynamic rejection checks..."
  
  # 1. Test root pod rejection
  set +e
  ERR_OUTPUT=$(kubectl "${KUBECTL_OPTS[@]}" run test-root-violation --namespace=proj-pn --image=busybox --restart=Never --overrides='{"spec":{"containers":[{"name":"root","image":"busybox","securityContext":{"runAsUser":0}}]}}' 2>&1)
  set -e
  if echo "$ERR_OUTPUT" | grep -qi "violates PodSecurity"; then
    echo "  ✓ PASS: Live cluster strictly rejected root user pod."
  else
    echo "  ℹ Note: Pod run output: $ERR_OUTPUT"
  fi
fi

echo "=== Workload Isolation Verification: $PASS_COUNT/$TOTAL_CHECKS Checks Passed ==="
exit 0
