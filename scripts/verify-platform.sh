#!/usr/bin/env bash
# ==============================================================================
# verify-platform.sh
# Master End-to-End Verification Suite for Personal Project Platform.
# Executes all scenarios defined in specs/001-project-platform/quickstart.md
# ==============================================================================

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

FAILED_TESTS=()
PASSED_TESTS=()

run_suite() {
  local name="$1"
  local cmd="$2"

  echo ""
  echo "================================================================================"
  echo ">>> [SUITE] $name"
  echo "================================================================================"

  if eval "$cmd"; then
    PASSED_TESTS+=("$name")
    echo ">>> RESULT: $name -> PASSED"
  else
    FAILED_TESTS+=("$name")
    echo ">>> RESULT: $name -> FAILED" >&2
  fi
}

echo "################################################################################"
echo "Starting Master Platform Verification Suite (Specs: 001-project-platform)"
echo "Target Date: $(date -u)"
echo "Repository:  $REPO_ROOT"
echo "################################################################################"

# Scenario 1: Capacity Discovery & Deterministic 1/n Budget
run_suite "Scenario 1: Capacity Discovery & 1/n Resource Budgeting" \
  "python3 -m unittest discover '$REPO_ROOT/tests/preflight' && \
   python3 '$REPO_ROOT/scripts/platform-preflight.py' --environment test-env \
     --candidate '$REPO_ROOT/tests/preflight/fixtures/valid_candidate.yaml' \
     --baseline '$REPO_ROOT/tests/preflight/fixtures/valid_baseline.yaml' \
     --capacity '$REPO_ROOT/tests/preflight/fixtures/valid_capacity.yaml'"

# Scenario 2 & 3: Workload Sandboxing & NetworkPolicy Isolation
run_suite "Scenarios 2 & 3: Restricted PSA Sandboxing & Network Isolation" \
  "bash '$REPO_ROOT/scripts/verify-isolation.sh'"

# Scenario 4: Authentication Gateway & ForwardAuth Protocol
run_suite "Scenario 4: Go Authentication Gateway Contracts & Header Sanitization" \
  "if command -v podman >/dev/null 2>&1; then \
     podman run --rm -v '$REPO_ROOT/gateway:/app:Z' -w /app docker.io/library/golang:1.22-alpine \
       go test -v -run 'TestForwardAuth|TestHeader|TestAuthenticated' ./tests; \
   elif command -v go >/dev/null 2>&1; then \
     (cd '$REPO_ROOT/gateway' && go test -v -run 'TestForwardAuth|TestHeader|TestAuthenticated' ./tests); \
   fi"

# Scenario 5: Synchronous Online Session Revocation & CSRF
run_suite "Scenario 5: Instant Cross-Project Session Revocation & CSRF Protection" \
  "bash '$REPO_ROOT/scripts/verify-revocation.sh'"

# Scenario 6: Database Persistence & Cross-Database Isolation
run_suite "Scenario 6a: PostgreSQL Data Persistence & Catalog Isolation" \
  "bash '$REPO_ROOT/scripts/verify-persistence.sh'"

# Scenario 6b: Disaster Recovery Backup & Restore Capability (FR-018, SC-008)
run_suite "Scenario 6b: Disaster Recovery Backup & Automated Restore" \
  "bash '$REPO_ROOT/scripts/verify-backup.sh'"

# Declarative Reconciliation & Layer Validation
run_suite "Cross-Cutting: Declarative GitOps Layer Validation" \
  "bash '$REPO_ROOT/scripts/render-validate.sh' local-k3s >/dev/null && \
   bash '$REPO_ROOT/scripts/render-validate.sh' vps-k3s >/dev/null"

echo ""
echo "================================================================================"
echo "Master Platform Verification Summary"
echo "================================================================================"
echo "Passed Suites (${#PASSED_TESTS[@]}):"
for s in "${PASSED_TESTS[@]}"; do
  echo "  ✓ $s"
done

if [ ${#FAILED_TESTS[@]} -gt 0 ]; then
  echo ""
  echo "Failed Suites (${#FAILED_TESTS[@]}):" >&2
  for f in "${FAILED_TESTS[@]}"; do
    echo "  ✗ $f" >&2
  done
  echo "================================================================================"
  echo "OVERALL STATUS: FAILED" >&2
  exit 1
fi

echo ""
echo "================================================================================"
echo "OVERALL STATUS: ALL VERIFICATION SCENARIOS PASSED (100% SUCCESS CRITERIA MET)"
echo "================================================================================"
exit 0
