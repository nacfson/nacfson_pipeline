# Project Log Viewer & Log History — Design

Date: 2026-10-22 · Size: project (new Flux layer + preflight change + constitution clarifying note) · Status: approved 2026-10-22, amended 2026-10-24

Marking: **(operator)** = decided by the operator. **(my call)** = my proposal, can be changed.

## Intent
The operator wants to read container logs in a GUI (live logs of any pod; **stored history for project workloads only**) so past project events (deleted pods, old rollouts, "what happened at 3am") can be searched. The VPS is on the public internet; Tailscale is not in use. Memory is shared with projects through the 1/n budget, so the stack must be light.

## Goals
- Live logs in a GUI, including the previous crashed container.
- Searchable history of **project** container logs only (`proj-*`), kept 30 days, cap 8 GiB, on a 10 GiB volume. **(operator, amended 2026-10-24)**
- No public route to any log UI or to the Kubernetes API.
- No plaintext Secrets in Git or pods; configuration lives in ConfigMaps.
- Follow the industry-standard collection pattern (see Industry alignment).
- Each layer replaceable on its own (see Seams).

## Non-goals
- Metrics, traces, alerting, Grafana dashboards.
- Network flows (Hubble UI covers those; Hubble cannot see application logs).
- Access for other people (deferred; see Future).
- Node or OS logs (`k3s.service`, journald).
- History of platform component logs (`platform`, `identity`, `vault`, `kube-system`, `flux-system`, `cert-manager`, `governance`, `observability`); these are live-only via `kubectl logs` or Headlamp. **(operator)**
- Severity ("how") and request/error correlation ("why") filtering; no structured-log convention and no parsing of log content. **(operator)**

## Anti-goals
- A public `IngressRoute` for any log UI.
- Projects writing log files, mounting volumes, or changing their manifests or images for logging.
- A stack so heavy it shrinks project slices noticeably.

## Constraints
- Constitution: zero plaintext secrets, platform-owned TLS, Restricted Pod Security for project namespaces, Flux layer order, `requests == limits`, pinned image tags (no `:latest`).
- `hostPath` rules: the constitution's ban (Principle II and the "Controlled Ingress Bypass Prevention" bullet) covers **project** namespaces and manifests only. A platform namespace is not in violation (Cilium already uses `hostPath` in `kube-system`). No formal amendment is needed; a short clarifying note in the constitution records that the collector's read-only `hostPath` in a platform namespace is intended (PATCH-level wording change). **(operator accepted the note)**
- `scripts/platform-preflight.py` keeps a fixed `PLATFORM_NAMESPACES` set. `observability` must be added to it, with a unit test in `tests/preflight/`; otherwise Rule 2 treats it as a project namespace and rejects the `hostPath`.
- Any change under `deploy/`, `clusters/`, `scripts/platform-preflight.py` or `.specify/memory/constitution.md` must also update `docs/platform-architecture.html` and pass preflight and `render-validate.sh` for both environments.
- Commits, pushes, and pull requests use `git` and `gh` on a feature branch. `gh-mcp` is gone. Never push to `main`. Never merge a feature branch into local `main`.

## Approaches considered
1. **Loki + Alloy + Grafana.** Standard stack, roughly 600–1000 Mi. Grafana's Keycloak login needs a client secret, which clashes with the zero-plaintext rule. Not chosen.
2. **VictoriaLogs + Fluent Bit (chosen).** Roughly 150–400 Mi for the store plus about 20–60 Mi per node for the collector. Built-in search UI, strong compression. VictoriaLogs documents Fluent Bit as a supported sender.
3. OpenSearch/Elasticsearch. 2 GiB or more of memory. Rejected.

Collector choice: Fluent Bit over Alloy: about a quarter of the memory and a plain ConfigMap. Alloy is better only if the team later adopts the full Grafana stack.

**How the collector reads logs.**
- **Chosen: `hostPath` read-only mount of `/var/log/pods`** (node-level DaemonSet agent). This is the Kubernetes-documented and most common pattern; it needs no API token and no application changes.
- **Rejected for now: Kubernetes API log streaming** (Alloy-style). It would let `observability` run under Restricted Pod Security and enforce the `proj-*` limit through RBAC, but it needs a ServiceAccount token, a heavier collector, puts one stream per container on the API server, and loses more lines across restarts. It stays a documented alternative if enforcing the project-only limit by RBAC becomes more important than memory and convention.

## Design

### Data flow
```
project pod prints to stdout/stderr
 → containerd writes /var/log/pods/proj-<name>_<pod>_<uid>/<container>/0.log on the node
 → Fluent Bit DaemonSet (observability) reads proj-* files read-only, adds the standard record
 → VictoriaLogs (observability) stores it on a PVC, with a built-in search UI
 → operator reaches the UI by a private workstation tunnel (no public route)
```
Projects only print to stdout/stderr, which they already do. Only the collector has a `hostPath`; project manifests and images do not change.

