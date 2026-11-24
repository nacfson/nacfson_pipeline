# Contract: Ansible Runner (control center)

**Implements**: FR-013, FR-014, FR-024, FR-031, SC-015 | **Runs in**: `nacfson-control-center` (separate private repository) | **Research**: [R22](../research.md#r22-automated-ansible-runs-from-the-control-center)

This repository owns the playbooks, the GKE permissions, and the OCI permissions. The control-center repository owns the actions, the triggers, and the runner's keys. A change to anything in this file is a pull request in both repositories.

## Actions

| Action | Playbook | Started by | Arguments |
|---|---|---|---|
| `kafka.site` | `infra/oci/ansible/site.yml` | merge watcher, or operator | none |
| `kafka.renew-certs` | `infra/oci/ansible/renew-certs.yml` | daily schedule, or operator | `force_renew` (operator only), `limit` (one host, operator only) |
| `kafka.rolling-change` | `infra/oci/ansible/rolling-change.yml` | operator | `tags`: `patch` or `reboot` |
| `kafka.rebuild-node` | `infra/oci/ansible/rebuild-node.yml` | operator | `limit`: one broker host |
| `kafka.replace-controller` | `infra/oci/ansible/replace-controller.yml` | operator | `limit`: one controller host |
| `kafka.pause` / `kafka.resume` | none | operator | none |

Operator-started actions use the control center's passkey step-up, like its existing actions. Host names are checked against the inventory (`a1-1`, `a1-2`, `micro-1`, `micro-2`).

## Triggers

- **Merge watcher**:
  - every 5 minutes, reads `main` from the public GitHub API
  - starts `kafka.site` when all three hold:
    1. the commit's required checks succeeded
    2. it differs from `/etc/nacfson/applied-commit` on any VM
    3. the changed paths include `infra/kafka/` or `infra/oci/ansible/`
- **Daily**: `kafka.renew-certs` once per day. The `certs` role renews only certificates with 30 days or fewer left.
- **Paused**: no automatic run starts. Operator-started actions still run.

## Run rules

1. **One run at a time.** A run takes the lock, and others wait.
2. **Commit**: the checkout is the read-only clone at the exact commit being applied. Local changes are refused.
3. **Pre-flight**: the runner first runs `infra/oci/ansible/preflight.yml`. It fails when a broker is missing or any partition is under-replicated, or when a VM cannot be reached. The runner reports any failure of it as `DeployBlocked`, not `DeployFailed`, and does not start the playbook. Every playbook also imports it, so a run started by hand stops the same way. (Ansible's own exit code 3 already means an unreachable host, so the result is read from which playbook failed, not from a custom exit code.)
4. **Plays**: use `serial: 1` and `any_errors_fatal: true`, with the full-replication wait between VMs.
5. **Last task on each VM**: writes the applied commit to `/etc/nacfson/applied-commit`.
6. **Run log**: kept in the control center's UI for 30 days.

## Result events

The runner creates one Kubernetes Event in `oci-link` per run. The `involvedObject` is `{kind: Namespace, name: oci-link, namespace: oci-link}`: the API server rejects an Event in `oci-link` whose `involvedObject.namespace` is empty. `source.component` is `kafka-runner`, and the `reportingController` is `nacfson-control-center/kafka-runner`.

| `reason` | `type` | When | `message` |
|---|---|---|---|
| `DeployOK` | Normal | the run finished on every VM | `<playbook> <commit> applied to <n> VMs in <minutes> min` |
| `DeployFailed` | Warning | a task failed | `<playbook> <commit> failed on <vm> at <role>/<task>` |
| `DeployBlocked` | Warning | the pre-flight play found the cluster degraded | `<playbook> <commit> blocked: <reason>` |

`ops-relay` maps these to subject `deploy/<playbook>`:
- `DeployFailed` and `DeployBlocked` are DOWN
- `DeployOK` is OK

It sends one message per state change (FR-023).

## Permissions

| Where | Identity | Allowed | Nothing else |
|---|---|---|---|
| GKE | Google service account `cc-kafka-runner`, impersonated by the control-center VM's service account | IAM `roles/container.clusterViewer`. Role `kafka-runner` in `oci-link` (`clusters/gke/oci-link/runner-rbac.yaml`): create, get, list `certificaterequests.cert-manager.io`; create `events`. | No Secrets, no other namespace |
| OCI | user and group `kafka-runner` (`infra/oci/terraform/iam.tf`) | `use bastion` and `manage bastion-session` scoped strictly by `target.bastion.ocid` for SSH port forwarding sessions on the Kafka Bastion, plus read-only lookup on `instances`, `vcn`, `subnets`, `vnic-attachments`, and `vnics` (Constitution Amendment `3.1.0`; no `instance-agent-plugins` read grant) | Cannot create, change, or delete instances, networks, or volumes |
| VMs | SSH user `ubuntu` through Bastion SSH port forwarding (`:22`), key from the control center | `sudo` for Ansible | |

The OCI API key and the SSH key are created on the control center, stored there under `/var/lib/nacfson-cc/credentials/kafka-runner/` (mode `0600`, owned by the service user), and rotated every 90 days. Only their public halves leave the control center: the public API key goes to `terraform.tfvars` (`kafka_runner_api_public_key`), and the public SSH key is registered in `infra/oci/ansible/group_vars/all/ssh_authorized_keys.yml` (`ubuntu_ssh_authorized_keys`, converged across all four VMs by `roles/base` with `exclusive: true`) and in `terraform.tfvars` (`ssh_authorized_keys`) for launch/rebuild injection.
