# Validation Record: Minimal VM Bootstrap

**Date**: 2026-10-03
**Feature**: `specs/003-minimal-vm-bootstrap`
**Target Profile**: Ubuntu Server 24.04 LTS ARM64 (systemd, Python 3.12, >= 2 vCPUs, >= 1,900 MiB RAM, >= 10 GiB free storage)
**Controller Profile**: Python 3.12, OpenSSH, `ansible-core==2.21.4`
**Accepted Version**: `v1.35.9+k3s1`

---

## 1. Controller & Static Verification

| Test Case | Command | Result | Details |
|---|---|---|---|
| Playbook Syntax Check | `ansible-playbook -i 'syntax-check-vm,' -u ubuntu -e 'k3s_version=v1.35.9+k3s1' bootstrap/bootstrap.yml --syntax-check` | **PASS (rc=0)** | Full playbook and all included task files (`preflight.yml`, `install.yml`, `verify.yml`) parse cleanly without syntax or schema errors. |
| Zero Inventory Hosts | `ansible-playbook bootstrap/bootstrap.yml` | **PASS (rc=2)** | Successfully halted before execution: `Exactly one inventory host required, but found 0 host(s).` |
| Multiple Inventory Hosts | `ansible-playbook -i 'host-one,host-two,' -u ubuntu -e 'k3s_version=v1.35.9+k3s1' bootstrap/bootstrap.yml` | **PASS (rc=2)** | Successfully halted before execution: `Exactly one inventory host required, but found 2 host(s).` |
| Missing Remote User | `ansible-playbook -i 'test-vm,' -e 'k3s_version=v1.35.9+k3s1' bootstrap/bootstrap.yml` | **PASS (rc=2)** | Successfully halted: `Explicit non-empty remote SSH user must be provided (e.g. -u SSH_USER).` |
| Missing K3s Version | `ansible-playbook -i 'test-vm,' -u ubuntu bootstrap/bootstrap.yml` | **PASS (rc=2)** | Successfully halted: `Invalid or missing k3s_version 'NOT_SET'. Supported version allowlist: v1.35.9+k3s1.` |
| Unsupported K3s Version | `ansible-playbook -i 'test-vm,' -u ubuntu -e 'k3s_version=v0.0.0+k3s1' bootstrap/bootstrap.yml` | **PASS (rc=2)** | Successfully halted: `Invalid or missing k3s_version 'v0.0.0+k3s1'. Supported version allowlist: v1.35.9+k3s1.` |
| CI Lint Integration | `.github/workflows/lint.yaml` | **PASS** | Dedicated `bootstrap-syntax` job added with Python 3.12, pinned `requirements.txt`, and syntax check. |
| Non-Root Kubectl Execution | `ssh <target> 'kubectl get nodes'` | **PASS** | Standard PATH symlinks (`/usr/bin/kubectl`) and mode 0600 `~/.kube/config` enable immediate non-root diagnostic commands. |

---

## 2. Codebase Audit Against Requirements

| Requirement | Audit Summary | Status |
|---|---|---|
| **FR-001** (Three settings / single target) | Verified in `bootstrap/bootstrap.yml`: exactly 1 host enforced; requires only target address, SSH user (`-u`), and pinned version (`-e k3s_version=...`). Reuses existing SSH authentication. | **PASS** |
| **FR-002** (Supported host profile) | Verified in `bootstrap/tasks/preflight.yml`: Ubuntu 24.04 ARM64, Python 3.12, systemd, sudo privilege, >=2 vCPUs, >=1,900 MiB RAM, >=10 GiB free on ancestor backing `/var/lib/rancher`, swap disabled, cgroups, kernel modules `overlay`/`br_netfilter`, tools, DNS, HTTPS trust, artifact reachability. | **PASS** |
| **FR-003** (Pinned version & defaults) | Verified in `bootstrap/bootstrap.yml`, `bootstrap/templates/k3s.service.j2`, and `bootstrap/tasks/install.yml`: only `v1.35.9+k3s1` accepted; verified checksum download; standard systemd unit without custom tuning flags. | **PASS** |
| **FR-004** (Bounded verified readiness) | Verified in `bootstrap/tasks/verify.yml` and `contracts/bootstrap.md`: 15s SSH, 60s tasks, 30s/600s download, 60s activation, 300s startup, 300s readiness deadline per check (service active/enabled, local `/readyz`, exactly 1 Ready node, CoreDNS rollout), 5s individual API request timeout. | **PASS** |
| **FR-005** (Truthful failure reporting) | Verified in `bootstrap/bootstrap.yml`: structured `bootstrap_result` tracking `completed_stages`, `failed_stage`, `readiness_outcomes`, and `next_action`. Preserves existing state on failure without false rollback claims. | **PASS** |
| **FR-006** (Rerun state preservation) | Verified in `bootstrap/tasks/preflight.yml` and `bootstrap/tasks/install.yml`: 3-way classification (`fresh`, `managed_compatible`, `conflicting_or_unknown`). Non-secret receipt at `/var/lib/nacfson-bootstrap/installation.json` (mode 0600, root-owned). Skips file writes and avoids restarting running service on healthy rerun. | **PASS** |
| **FR-007** (Credential isolation) | Audited across `bootstrap/`: zero backend credentials handled; no private keys stored in Git; no kubeconfig exported to controller; base installation operates independently of Vault or private image paths. | **PASS** |
| **FR-008** (Excluded lifecycle & workload operations) | Audited across `bootstrap/`: zero cloud provisioning commands, zero firewall/security-list alterations, zero disk partitioning/formatting, zero project workload manifests, zero backup or upgrade jobs. | **PASS** |
| **FR-009** (Scope-bounded result reporting) | Verified in `bootstrap/bootstrap.yml`: result fields identify target, requested/observed versions, stage outcomes, and explicitly instruct operator that platform workload deployment remains a separate step. | **PASS** |

---

## 3. Success Criteria & Live Acceptance Status

| Criterion | Target Requirement | Live Verification Status | Notes |
|---|---|---|---|
| **SC-001** | 2 fresh supported test hosts prepared with 1 invocation and 3 settings | **PENDING LIVE VM FIXTURE** | Requires operator-provided clean Ubuntu 24.04 ARM64 VM instances matching the support profile. |
| **SC-002** | 4 passing readiness checks; deliberately failed check prevents success | **PENDING LIVE VM FIXTURE** | Verified locally via task implementation in `verify.yml` with fail-fast assertions and deadlines; live cluster execution pending. |
| **SC-003** | 2 consecutive reruns cause 0 reinstalls, 0 restarts, 0 identity changes | **PENDING LIVE VM FIXTURE** | Guarded by `host_state == 'managed_compatible'` logic and unchanged receipt/service verification; live cluster execution pending. |
| **SC-004** | All invalid-input and conflict cases produce actionable failure | **PARTIALLY VERIFIED** | Controller invalid-input cases verified (rc=2); host-level prerequisite failure fixtures pending live test hosts. |
| **SC-005** | Zero backend credentials and zero application deployments | **PASS** | Fully audited across all repository additions and playbooks. |
