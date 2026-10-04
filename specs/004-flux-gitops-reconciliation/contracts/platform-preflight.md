# Contract: `platform-preflight` Required Check

**Feature**: [spec.md](../spec.md) FR-010, FR-013, SC-006, SC-008 | **Decisions**: [research.md](../research.md) R9, R11
**Upstream requirement**: [SPEC.md](../../../SPEC.md) DEPLOY-04, RESOURCE-02, ISOLATE-01; constitution v1.4.1 Principle IV and the Deployment Quality Gates.
**Supersedes**: `scripts/preflight-budget.sh`. The output stays compatible with [001 preflight-budget.json](../../001-project-platform/contracts/preflight-budget.json), extended with new statuses.

## 1. Where it runs

The GitHub Actions workflow `.github/workflows/gitops.yaml` runs on every pull request into `main` and on every push to `main`, as a matrix over `env ∈ {local-k3s, vps-k3s}`. Job names (required by branch protection):

- `render-validate (<env>)`
- `platform-preflight (<env>)`

## 2. Inputs

| Input | Source |
|---|---|
| Candidate render | For every Flux Kustomization in `clusters/<env>/layers.yaml` and `clusters/<env>/projects/*.yaml`: `flux build kustomization <layer> --path <spec.path> --kustomization-file <file that declares it> --dry-run` (Flux CLI 2.9.5), concatenated. Also `kustomize build clusters/<env>/flux-system` (the Flux controllers, for the reservation). |
| Baseline render | The same procedure on `git merge-base origin/main HEAD`. Empty when the baseline has no `clusters/<env>/`. |
| Capacity Measurement | `clusters/<env>/capacity.yaml` (schema in the [data model](../data-model.md#capacity-measurement-preflight-input-new)) |
| Layer classification | Platform layers = every layer without `serviceAccountName`. Project layers = layers with `serviceAccountName`; the project identifier is the layer's namespace. |

Invocation: `python scripts/platform-preflight.py --environment <env> --candidate <file> --baseline <file> --capacity clusters/<env>/capacity.yaml [--json]`

## 3. Rules (evaluated in order; the first failure decides the status)

| # | Rule | Status on failure |
|---|---|---|
| 1 | `capacity.yaml` exists, parses, `environment` matches, and all values are > 0 | `REJECTED_NO_MEASUREMENT` |
| 2 | Isolation in project namespaces: no Service of type NodePort or LoadBalancer; no `hostPath` volume; no `hostNetwork`, `hostPID` or `hostIPC`; no `privileged: true`; no `allowPrivilegeEscalation: true`; no `automountServiceAccountToken` other than `false` on pods; capabilities `add` only `NET_BIND_SERVICE` | `REJECTED_ISOLATION` |
| 3 | Every container and init container in **project** layers has CPU and memory requests **equal to** limits | `REJECTED_QOS` |
| 4 | Every container in **all** rendered layers (including Flux controllers) declares CPU and memory requests and limits | `REJECTED_UNBOUNDED` |
| 5 | Memory freeze: for each project container present in the baseline, the candidate memory limit is ≥ the baseline limit | `REJECTED_MEMORY_SHRINK` |
| 6 | Budget: per project, peak footprint ≤ slice (formula §4); zero projects always pass this rule | `REJECTED_OVER_BUDGET` |

If every rule passes, the status is `PASSED`. On rejection the exit code is 1; on success it is 0; on a usage or parse error it is 2. Exit code 2 also fails the job (fail-closed).

## 4. Calculation

```text
platformReservation.cpu = systemReserved.cpu + Σ cpu requests of all containers in platform layers and flux-system
platformReservation.mem = systemReserved.mem + Σ memory LIMITS of all containers in platform layers and flux-system
appCapacity             = allocatable − platformReservation        (≤ 0 ⇒ REJECTED_NO_MEASUREMENT)
slice                   = appCapacity / projectCount               (projectCount = 0 ⇒ slice = appCapacity)

projectPeak = Σ_Deployments  perPod × (replicas + maxSurge)        # maxSurge default 25% rounded up; Recreate ⇒ 0
            + Σ_StatefulSets perPod × replicas
            + Σ_Jobs         perPod × parallelism                  # default 1
            + Σ_CronJobs     perPod × (concurrencyPolicy == Allow ? 2 : 1)
perPod      = Σ container requests + max(init container requests)  # init containers run before app containers
```

Completed pods are not counted. Platform pods count toward the reservation, not toward any project's slice.

## 5. Output

Human-readable text by default. With `--json`, a single object that is a superset of the 001 schema:

```json
{
  "environment": "vps-k3s",
  "measuredAllocatable": { "cpuMillicores": 0, "memoryMib": 0 },
  "platformReservations": { "cpuMillicores": 0, "memoryMib": 0 },
  "projectCount": 1,
  "calculatedPerProjectBudget": { "cpuMillicores": 0, "memoryMib": 0, "strictQoS": true },
  "candidatePeakFootprint": { "cpuMillicores": 0, "memoryMib": 0 },
  "projects": [ { "id": "proj-pn", "peak": { "cpuMillicores": 0, "memoryMib": 0 } } ],
  "validationStatus": "PASSED",
  "rejectionReason": "",
  "violations": [ { "rule": 2, "object": "proj-pn/Service/x", "detail": "type NodePort" } ]
}
```

`validationStatus` enum: `PASSED`, `REJECTED_NO_MEASUREMENT`, `REJECTED_ISOLATION`, `REJECTED_QOS`, `REJECTED_UNBOUNDED`, `REJECTED_MEMORY_SHRINK`, `REJECTED_OVER_BUDGET`. `candidatePeakFootprint` is the largest project peak (kept for 001 compatibility). Output MUST NOT contain secret values; the script reads only resource, security and spec fields.

## 6. Required fixtures (`tests/preflight/`)

| Fixture | Expected |
|---|---|
| Current repository render (vps-k3s) | `PASSED` |
| Zero project layers | `PASSED` (no division by zero) |
| Missing or zero `capacity.yaml` | `REJECTED_NO_MEASUREMENT`, exit 1 |
| Project Service `type: NodePort` | `REJECTED_ISOLATION` |
| Project pod with `hostPath` | `REJECTED_ISOLATION` |
| Project container with requests ≠ limits | `REJECTED_QOS` |
| Platform container without limits | `REJECTED_UNBOUNDED` |
| Project memory limit lowered vs baseline | `REJECTED_MEMORY_SHRINK` |
| Project replicas raised beyond slice | `REJECTED_OVER_BUDGET` |
| Malformed render | exit 2 |

## 7. Branch protection (operator configuration, documented in the runbook)

Protected branch `main`:
- **Required status checks:** `render-validate (local-k3s)`, `render-validate (vps-k3s)`, `platform-preflight (local-k3s)`, `platform-preflight (vps-k3s)`, `Scan for Leaked Secrets (Gitleaks)`, `lint`, `bootstrap-syntax`.
- **Settings:** require branches to be up to date, require 1 approving review (the repository owner may self-approve through admin bypass only as documented break-glass), no force pushes, no deletions.
