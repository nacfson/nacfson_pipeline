#!/usr/bin/env bash
# ==============================================================================
# Vault Registry Proxy Retirement Verification (Spec 012 FR-004)
# Asserts that vault-registry-proxy manifests, OpenBao policy, K3s mirror
# template, and Go binary have been cleanly retired in favor of public GAR
# @sha256 digest pulls with zero pull credentials.
# ==============================================================================

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "${SCRIPT_DIR}/../.." && pwd)"
cd "${ROOT_DIR}"

echo "=== [Test] Vault Registry Proxy Retirement Check (Spec 012) ==="

RETIRED_PATHS=(
  "deploy/platform/vault-proxies/registry-proxy.yaml"
  "deploy/platform/vault/policies/registry-proxy.hcl"
  "deploy/platform/vault/registries.yaml.template"
  "services/vault-proxies/cmd/registry-proxy"
  "services/vault-proxies/Dockerfile.registry-proxy"
)

for p in "${RETIRED_PATHS[@]}"; do
  if [ -e "${p}" ]; then
    echo "FAIL: Retired path still exists: ${p}"
    exit 1
  fi
done
echo "✓ PASS: All retired vault-registry-proxy files are absent."

if grep -q "vault-registry-proxy" deploy/platform/vault/service-accounts.yaml deploy/platform/vault-proxies/kustomization.yaml; then
  echo "FAIL: Active Vault manifests still reference vault-registry-proxy."
  exit 1
fi
echo "✓ PASS: Vault manifests contain zero vault-registry-proxy references."

(cd services/vault-proxies && go test ./... && go vet ./...)
echo "✓ PASS: Remaining Vault proxies compile and pass unit tests."