### Components (all in the new `observability` namespace, manifests in `deploy/platform/observability/`)
1. **Live logs (already available).** Desktop Headlamp or `kubectl logs` over the existing SSH tunnel with the `operator-readonly` account (`deploy/platform/governance/operator-access.yaml`, `scripts/operator-kubeconfig.sh`, runbook §6 and §13). Docs only.
2. **Collector.** Fluent Bit DaemonSet. `tail` input on `/var/log/pods/proj-*_*/*/*.log`. Names come from the file path, so there is **no Kubernetes API access and no service-account token**. Output goes to VictoriaLogs over HTTP inside the cluster. The tail bookmark is a 1 GiB PVC, not an `emptyDir` and not a second `hostPath`.
3. **Store and UI.** VictoriaLogs single-binary StatefulSet with a PVC on k3s `local-path`. `ClusterIP` only.
4. **Access.** No `IngressRoute`. On this workstation, `scripts/open-observe.sh` SSHes only the VPS API to `127.0.0.1:16443` (local k3s owns `6443`), then port-forwards Hubble and VictoriaLogs. `local-k3s` uses `scripts/open-hubble-ui.sh` and `scripts/open-victorialogs-ui.sh`. Both log scripts have `--verify`.
5. **Isolation.** `observability` has a default-deny `NetworkPolicy`; allow only Fluent Bit → VictoriaLogs write port and operator port-forward.

### Aggregation, naming and filtering **(operator)**
- One Fluent Bit DaemonSet collects project logs and sends all of them to one VictoriaLogs upstream.
- Filters in the UI: **which project** (`project`), **when** (time range), **where** (`node`, `pod`, `container`). "How" and "why" are out of scope.
- Per-project separation is **by name**, not by separate storage: Kubernetes already prefixes each log folder with the namespace (`/var/log/pods/proj-pn_...`), Fluent Bit derives `project=proj-pn` from it, and a new project is collected automatically. No per-project path, volume or configuration is created.

### Standard record (stable contract between collector and store)
Fluent Bit adds these fields to every line: `project` (namespace), `pod`, `container`, `node`, `pod_uid`, `time` (original container timestamp), `message` (printed line). Stream fields are only `project`, `container` and `pod`; `pod_uid` and `node` are ordinary searchable fields, to keep the stream count low. No files or folders are created per container or pod uid. Outputs are separate `[OUTPUT]` blocks in a ConfigMap, so a destination can be added or swapped without changing the record.

### Log schema and GitOps **(operator, 2026-10-24)**

Git is the schema registry. Flux applies the Fluent Bit ConfigMap. There is no schema-registry pod.

- **Envelope**, owned here: [`contracts/CONTRACT_LOG_SCHEMA.md`](../../../contracts/CONTRACT_LOG_SCHEMA.md). A manifest test fails if the ConfigMap field names drift from that file. That is the GitOps gate. VictoriaLogs does not read the contract at runtime.
- **Body**, owned by the producer: `kind:"analytics"` on stdout and `kind:"admin"` on stderr stay in Project PN, next to the code that prints them. This repository names the kind and the owner. It does not copy the field list.
- The collector does not parse `message` in this version, and it does not drop a line for a body-schema mismatch.

Label source (my call): names come from the file path only, not from the Kubernetes API, so Fluent Bit needs no token. Pod labels are not available in this version; adding them later needs a read-only ServiceAccount.

### Retention and size **(operator, amended 2026-10-24: 30 days, 8 GiB cap, 10 GiB volume)**
`-retentionPeriod=30d` and `-retention.maxDiskSpaceUsageBytes=8589934592` (8 GiB) on a 10 GiB PVC. 8 GiB leaves 20% of that volume free. Upstream can still pass the cap between checks, and it never deletes the last two days, so the cap does not prove the volume never fills. The operator accepted that residual. The builder checks free disk on the VPS only, before the PVC is applied. Under 10 GiB free on the VPS: stop. Do not check `local-k3s`, and do not give the two clusters different sizes.

