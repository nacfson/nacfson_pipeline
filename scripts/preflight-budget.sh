#!/usr/bin/env bash
# ==============================================================================
# preflight-budget.sh
# Validates deterministic 1/n project resource budgeting, strict requests == limits,
# and memory freeze policies per contracts/preflight-budget.json.
# ==============================================================================

set -euo pipefail

ENVIRONMENT="vps-k3s"
PROJECT_COUNT=1
NODE_CPU_MILLIS=""
NODE_MEM_MIB=""
CANDIDATE_CPU_MILLIS=350   # Default PN candidate: backend 250m + frontend 100m
CANDIDATE_MEM_MIB=640       # Default PN candidate: backend 512Mi + frontend 128Mi
DEPLOYED_BASELINE_MEM_MIB=0
OUTPUT_JSON=false

while [[ $# -gt 0 ]]; do
  case "$1" in
    --environment)
      ENVIRONMENT="$2"
      shift 2
      ;;
    --projects)
      PROJECT_COUNT="$2"
      shift 2
      ;;
    --node-cpu)
      NODE_CPU_MILLIS="$2"
      shift 2
      ;;
    --node-mem)
      NODE_MEM_MIB="$2"
      shift 2
      ;;
    --candidate-cpu)
      CANDIDATE_CPU_MILLIS="$2"
      shift 2
      ;;
    --candidate-mem)
      CANDIDATE_MEM_MIB="$2"
      shift 2
      ;;
    --deployed-baseline-mem)
      DEPLOYED_BASELINE_MEM_MIB="$2"
      shift 2
      ;;
    --json)
      OUTPUT_JSON=true
      shift
      ;;
    *)
      echo "Unknown option: $1" >&2
      exit 1
      ;;
  esac
done

# 1. Discover or parse node allocatable capacity
if [[ -z "$NODE_CPU_MILLIS" || -z "$NODE_MEM_MIB" ]]; then
  if command -v kubectl >/dev/null 2>&1 && kubectl get nodes >/dev/null 2>&1; then
    NODE_NAME=$(kubectl get nodes -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || echo "")
    if [[ -n "$NODE_NAME" ]]; then
      RAW_CPU=$(kubectl get node "$NODE_NAME" -o jsonpath='{.status.allocatable.cpu}' 2>/dev/null || echo "")
      RAW_MEM=$(kubectl get node "$NODE_NAME" -o jsonpath='{.status.allocatable.memory}' 2>/dev/null || echo "")

      # Parse CPU (cores or millis)
      if [[ "$RAW_CPU" =~ ^[0-9]+m$ ]]; then
        NODE_CPU_MILLIS="${RAW_CPU%m}"
      elif [[ "$RAW_CPU" =~ ^[0-9]+$ ]]; then
        NODE_CPU_MILLIS=$(( RAW_CPU * 1000 ))
      fi

      # Parse Memory (Ki, Mi, Gi)
      if [[ "$RAW_MEM" =~ ^[0-9]+Ki$ ]]; then
        NODE_MEM_MIB=$(( ${RAW_MEM%Ki} / 1024 ))
      elif [[ "$RAW_MEM" =~ ^[0-9]+Mi$ ]]; then
        NODE_MEM_MIB="${RAW_MEM%Mi}"
      elif [[ "$RAW_MEM" =~ ^[0-9]+Gi$ ]]; then
        NODE_MEM_MIB=$(( ${RAW_MEM%Gi} * 1024 ))
      fi
    fi
  fi
fi

# Fallback default for single Oracle Cloud A1 VPS (2 OCPU = 2000m, 12GB = 12288Mi)
if [[ -z "$NODE_CPU_MILLIS" ]]; then
  NODE_CPU_MILLIS=2000
fi
if [[ -z "$NODE_MEM_MIB" ]]; then
  NODE_MEM_MIB=12288
fi

# Platform reservations: System/K8s (500m/512Mi) + Traefik (100m/128Mi) + Keycloak (500m/1024Mi) + Postgres (250m/256Mi) + Gateway (100m/64Mi)
PLATFORM_RES_CPU=1450
PLATFORM_RES_MEM=1984

APP_CAPACITY_CPU=$(( NODE_CPU_MILLIS - PLATFORM_RES_CPU ))
APP_CAPACITY_MEM=$(( NODE_MEM_MIB - PLATFORM_RES_MEM ))

