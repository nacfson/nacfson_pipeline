# Quickstart Validation: Minimal VM Bootstrap

**Status**: Implementation guide. The `bootstrap/` playbook, tasks, templates, and configuration are implemented and syntax-verified. Live acceptance tests are performed on operator-provided target VMs.

## 1. Prepare prerequisites

Use two clean disposable Ubuntu Server 24.04 ARM64 VM installations matching the [operator contract](contracts/bootstrap.md). They may be tested sequentially; this guide does not create or rebuild VMs. Confirm existing verified SSH access, noninteractive sudo, suitable networking/storage, and independent public download access. Do not run failure injection against production.

On the controller, with Python 3.12 and OpenSSH already available:

```bash
cd /path/to/nacfson_pipeline/bootstrap
python3.12 -m venv /tmp/nacfson-bootstrap-venv
/tmp/nacfson-bootstrap-venv/bin/python -m pip install -r requirements.txt
source /tmp/nacfson-bootstrap-venv/bin/activate
ansible --version
```

Expected dependency: `ansible-core 2.21.4`. The initial accepted cluster version is `v1.35.9+k3s1`; no implicit version selection occurs.

## 2. Validate configuration before installation

Set the target's existing SSH address/alias and account:

```bash
BOOTSTRAP_TARGET=your-existing-vm
BOOTSTRAP_USER=ubuntu
ssh -o BatchMode=yes -o StrictHostKeyChecking=yes -o ConnectTimeout=15 \
  "$BOOTSTRAP_USER@$BOOTSTRAP_TARGET" 'sudo -n true'
ansible-playbook -i "$BOOTSTRAP_TARGET," -u "$BOOTSTRAP_USER" \
  bootstrap.yml -e 'k3s_version=v1.35.9+k3s1' --syntax-check
ansible-playbook -i "$BOOTSTRAP_TARGET," -u "$BOOTSTRAP_USER" \
  bootstrap.yml -e 'k3s_version=v1.35.9+k3s1' --check
```

Expected: prerequisites are checked without installation or service changes. A fresh VM receives a validation-only result, never a claim of live readiness. Unknown SSH host identity must be independently verified through the existing operator process, not bypassed.

## 3. Bootstrap and inspect

```bash
ansible-playbook -i "$BOOTSTRAP_TARGET," -u "$BOOTSTRAP_USER" \
  bootstrap.yml -e 'k3s_version=v1.35.9+k3s1'
ssh "$BOOTSTRAP_USER@$BOOTSTRAP_TARGET" \
  'sudo -n systemctl is-active k3s'
ssh "$BOOTSTRAP_USER@$BOOTSTRAP_TARGET" \
  'sudo -n systemctl is-enabled k3s'
ssh "$BOOTSTRAP_USER@$BOOTSTRAP_TARGET" \
  'sudo -n k3s kubectl --request-timeout=5s get --raw=/readyz'
ssh "$BOOTSTRAP_USER@$BOOTSTRAP_TARGET" \
  'sudo -n k3s kubectl --request-timeout=5s get nodes -o wide'
ssh "$BOOTSTRAP_USER@$BOOTSTRAP_TARGET" \
  'sudo -n k3s kubectl --request-timeout=5s -n kube-system rollout status deployment/coredns --timeout=300s'
ssh "$BOOTSTRAP_USER@$BOOTSTRAP_TARGET" \
  'sudo -n k3s kubectl --request-timeout=5s get namespaces'
```

Expected: selected version, active/enabled service, responding management interface, one Ready node, available CoreDNS, and default cluster namespaces/components only. No project, identity, or vault installation. Check host policy remains unchanged apart from expected cluster-generated runtime networking. Repeat on the second clean test installation for SC-001.

A successful base bootstrap does not establish application image access, database recovery, or Vault compliance. Do not automatically invoke the existing application release script.

## 4. Verify safe repetition

On a disposable bootstrapped host, create a non-secret verification object and capture identity/service state:

```bash
ssh "$BOOTSTRAP_USER@$BOOTSTRAP_TARGET" \
  'sudo -n k3s kubectl --request-timeout=5s create configmap bootstrap-acceptance --from-literal=sentinel=preserve-me'
ssh "$BOOTSTRAP_USER@$BOOTSTRAP_TARGET" \
  'sudo -n k3s kubectl --request-timeout=5s get nodes -o custom-columns=NAME:.metadata.name,UID:.metadata.uid'
ssh "$BOOTSTRAP_USER@$BOOTSTRAP_TARGET" \
  'sudo -n systemctl show k3s -p NRestarts -p ActiveEnterTimestampMonotonic'
```

Run the normal bootstrap command twice. Capture the same observations after each run and inspect the verification object:

```bash
ssh "$BOOTSTRAP_USER@$BOOTSTRAP_TARGET" \
  'sudo -n k3s kubectl --request-timeout=5s get configmap bootstrap-acceptance -o jsonpath="{.data.sentinel}"'
```

Expected: unchanged node UID, sentinel value, service start time/restart count, managed file hashes, and receipt; zero reinstall/restart operations. This proves preservation of the exercised cluster state, not full disaster recovery. Remove the test object afterward using `k3s kubectl delete configmap bootstrap-acceptance` through the same operator connection.

## 5. Exercise failure boundaries

Missing/unsupported versions and multiple-target inventories are runnable input failures:

```bash
ansible-playbook -i "$BOOTSTRAP_TARGET," -u "$BOOTSTRAP_USER" bootstrap.yml
ansible-playbook -i "$BOOTSTRAP_TARGET," -u "$BOOTSTRAP_USER" \
  bootstrap.yml -e 'k3s_version=v0.0.0+k3s1'
ansible-playbook -i 'host-one,host-two,' -u "$BOOTSTRAP_USER" \
  bootstrap.yml -e 'k3s_version=v1.35.9+k3s1'
```

Expected: nonzero exit and no installation changes. For the remaining cases, prepare each condition only in an isolated test fixture, run the same normal command, and compare state before/after:

| Test condition | Expected outcome |
| --- | --- |
| Missing SSH/sudo, unsupported OS/CPU, insufficient memory/disk, CIDR overlap | Specific prerequisite failure before installation |
| Blocked download, missing release artifact, deliberately corrupted staged binary | Bounded failure; no unverified executable installed |
| Different existing version, modified unit, unexpected config/drop-in | Conflict reported without overwrite, upgrade, or reset |
| Interrupted file installation with no valid receipt | Stop and identify partial/unknown state |
| Matching installed files/receipt with service stopped | Start and verify without reinstall or data changes |
| Service/API/node/CoreDNS never ready | Nonzero result within defined stage deadlines; no false rollback |

Inspect generated configuration and captured sanitized output: no backend credentials, copied private operator keys, exported administrative kubeconfig, project workload deployment, or Vault operations. The [data model](data-model.md) defines allowed state transitions. Record pass/fail evidence for SC-001–SC-005; syntax/check-mode success alone does not satisfy live acceptance.