### Security of the collector
- **Why it needs `hostPath`:** logs are files on the node, and a pod cannot see the node's files without a mount. Only the collector gets one.
- **File permission is a separate matter:** the log files are root-only (a non-root `ls /var/log/pods` is denied). The collector runs as root (my call), with these limits: read-only mount of `/var/log/pods` only, no service-account token, `allowPrivilegeEscalation: false`, all capabilities dropped, read-only root filesystem, egress only to VictoriaLogs, pinned image. The builder may test a non-root user with only `DAC_READ_SEARCH` on the VPS and switch to it if it works.
- **A `hostPath` cannot be limited to a glob,** so the mount exposes all pods' log files to the collector; the `proj-*` limit is enforced by its config, not by the mount.
- **Write protection (my call):** no shared password. Kubernetes Secrets are not allowed for backend credentials, and a password would need a new OpenBao delivery path. Instead, a `NetworkPolicy`/Cilium policy allows only pods labeled for Fluent Bit to reach the VictoriaLogs write port; other namespaces cannot. Read access is Kubernetes RBAC through port-forward with the `operator-readonly` token (existing, no new RBAC).
- **Namespace Pod Security (operator, 2026-10-24):** `pod-security.kubernetes.io/enforce: privileged` on `observability`, with `warn` and `audit` at the same level. The operator accepted this without a full understanding of the rule. The explanation is the next section. The builder confirms no cluster-wide policy overrides the label.


### PSA label, explained

The operator did not fully understand this part when accepting it. This section is that explanation.

| Piece | Meaning |
|---|---|
| What a PSA label is | A label on the namespace, not on the pod. Pod Security Admission reads it when a pod is created and either allows the pod or rejects it before kubelet starts it. |
| `enforce: restricted` | Hard reject. Vault and `proj-*` use this (`deploy/platform/vault/namespace.yaml`). It forbids every `hostPath`, even read-only, and it forbids root and any added capability. |
| `enforce: baseline` | Also forbids `hostPath`. Not enough for this collector. |
| `enforce: privileged` | Allows the node-file mount. This is the evaluated industry label for a log DaemonSet. Kubernetes says a privileged namespace must document why and keep access tight. That document is the constitution clarifying note already accepted for the read-only `hostPath`. |
| No label | Admission skips the check. `identity`, `platform`, and `governance` are unlabeled. Kubernetes calls an unlabeled namespace unevaluated, not finished. |

Fluent Bit must mount `/var/log/pods` and run as root, because the log files are root-only. `restricted` rejects that pod. Vault can wear `restricted` because OpenBao is uid `10001` and uses a PVC. Copying `deploy/platform/vault/namespace.yaml` into `observability` would refuse the collector.

