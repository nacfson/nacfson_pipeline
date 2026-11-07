# Implementation Plan: Project Log Contract v3 and Enforcement

**Branch**: `011-project-log-contract` | **Date**: 2026-11-04 | **Spec**: [spec.md](spec.md)

**Input**: Feature specification from `specs/011-project-log-contract/spec.md`

## Summary

Hosted projects print one JSON line per event. Each line carries:

- the service's identity: `service.name`, and `service.version` = the release tag;
- what happened: `level`, `msg`, `exception.*`, `trace_id`, `http.*`, `event.name`.

Fluent Bit stays the only collector. Its pipeline is rebuilt so that it:

1. joins split lines and groups crashes;
2. parses JSON into fields;
3. strips project-supplied reserved keys and adds trusted OpenTelemetry `k8s.*`, `log.iostream`, and `observed_time` fields;
4. chooses the program's time as the main time;
5. redacts secrets before storage;
6. routes non-conforming lines to a quarantine tenant in VictoriaLogs. Nothing is dropped.

Three gates enforce the contract:

- **Preflight** checks manifests: per-container declarations, no log files, release-version labels, and `v0.0.0` kept off `vps-k3s`.
- A stdlib-only Go validator, shipped as the **`logcheck` CLI**, checks each release's real output in project CI.
- The **image-pin helper** refuses images without a version label or a passing `log-contract` check.

The two hosted projects migrate in their own repositories. Runtime drift reporting is delivered by error-pipeline workstream 1, whose design is updated here.

## Technical Context

**Language/Version**:

- Go 1.22, standard library only: `services/error-pipeline`.
- Python 3: preflight, tests, image-pin helper.
- Lua: LuaJIT, embedded in Fluent Bit 4.2.8.
- Fluent Bit classic config.

**Primary Dependencies**: Fluent Bit `4.2.8`, VictoriaLogs `v1.53.0` (both already pinned), PyYAML, `skopeo`, `gh`, `luajit` (CI only).

**Storage**: VictoriaLogs single node, one PVC (10 Gi, 30 d, 8 GiB cap). Tenant `0:0` is the main area and `0:1` is the quarantine area.

**Testing**:

- `go test` (validator, redaction vectors);
- `python3 -m unittest` (`tests/observability`, `tests/preflight`, `tests/skills`);
- `luajit` (Lua stage);
- `render-validate.sh` for both clusters;
- a local Docker end-to-end and load test (quickstart §3–4).

**Target Platform**:

- `vps-k3s`: Oracle ARM64, single-node k3s, CPU over-committed.
- `local-k3s`: amd64.

Images are multi-arch.

**Project Type**: platform infrastructure, made of:

- GitOps manifests;
- a collector configuration;
- a Go library and CLI;
- Python validation tooling.

**Performance Goals**: 100 lines/s sustained and 1,000 lines/s for 10 s through the collector, with no loss (quickstart §4).

**Constraints**:

- The collector stays within `50m` CPU / `64Mi` memory (SC-009).
- No new pods.
- No new `hostPath`.
- No Kubernetes API access for the collector.
- No secrets anywhere.
- The handbook is updated in the same change (AGENTS.md rule 8).

**Scale/Scope**:

- 3 hosted projects and 5 containers today (`proj-pn`, `proj-health`, `proj-blog`), plus the error pipeline as a downstream consumer.
- Cross-repository: `Project_PN`, `nacfson_health`, and `nacfson_blog` migrate in their own PRs.

## Constitution Check

*GATE: Must pass before Phase 0 research. Re-checked after Phase 1 design.*

