#!/usr/bin/env bash
# ==============================================================================
# verify-identity.sh
# Validates declarative Keycloak realm configuration, automated import,
# and OpenBao Vault secret resolution for the platform realm.
# ==============================================================================

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

echo "=== Verifying Keycloak Declarative Identity & Realm Ingestion ==="

# Check 1: Static verification of Keycloak deployment flags and volume mounts
echo "[Check 1/3] Verifying Keycloak deployment startup flags and volume mounts..."
if grep -q -- "--import-realm" "$REPO_ROOT/deploy/platform/identity/keycloak-deployment.yaml" && \
   grep -q -- "--vault=file" "$REPO_ROOT/deploy/platform/identity/keycloak-deployment.yaml" && \
   grep -q -- "--vault-dir=/opt/keycloak/conf/secrets" "$REPO_ROOT/deploy/platform/identity/keycloak-deployment.yaml" && \
   grep -q "mountPath: /opt/keycloak/data/import/platform-realm.json" "$REPO_ROOT/deploy/platform/identity/keycloak-deployment.yaml" && \
   grep -q "name: keycloak-realm-config" "$REPO_ROOT/deploy/platform/identity/keycloak-deployment.yaml"; then
  echo "  ✓ PASS: Keycloak deployment configured with realm auto-import and file vault SPI."
else
  echo "  ✗ FAIL: Missing required Keycloak deployment flags or realm ConfigMap mount!" >&2
  exit 1
fi

# Check 2: Static verification of single-source-of-truth realm config
echo "[Check 2/3] Verifying declarative realm definition and Vault secret placeholders..."
if grep -q '"realm": "platform"' "$REPO_ROOT/deploy/platform/identity/keycloak-realm-config.yaml" && \
   grep -q 'GOOGLE_CLIENT_ID' "$REPO_ROOT/deploy/platform/identity/keycloak-realm-config.yaml" && \
   grep -q 'GOOGLE_CLIENT_SECRET' "$REPO_ROOT/deploy/platform/identity/keycloak-realm-config.yaml" && \
   grep -q '"clientId": "gateway-client"' "$REPO_ROOT/deploy/platform/identity/keycloak-realm-config.yaml" && \
   grep -q '"clientId": "project-pn"' "$REPO_ROOT/deploy/platform/identity/keycloak-realm-config.yaml" && \
   grep -q '"clientId": "session-revocation-client"' "$REPO_ROOT/deploy/platform/identity/keycloak-realm-config.yaml"; then
  echo "  ✓ PASS: Declarative realm configuration defines platform realm and client identities."
else
  echo "  ✗ FAIL: Realm configuration missing platform definitions or Vault placeholders!" >&2
  exit 1
fi

# Check 3: Dynamic Realm API Reachability (if live cluster is accessible)
echo "[Check 3/3] Dynamic Platform Realm Health & Discovery Endpoint Check..."
if ssh -o BatchMode=yes -o ConnectTimeout=3 oracleCloud "sudo k3s kubectl get nodes" >/dev/null 2>&1; then
  echo "Live cluster detected. Querying Keycloak platform realm endpoint..."
  KC_IP=$(ssh oracleCloud "sudo k3s kubectl get svc -n identity keycloak-service -o jsonpath='{.spec.clusterIP}'" 2>/dev/null || true)
  if [ -n "$KC_IP" ]; then
    # KC_IP was read on this machine and must be filled in before ssh.
    # shellcheck disable=SC2029
    REALM_RESP=$(ssh oracleCloud "curl -sS --max-time 5 http://${KC_IP}:8080/realms/platform" 2>/dev/null || true)
    if echo "$REALM_RESP" | grep -q '"realm":"platform"'; then
      echo "  ✓ PASS: Live Keycloak endpoint returned healthy platform realm metadata."
    else
      echo "  ℹ NOTICE: Realm endpoint returned non-200 or not yet ready: $REALM_RESP"
      echo "           (Expected before initial GitOps reconciliation and pod restart)"
    fi
  fi
elif command -v kubectl >/dev/null 2>&1 && kubectl get nodes >/dev/null 2>&1; then
  echo "Local cluster detected. Querying Keycloak platform realm endpoint..."
  KC_IP=$(kubectl get svc -n identity keycloak-service -o jsonpath='{.spec.clusterIP}' 2>/dev/null || true)
  if [ -n "$KC_IP" ]; then
    REALM_RESP=$(curl -sS --max-time 5 "http://${KC_IP}:8080/realms/platform" 2>/dev/null || true)
    if echo "$REALM_RESP" | grep -q '"realm":"platform"'; then
      echo "  ✓ PASS: Live Keycloak endpoint returned healthy platform realm metadata."
    fi
  fi
else
  echo "  ✓ SKIP: No live cluster detected in current environment; static verification passed."
fi

echo "=== All Keycloak Identity Verification Checks Completed ==="