Source: [Enforcing Pod Security Standards](https://kubernetes.io/docs/setup/best-practices/enforcing-pod-security-standards/).

### Operational details (my calls)
| Topic | Decision |
|---|---|
| Collector's own logs | Not collected (the path only matches `proj-*`), so no feedback loop. Fluent Bit logs at `warn`; its output stays readable with `kubectl logs`. |
| PSA label | `enforce: privileged` on `observability`. Accepted 2026-10-24 without a full understanding of the rule. Read "PSA label, explained" above before changing it. |
| Read position | A 1 GiB `ReadWriteOnce` PVC `fluent-bit-state`, mounted at `/state`, holds `tail.db`. One node only. A collector restart keeps the bookmark, so it does not re-send lines still on the node. The PVC is on the node disk, so node loss deletes it. It is not a second `hostPath`. **(operator, 2026-10-24)** |
| If VictoriaLogs is down | Fluent Bit buffers in memory and retries; lines can be lost only if the node rotates the files first. No disk buffer. |
| Flux layer | New `observability` layer in `clusters/local-k3s/layers.yaml` and `clusters/vps-k3s/layers.yaml`, `dependsOn: cni, governance`, health checks on the DaemonSet and StatefulSet. It does not depend on `vault`, so collection continues if OpenBao is down. |
| Both environments | Same manifests and sizes on `local-k3s` and `vps-k3s` (10 GiB store PVC, 8 GiB cap, 1 GiB tail PVC, 30 days). Disk is checked on the VPS only. |
| Budget | Collector and store requests and limits count under `platformReservations` in the preflight. Initial proposal: Fluent Bit `50m` CPU / `64Mi`; VictoriaLogs `100m` CPU / `384Mi`. Final numbers come from the build. |
| Storage location | PVC on `local-path` (node storage, as Postgres). A separate disk can be used later through the provisioner's `nodePathMap` or a static `local` PV, with no design change. |

## Industry alignment
Matches the common pattern: Fluent Bit DaemonSet, dedicated namespace, read-only `hostPath` for the node's log folder, root user with a read-only root filesystem and dropped capabilities, namespace labeled `pod-security.kubernetes.io/enforce: privileged` rather than `restricted` or `baseline`, store on a PVC with retention set in the store. It is narrower than the common setup in three ways: it mounts only `/var/log/pods`, it uses no API token, and it collects only `proj-*`.

## Accepted risks (operator)
- **Single-node data loss.** History and the node's `/var/log/pods` files live on the node's disk. If the disk is lost or the VPS is rebuilt or terminated, the whole history is lost. No backup and no off-node copy in this version. A reboot with the disk intact keeps the history; lines logged while the node is down, and possibly the last few seconds before a hard crash, are lost.
- **Viewer availability.** The log UI runs on the same node, so logs cannot be read while the node is down.
- **Silent drop at the limit.** Data older than 30 days or over 8 GiB is deleted without an alert.
- **Residual volume fill.** The 8 GiB cap can still be passed between VictoriaLogs' checks, and the last two days are kept even over the cap. A full volume makes the store read-only. Accepted with the 8 GiB choice.
- **local-k3s disk.** The same PVCs are applied there without a disk check. A pending local volume does not change the size.
- **Namespace prefix.** A project namespace without the `proj-` prefix would not be collected.

## Rejected during design
- Projects writing their own log files to a PVC or standardized path: not a recommended pattern; needs project PVCs (not allowed in project namespaces), a collector per project, and image changes.
- Plain text copies on a PVC: about 5–10x the disk of VictoriaLogs, needs a cleanup job, and does not help a Kafka move.
- Kafka now: a broker takes roughly 1 GiB or more, too heavy for the 8 GiB `local-k3s` cluster.
- `emptyDir` or NFS for the store: history lost on restart / poor fit for VictoriaLogs.
- Shared password between Fluent Bit and VictoriaLogs: conflicts with the zero-plaintext rule.
- `emptyDir` for the Fluent Bit tail database: a collector restart would re-send lines. Operator chose a 1 GiB PVC on 2026-10-24.
- An external schema registry: one collector, plain `message`, nothing to validate.

## Scale-up path (not in this version)
Each step reuses the standard record and swappable outputs, so projects and the collector's inputs do not change.
1. Off-node copy: a second Fluent Bit `[OUTPUT]` to S3-compatible object storage, if the accepted risk stops being acceptable.
2. Per-project tenants in the store (VictoriaLogs `AccountID`/`ProjectID`, or Loki `X-Scope-OrgID`) for access isolation. A per-project size cap would need a separate store or ingestion limits; per-tenant retention in open-source VictoriaLogs must be verified first.
3. Add Fluent Bit `[OUTPUT] kafka` (topic per project) with a queue in the middle: Fluent Bit → Kafka → consumer → store.
4. Clustered store, or Loki with S3-compatible object storage for long retention.
5. Streaming sidecar (app writes a file to `emptyDir`, sidecar prints it to stdout) only for a project that cannot log to stdout.
6. Kubernetes API log streaming instead of `hostPath`, if RBAC-enforced project scope matters more.

## Seams
| Layer | Now | Swap to later | What stays unchanged |
|---|---|---|---|
| Collector | Fluent Bit | Alloy or Vector | Store and UI (both can send the same push formats) |
| Store/UI | VictoriaLogs | Loki + Grafana | Collector and project manifests (VictoriaLogs also accepts the Loki push format) |
| Reach | SSH tunnel, port-forward | Tailscale, or a gateway route in `mode: shared` | Store and collector |
| Identity | `operator-readonly` token | Keycloak `log-viewer` role | The read-only roles |

## Future (not in this version)
- Gateway route with a Keycloak `log-viewer` role for other users (default-deny, `view`-only identity).
- Grafana attached to VictoriaLogs and Hubble `:9965` metrics. If added, the Grafana/Loki check in `scripts/open-hubble-ui.sh --verify` must be updated.
- Log masking, node logs, alerting, platform-component log history.

## Decided in the build
- Images: `fluent/fluent-bit:4.2.8` and `victoriametrics/victoria-logs:v1.53.0`.
- Resources: Fluent Bit `50m` CPU / `64Mi`, request equals limit. VictoriaLogs `100m` request / `500m` limit / `384Mi`. The platform reservation that includes these numbers is not yet measured. Do not add `150m` / `448Mi` by hand.
- The CRI parser is in the collector ConfigMap. A multiline stack-trace parser was left optional and was not added.
- `DAC_READ_SEARCH` was not adopted. The collector stays root.
- Constitution wording is Clarification 2.2.1.
- Manifests live in `deploy/platform/observability/`. Helper scripts are `scripts/open-victorialogs-ui.sh` and `scripts/open-observe.sh`.

## Open for the operator
None. Approved 2026-10-22. Amended 2026-10-24: 8 GiB cap, VPS-only disk check, tail bookmark on a 1 GiB PVC, `enforce: privileged`, envelope schema stays in this repo. Implementation plan: [`docs/superpowers/plans/2026-10-22-project-log-history.md`](../plans/2026-10-22-project-log-history.md).