| Principle / rule | Assessment | Result |
|---|---|---|
| I. GitOps, pinned images | Every config change goes through Git and Flux. The `logcheck` image is built by this repository, so it is referenced by digest. Project images stay pinned by digest, with the version label added. Third-party images are unchanged. | Pass |
| II. Isolation, Restricted PSS | Projects gain only pod-template annotations and a label. There is no Downward API, no new privileges, and no API access. | Pass |
| III. Identity | Not affected. | N/A |
| IV. 1/n budget | Project pods are unchanged, and no new platform pod is added. The collector keeps its declared limits, and the load test verifies them. Clarification 2.2.1 still applies: the limits are not added to the reservation by hand. | Pass, needs a measurement (research R12) |
| V. Zero plaintext secrets | Strengthened: secrets printed by projects are redacted before storage. No credential is added. `gh` runs on the operator's workstation, not in a cluster. | Pass |
| VI. Portability | The same pipeline runs in both clusters. Tenants are a standard VictoriaLogs header. Untagged builds (`v0.0.0-<commit>`) are supported everywhere once checked. | Pass |
| VII. Probe roles | Not affected. | N/A |
| Clarification 2.2.1 (one read-only `/var/log/pods` mount) | Still exactly one mount, read-only. The collector writes only to its existing state PVC. | Pass |
| AGENTS rule 1 (stdlib-only Go) | `services/error-pipeline` (`internal/conform`, `cmd/logcheck`) follows the same rule as the gateway and the proxies. | Pass |
| AGENTS rule 8 (handbook sync) | `docs/platform-architecture.html` is updated in every PR of this feature that touches the listed paths. | Pass (tracked as a task) |
| AGENTS rule 9 (SSH helpers) | The post-deploy checks use `open-observe.sh`, which needs the user's explicit allowance. | Pass |

**Post-design re-check (after Phase 1)**: no new violations. The design adds two new surfaces:

- the `internal/conform` package and `cmd/logcheck` CLI inside `services/error-pipeline`;
- the Lua stage in the collector.

Neither adds privileges, network paths, or pods. Both are justified under Complexity Tracking.

## Project Structure

### Documentation (this feature)

```text
specs/011-project-log-contract/
├── plan.md                  # this file
├── spec.md
├── research.md              # Phase 0
├── data-model.md            # Phase 1
├── quickstart.md            # Phase 1
├── contracts/
│   ├── log-line-v3.md       # what projects print
│   ├── collector-pipeline.md# Fluent Bit stages, stored record, readers
│   └── enforcement-gates.md # preflight rules, logcheck CLI, image-pin checks
├── checklists/requirements.md
└── tasks.md                 # Phase 2 (/speckit-tasks)
```

### Source Code (repository root)

```text
contracts/
└── CONTRACT_LOG_SCHEMA.md            # rewritten to v3 (from contracts/log-line-v3.md)

services/error-pipeline/              # existing Go module (stdlib only, go 1.22)
├── internal/
│   ├── conform/                      # NEW: Check, Classify against v3 contract
│   │   ├── conform.go
│   │   ├── conform_test.go
│   │   └── contract_test.go          # Markdown contract drift test
│   ├── classify/                     # updated: v3 crash signatures, no Node.js
│   └── redact/                       # updated: kind suffixes [REDACTED:<kind>]
│       └── testdata/vectors.json     # shared Go/Lua parity vectors
├── cmd/logcheck/main.go              # NEW: CLI for project CI
└── Dockerfile.logcheck               # NEW: multi-stage non-root logcheck image

deploy/platform/observability/
├── fluent-bit-configmap.yaml         # rebuilt pipeline + log_contract.lua + legacy-containers.txt
└── fluent-bit-daemonset.yaml         # mounts unchanged; Lua/list files come from the ConfigMap

deploy/projects/{pn,health}/workloads/*-deployment.yaml
                                      # + logs.nacfson.cloud/* annotations, app.kubernetes.io/version label

scripts/
├── platform-preflight.py             # + check_log_contract (contracts/enforcement-gates.md, Gate 1)
├── open-victorialogs-ui.sh           # verify step uses k8s.namespace.name
└── open-observe.sh                   # example query text

.agents/skills/update-project-images/
├── SKILL.md                          # documents the version/conformance checks
└── scripts/update_project_images.py  # Gate 3

.github/workflows/
├── build-images.yaml                 # + logcheck image
└── lint.yaml                         # + logcontract go test, luajit tests

tests/
├── observability/
│   ├── test_observability_manifests.py   # updated field names, filter order, no-drop rules
│   ├── test_log_contract_schema.py       # NEW: contract ↔ Lua lists
│   ├── lua/run_log_contract_tests.lua    # NEW: Lua stage + redaction vectors
│   ├── fixtures/cri/*.log                # NEW: end-to-end fixtures
│   └── test_open_victorialogs_ui.py      # updated
├── preflight/
│   ├── test_log_contract.py              # NEW
│   └── fixtures/log_contract/*           # NEW
└── skills/test_update_project_images.py  # + version/conformance cases

docs/
├── platform-architecture.html        # handbook sync (rule 8)
├── operations-runbook.md             # §15: v3 fields, quarantine tenant, queries
└── superpowers/
    ├── specs/2026-10-29-error-pipeline-design.md   # v3 names, tenants, marker alert, detector RBAC
    └── plans/2026-10-29-ws0-*.md, ws1-*.md          # rewritten to v3

DEPENDENCIES.md, INDEX.md, contracts/README.md, ARCHITECTURE.md   # references
```