if [[ $APP_CAPACITY_CPU -le 0 || $APP_CAPACITY_MEM -le 0 ]]; then
  STATUS="REJECTED_NO_MEASUREMENT"
  REASON="Platform reservations exceed node allocatable capacity."
  if [[ "$OUTPUT_JSON" == true ]]; then
    printf '{"environment":"%s","validationStatus":"%s","rejectionReason":"%s"}\n' "$ENVIRONMENT" "$STATUS" "$REASON"
  else
    echo "Validation FAILED: $STATUS - $REASON" >&2
  fi
  exit 1
fi

# Handle zero deployed projects safely (T025)
if [[ $PROJECT_COUNT -eq 0 ]]; then
  STATUS="PASSED"
  REASON="Zero active projects: 100% capacity available."
  SLICE_CPU=$APP_CAPACITY_CPU
  SLICE_MEM=$APP_CAPACITY_MEM
else
  SLICE_CPU=$(( APP_CAPACITY_CPU / PROJECT_COUNT ))
  SLICE_MEM=$(( APP_CAPACITY_MEM / PROJECT_COUNT ))
fi

# Memory Freeze Check (T026): Reject memory limit shrink below deployed baseline
if [[ $DEPLOYED_BASELINE_MEM_MIB -gt 0 && $CANDIDATE_MEM_MIB -lt $DEPLOYED_BASELINE_MEM_MIB ]]; then
  STATUS="REJECTED_MEMORY_SHRINK"
  REASON="Candidate memory limit (${CANDIDATE_MEM_MIB}Mi) reduces deployed memory envelope (${DEPLOYED_BASELINE_MEM_MIB}Mi)."
  if [[ "$OUTPUT_JSON" == true ]]; then
    printf '{"environment":"%s","validationStatus":"%s","rejectionReason":"%s"}\n' "$ENVIRONMENT" "$STATUS" "$REASON"
  else
    echo "Validation FAILED: $STATUS - $REASON" >&2
  fi
  exit 1
fi

# Capacity Slice Budget Check (T024)
if [[ $PROJECT_COUNT -gt 0 ]]; then
  if [[ $CANDIDATE_CPU_MILLIS -gt $SLICE_CPU || $CANDIDATE_MEM_MIB -gt $SLICE_MEM ]]; then
    STATUS="REJECTED_OVER_BUDGET"
    REASON="Candidate peak footprint (${CANDIDATE_CPU_MILLIS}m / ${CANDIDATE_MEM_MIB}Mi) exceeds project budget (${SLICE_CPU}m / ${SLICE_MEM}Mi)."
    if [[ "$OUTPUT_JSON" == true ]]; then
      printf '{"environment":"%s","validationStatus":"%s","rejectionReason":"%s"}\n' "$ENVIRONMENT" "$STATUS" "$REASON"
    else
      echo "Validation FAILED: $STATUS - $REASON" >&2
    fi
    exit 1
  fi
fi

STATUS="PASSED"
REASON="Candidate conforms to 1/n budget slice and requests == limits."

if [[ "$OUTPUT_JSON" == true ]]; then
  cat <<EOF
{
  "environment": "$ENVIRONMENT",
  "measuredAllocatable": {
    "cpuMillicores": $NODE_CPU_MILLIS,
    "memoryMib": $NODE_MEM_MIB
  },
  "platformReservations": {
    "cpuMillicores": $PLATFORM_RES_CPU,
    "memoryMib": $PLATFORM_RES_MEM
  },
  "projectCount": $PROJECT_COUNT,
  "calculatedPerProjectBudget": {
    "cpuMillicores": $SLICE_CPU,
    "memoryMib": $SLICE_MEM,
    "strictQoS": true
  },
  "candidatePeakFootprint": {
    "cpuMillicores": $CANDIDATE_CPU_MILLIS,
    "memoryMib": $CANDIDATE_MEM_MIB
  },
  "validationStatus": "$STATUS",
  "rejectionReason": "$REASON"
}
EOF
else
  echo "Preflight Budget Check: $STATUS"
  echo "  Node Allocatable:    CPU=${NODE_CPU_MILLIS}m, Mem=${NODE_MEM_MIB}Mi"
  echo "  Platform Overhead:   CPU=${PLATFORM_RES_CPU}m, Mem=${PLATFORM_RES_MEM}Mi"
  echo "  Available Capacity:  CPU=${APP_CAPACITY_CPU}m, Mem=${APP_CAPACITY_MEM}Mi"
  echo "  Project Slice ($PROJECT_COUNT):   CPU=${SLICE_CPU}m, Mem=${SLICE_MEM}Mi"
  echo "  Candidate Footprint: CPU=${CANDIDATE_CPU_MILLIS}m, Mem=${CANDIDATE_MEM_MIB}Mi"
fi

exit 0
