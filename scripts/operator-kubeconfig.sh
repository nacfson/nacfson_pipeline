#!/usr/bin/env bash
# ==============================================================================
# operator-kubeconfig.sh <vm> [<env>]
# Generates a short-lived (24h) read-only workstation kubeconfig for operator-readonly.
# See contracts/operations.md §13.
# ==============================================================================

set -euo pipefail

if [ $# -lt 1 ]; then
  echo "Usage: $0 <vm> [<env>]" >&2
  echo "Example: $0 local-k3s" >&2
  echo "         $0 user@vps-ip vps-k3s" >&2
  exit 1
fi

VM="$1"
ENV="${2:-$1}"

# Strip any user@ or domain suffix for standard naming if not explicitly provided
if [ $# -lt 2 ]; then
  ENV="$(echo "$VM" | sed -E 's/^.*@//' | sed -E 's/\..*$//')"
fi

echo "Requesting 24h token for ServiceAccount governance/operator-readonly on $VM..."
TOKEN=$(ssh "$VM" "sudo k3s kubectl -n governance create token operator-readonly --duration=24h")

if [ -z "$TOKEN" ]; then
  echo "Error: Failed to obtain token for operator-readonly." >&2
  exit 1
fi

echo "Retrieving cluster CA certificate from $VM..."
CA_DATA=$(ssh "$VM" "sudo k3s kubectl config view --raw -o jsonpath='{.clusters[0].cluster.certificate-authority-data}'")

if [ -z "$CA_DATA" ]; then
  echo "Error: Failed to obtain cluster CA certificate data." >&2
  exit 1
fi

KUBE_DIR="${HOME}/.kube"
mkdir -p "$KUBE_DIR"
OUTPUT_FILE="${KUBE_DIR}/nacfson-${ENV}.yaml"

cat <<EOF > "$OUTPUT_FILE"
apiVersion: v1
kind: Config
clusters:
- cluster:
    certificate-authority-data: ${CA_DATA}
    server: https://127.0.0.1:6443
  name: nacfson-${ENV}
contexts:
- context:
    cluster: nacfson-${ENV}
    namespace: governance
    user: operator-readonly
  name: nacfson-${ENV}
current-context: nacfson-${ENV}
users:
- name: operator-readonly
  user:
    token: ${TOKEN}
EOF

chmod 600 "$OUTPUT_FILE"
echo "Successfully wrote read-only kubeconfig to $OUTPUT_FILE"
echo "Test connection with:"
echo "  KUBECONFIG=$OUTPUT_FILE kubectl auth can-i create deployments -n identity   # Expected: no"
echo "  KUBECONFIG=$OUTPUT_FILE flux get kustomizations -A"
