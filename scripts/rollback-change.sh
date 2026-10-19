#!/usr/bin/env bash
# rollback-change.sh — Fast rollback helper & review circuit-breaker incident recorder.
#
# Usage:
#   ./scripts/rollback-change.sh --dry-run <commit-ref>
#   ./scripts/rollback-change.sh <commit-ref>

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DRY_RUN=0
TARGET_REF=""

usage() {
  cat <<'EOF'
Usage: ./scripts/rollback-change.sh [--dry-run] <commit-ref>

Options:
  --dry-run   Validate the target commit and print the revert + circuit-breaker
              commands without modifying the working tree or repository state.
  -h, --help  Show this help message.
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --dry-run)
      DRY_RUN=1
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    -*)
      echo "ERROR: Unknown option: $1" >&2
      usage >&2
      exit 2
      ;;
    *)
      if [[ -z "$TARGET_REF" ]]; then
        TARGET_REF="$1"
      else
        echo "ERROR: Unexpected extra argument: $1" >&2
        usage >&2
        exit 2
      fi
      shift
      ;;
  esac
done

if [[ -z "$TARGET_REF" ]]; then
  echo "ERROR: Missing <commit-ref> to roll back." >&2
  usage >&2
  exit 2
fi

if ! RESOLVED_SHA="$(git -C "$REPO_ROOT" rev-parse --verify "${TARGET_REF}^{commit}" 2>/dev/null)"; then
  echo "ERROR: Cannot resolve commit reference '${TARGET_REF}'." >&2
  exit 1
fi

SHORT_SHA="$(git -C "$REPO_ROOT" rev-parse --short "$RESOLVED_SHA")"

if [[ "$DRY_RUN" -eq 1 ]]; then
  echo "[DRY-RUN] Validated rollback target: ${SHORT_SHA} (${RESOLVED_SHA})"
  echo "[DRY-RUN] Would execute: SKIP_REVIEW_GATE=1 git revert --no-edit ${RESOLVED_SHA}"
  echo "[DRY-RUN] Would record circuit-breaker incident: python3 scripts/review-cycle.py circuit-breaker --record-incident ${SHORT_SHA}"
  echo "[DRY-RUN] Would run verification: python3 -m unittest discover tests/preflight"
  exit 0
fi

echo "[ROLLBACK] Reverting commit ${SHORT_SHA} (${RESOLVED_SHA})..."
SKIP_REVIEW_GATE=1 git -C "$REPO_ROOT" revert --no-edit "$RESOLVED_SHA"

echo "[ROLLBACK] Recording incident in review circuit breaker..."
python3 "$REPO_ROOT/scripts/review-cycle.py" circuit-breaker --record-incident "$SHORT_SHA"

echo "[ROLLBACK] Running preflight unit tests to verify post-revert state..."
python3 -m unittest discover -s "$REPO_ROOT/tests/preflight"

echo "[ROLLBACK] Rollback complete for ${SHORT_SHA}."
