# Operator Contract: Minimal VM Bootstrap

## Invocation

Run from the proposed `bootstrap/` directory after implementation:

```bash
ansible-playbook -i 'VM_ADDRESS,' -u SSH_USER bootstrap.yml \
  -e 'k3s_version=v1.35.9+k3s1'
```

The trailing comma makes this a one-host inline inventory. Replace address and user, or use a previously configured SSH host alias. No cloud credentials, application credentials, or new SSH key variables are accepted by this feature. Ordinary Ansible connection configuration remains available through the operator's existing environment; it does not bypass bootstrap validation.

Exactly one host is allowed. Zero/multiple hosts, missing user/version, and unsupported versions must fail before installation. The accepted release list is internal version-controlled support policy, not a fourth operator setting.

## Preconditions

- Ubuntu Server 24.04 LTS ARM64; Python 3.12; systemd; existing noninteractive sudo and verified SSH host identity.
- At least 2 visible CPUs, 1,900 MiB reported total RAM, and 10 GiB free at the filesystem backing `/var/lib/rancher`.
- Swap disabled; supported cgroups and kernel modules; working DNS/default route/HTTPS trust; normal Ubuntu system/network tools available.
- Host/cloud policies already permit default cluster operation and restrict management traffic to intended administrators. Bootstrap does not open cloud security rules or disable host firewalls.
- On fresh hosts, pod/service CIDRs `10.42.0.0/16` and `10.43.0.0/16` must not overlap existing non-default routes/interface networks; required release-specific ports must be free.
- Public release artifacts and bootstrap images reachable independently of application identity, Vault, or private GHCR authorization.

The operator is responsible for cloud policies not observable from the VM. Preflight must state this limit; passing endpoint checks alone is not proof that image pulls or cluster networking will work.

## Fixed execution limits

| Operation | Bound |
| --- | --- |
| SSH connection | 15 seconds |
| Ordinary inspection/remote task | 60 seconds |
| Download request idle wait | 30 seconds |
| One artifact download, outer bound | 600 seconds |
| Service activation request | 60 seconds, nonblocking activation |
| Service startup | 300 seconds in the service unit |
| Each service/API/node/DNS readiness stage | 300 seconds |
| One API request | 5 seconds |

Task/process deadlines must complement connection timeouts. Use no unbounded retry loops. These constants belong to implementation, not the public configuration surface. Check-mode tasks that cannot safely predict a change must explicitly skip it.

## Outcomes

- **Ready**: Non-secret result identifies target/version and all four passing readiness checks; process exits zero; next step is separate platform deployment/preflight.
- **Failed**: Nonzero exit with target and failed condition/stage. Identify completed stages when known. Keep existing data and report partial installation honestly. Never suggest broad automatic uninstall/reset as the default remedy.
- **Validation only**: Check mode completed read-only validation, with zero installation/service mutations. Report whether further live installation/verification is required; never claim a fresh target is ready.

Do not promise particular nonzero exit codes beyond Ansible's native semantics. Unreachable/authentication errors can be reported natively. Do not print credentials, full kubeconfig, raw environment, or potentially sensitive journal content.

## Ownership and reruns

Bootstrap owns only its installed binary, service definition, and receipt. K3s owns its runtime state. Inspect unexpected config files, config drop-ins, systemd overrides/environment sources, alternate installations, or unowned existing data and stop before modifying them. Missing receipt means an existing installation cannot be automatically adopted.

A successful second run must not rewrite these managed files or restart an already healthy service. Changes to an accepted version never authorize upgrading an existing different version. Platform manifests, Vault integration, custom registry routing, backup/restore, and host lifecycle maintenance remain separate work.
