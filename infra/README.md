# OpenTofu external infrastructure
#
# Ownership:
#   OpenTofu  -> node contract, DNS desired state, firewall policy, backup destination
#   Ansible   -> host harden, mounts, k3s, Flux bootstrap, host firewall enforcement
#   Flux      -> all routine Kubernetes resources from platform-config
#   ProcessManager -> Git desired-state mutations + read-only observation
#
# This tree must never manage Kubernetes resources.

## Layout

```text
infra/
  modules/
    node/              # compute/SSH/disk capability contract
    dns/               # desired DNS records
    firewall/          # desired ingress policy
    backup_target/     # off-server backup destination
  environments/
    home-lab/          # external/existing-node backend (first target)
    cloud/             # stub for later portable VPS provider
  handoff/             # generated Ansible/DNS artifacts (gitignored contents)
```

## Prerequisites

1. Install OpenTofu >= 1.6 (`tofu`).
2. Copy example tfvars:

```bash
cd infra/environments/home-lab
cp terraform.tfvars.example terraform.tfvars
# edit domain, admin_cidrs, node IP, backup endpoint
```

## Apply (home-lab)

```bash
cd infra/environments/home-lab
tofu init
tofu validate
tofu plan
tofu apply
```

Outputs and files written under `infra/handoff/`:

| File | Purpose |
|---|---|
| `home-lab.json` | Full handoff contract |
| `home-lab.inventory.yaml` | Ansible inventory fragment |
| `home-lab.dns.yaml` | DNS records to publish |

## What this does / does not do

| Does | Does not |
|---|---|
| Validate node/DNS/firewall/backup contracts | Install k3s |
| Write Ansible handoff artifacts | Bootstrap Flux |
| Record off-server backup destination | Create Keycloak/CNPG/apps |
| Keep provider/node IDs out of app config | Manage any Kubernetes API objects |

## Next owner after apply

1. Publish DNS from `home-lab.dns.yaml` (manual registrar or later DNS provider module).
2. Render Ansible inventory from handoff and run the site playbook — see [`ansible/README.md`](../ansible/README.md):

```bash
cd ansible
python3 scripts/render-inventory.py   # always refresh hosts.yaml + from_opentofu.yaml
# set flux_git_url to the private off-server seed mirror + place .secrets/
./scripts/init-encryption-vault.sh    # interactive vault password on this PC
python3 scripts/render-inventory.py --check   # fail-closed preflight
python3 scripts/render-inventory.py --spawn -- --ask-vault-pass   # manual unlock
```

3. Ansible: harden host, mount data disk, install pinned k3s, bootstrap Flux once against the private off-server seed mirror (stops writing Kubernetes after Flux Git source is Ready).
4. Flux reconciles `platform-config/clusters/production`, including the Forgejo workload, from that seed source.
5. After Forgejo is Ready, mirror `platform-config` into `platform/platform-config`, register the Flux public deploy key, and cut the `GitRepository` URL over to Forgejo. Keep the off-server mirror as the recovery source. See [`ansible/README.md`](../ansible/README.md) "Two-stage Git source".

**Git source rule:** the off-server repository is bootstrap/recovery only. Local
Forgejo `platform/platform-config` is mandatory as the steady-state source after
the cutover gate succeeds; routine production reconciliation must not remain on
GitHub/GitLab.

## Cloud later

Implement `environments/cloud` with the same module outputs. Do not fork `platform-config` for cloud. Home-lab and cloud remain one portable production target with different OpenTofu backends.

## Acceptance checks

```bash
# from repo root
python3 scripts/validate-opentofu-infra.py
```

If `tofu` is installed, the script also runs `tofu init -backend=false` + `tofu validate` in `home-lab`.