Changes in other repositories, each with its own PR:

```text
Project_PN/      backend logger (JSON, service.*, exception root type, traceparent),
                 nginx JSON access log + traceparent, release workflow (version ldflags,
                 OCI labels, log-contract job using ghcr.io/nacfson/logcheck@sha256)
nacfson_health/  same pattern
```

**Structure Decision**:

- **Platform changes** stay in their existing homes: `deploy/platform/observability/`, `scripts/`, `tests/`, and the image-pin skill.
- **The validator** is built inside the existing `services/error-pipeline` module under `internal/conform`, with the `cmd/logcheck` CLI. It reuses `internal/classify` and `internal/redact`.
- **The project migrations** are delivered in their own repositories and pinned back through the image-pin flow.

## Delivery Order (PR slices)

*Updated 2026-11-05: operator decision D22, with reconciliation R-5/R-6 (see [deferred-decisions.md](deferred-decisions.md)).*

| # | PR | Contents | Changes the cluster? | Depends on |
|---|---|---|---|---|
| 1 | Contract v3 and checker | `CONTRACT_LOG_SCHEMA.md` v3 §2–§6 (R-9), `services/error-pipeline/internal/conform` + `cmd/logcheck` (R-1), drift tests, CI job, `logcheck` image build | No | — |
| 2 | Detector reads v2 and v3 | The detector accepts both the v2 and v3 field names. Built on merge, then pinned by digest **before** PR 3 (R-5) | Detector only | 1 |
| 3 | Collector v3 | Fluent Bit pipeline, Lua stage (redaction, 8 KiB stack-trace cap), legacy list (5 containers, R-7), quarantine tenant, contract §1, updated readers, scripts, tests, runbook, handbook | **Yes, live logs** | 2 deployed |
| 4 | Manifest gate | Preflight `check_log_contract`; the version-label PR check (Gate 1b, FR-024a, registry read in CI); annotations and version labels on the PN, health, and blog workloads (`v2/legacy`); fixtures | No (CI only) | 3 |
| 5 | Image-pin gate | Helper and skill changes: version label, untagged commit check, conformance by revision commit; `image-pin.py` diff check allows the version label (R-8); tests | No (CI only) | 1 |
| 6 | Violation reporting | FR-025 in the detector: daily digest, redaction-marker alerts, and the immediate quarantine alert (first line at once, then at most one per container per hour). No Kubernetes access for the detector (D19). Error-pipeline docs to v3 (R-6) | Detector | 3 |
| 7+ | Project migrations | Per project repository (PN, health, blog): logger, nginx, and a build workflow that runs `log-contract` on every image build. Then a pin PR that moves the annotation to `v3/*`, removes the container from `legacy-containers.txt`, and sets the row to `done`. After every row is `done`, a final PR removes the detector's v2 read (FR-028) | Per project | 4, 5 |

PR 2 renames fields in the main area. The old field names stay in the data written before the switch, which ages out after 30 days. The runbook notes this.

## Complexity Tracking

| Addition | Why Needed | Simpler Alternative Rejected Because |
|---|---|---|
| Conformance package `internal/conform` and `cmd/logcheck` in `services/error-pipeline` | One validator is needed by project CI, the runtime detector, and repository tests (FR-023) | Separate implementations would drift. Reuses existing `error-pipeline` module instead of creating a new module (R-1). |
| Lua stage in the collector | Missing-key routing, timestamp choice, and value redaction can't be expressed with the built-in filters (research R4) | `modify`/`grep`/`rewrite_tag` alone can't test for missing keys or replace the timestamp. A WASM filter needs a toolchain. |
| Second VictoriaLogs tenant | The quarantine area must be outside default searches without dropping lines (FR-018a) | A field marker still pollutes default searches. A second store adds a pod. A file isn't visible to readers. |
