#!/usr/bin/env bash
set -euo pipefail

# Render and Validate Script (contracts/reconciliation-layers.md, contracts/platform-preflight.md)
# Usage: ./scripts/render-validate.sh <env> [<git-ref>]

ENV="${1:-}"
REF="${2:-}"

if [ -z "$ENV" ]; then
  echo "Usage: $0 <environment> [<git-ref>]" >&2
  exit 2
fi

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"

# If a git ref is specified, create a worktree, run there, and exit
if [ -n "$REF" ]; then
  WORKTREE_DIR="$(mktemp -d "/tmp/render-validate-worktree-XXXXXX")"
  trap 'rm -rf "${WORKTREE_DIR}"' EXIT
  git worktree add --detach "${WORKTREE_DIR}" "${REF}" >/dev/null 2>&1 || {
    echo "Warning: git worktree add failed for ref '${REF}'. Baseline render will be empty." >&2
    touch /tmp/empty-baseline.yaml
    echo "/tmp/empty-baseline.yaml"
    exit 0
  }
  cd "${WORKTREE_DIR}"
  if [ ! -d "clusters/${ENV}" ]; then
    echo "Notice: Ref '${REF}' has no clusters/${ENV}/; output is empty." >&2
    touch /tmp/empty-baseline.yaml
    echo "/tmp/empty-baseline.yaml"
    exit 0
  fi
  bash "${WORKTREE_DIR}/scripts/render-validate.sh" "${ENV}"
  exit 0
fi

cd "${ROOT_DIR}"

RENDER_FILE="$(mktemp "/tmp/render-${ENV}-XXXXXX")"
mv "${RENDER_FILE}" "${RENDER_FILE}.yaml"
RENDER_FILE="${RENDER_FILE}.yaml"
: > "${RENDER_FILE}"

echo "=== Rendering clusters/${ENV}/flux-system ===" >&2
kubectl kustomize "clusters/${ENV}/flux-system" >> "${RENDER_FILE}"
echo "---" >> "${RENDER_FILE}"

echo "=== Rendering layers from clusters/${ENV}/layers.yaml ===" >&2
python3 -c '
import yaml, subprocess, sys

env = sys.argv[1]
render_file = sys.argv[2]
layers_file = f"clusters/{env}/layers.yaml"

with open(layers_file, "r") as f:
    docs = [d for d in yaml.safe_load_all(f) if d]

for doc in docs:
    name = doc["metadata"]["name"]
    path = doc["spec"]["path"].replace("<env>", env)
    cmd = ["flux", "build", "kustomization", name, "--path", path, "--kustomization-file", layers_file, "--dry-run"]
    res = subprocess.run(cmd, capture_output=True, text=True)
    if res.returncode != 0:
        sys.stderr.write(f"Error building layer {name}: {res.stderr}\n")
        sys.exit(1)
    with open(render_file, "a") as rf:
        rf.write(res.stdout)
        rf.write("\n---\n")
' "${ENV}" "${RENDER_FILE}"

echo "=== Rendering project layers from clusters/${ENV}/projects/ ===" >&2
python3 -c '
import os, yaml, subprocess, sys

env = sys.argv[1]
render_file = sys.argv[2]
proj_dir = f"clusters/{env}/projects"

if os.path.isdir(proj_dir):
    for pf in sorted(os.listdir(proj_dir)):
        if pf.endswith((".yaml", ".yml")) and pf != "kustomization.yaml":
            filepath = os.path.join(proj_dir, pf)
            with open(filepath, "r") as f:
                docs = [d for d in yaml.safe_load_all(f) if d]
            for doc in docs:
                name = doc["metadata"]["name"]
                ns = doc["metadata"].get("namespace", "flux-system")
                path = doc["spec"]["path"].replace("<env>", env)
                cmd = ["flux", "build", "kustomization", name, "--namespace", ns, "--path", path, "--kustomization-file", filepath, "--dry-run"]
                res = subprocess.run(cmd, capture_output=True, text=True)
                if res.returncode != 0:
                    sys.stderr.write(f"Error building project layer {name}: {res.stderr}\n")
                    sys.exit(1)
                with open(render_file, "a") as rf:
                    rf.write(res.stdout)
                    rf.write("\n---\n")
' "${ENV}" "${RENDER_FILE}"

# Schema validation with kubeconform if present
if command -v kubeconform >/dev/null 2>&1; then
  echo "=== Running kubeconform schema validation ===" >&2
  kubeconform -strict -kubernetes-version 1.35.0 \
    -schema-location default \
    -schema-location 'https://raw.githubusercontent.com/datreeio/CRDs-catalog/main/{{ .ResourceKind }}_{{ .ResourceAPIVersion }}.json' \
    "${RENDER_FILE}" >&2
else
  echo "Notice: kubeconform not installed locally, skipping kubeconform step." >&2
fi

echo "=== Running render-invariants check ===" >&2
python3 "${SCRIPT_DIR}/render-invariants.py" --environment "${ENV}" --rendered "${RENDER_FILE}" >&2

echo "${RENDER_FILE}"
