# Ansible host bootstrap (k3s + Forgejo + Flux)

Ownership boundaries (Ansible **must not** manage routine cluster apps after Flux handover):

| Owner | May | Must not |
|---|---|---|
| **Ansible** | Host OS harden, mounts, nftables firewall, pinned k3s, one-time `local-path-retain` StorageClass, minimal Forgejo bootstrap workload/repository, Flux controllers + Git source secret + root GitRepository/Kustomization + `sops-age` secret | Apply CNPG/Keycloak/ingress HelmReleases, ProcessManager manifests, or project apps; install Docker as the process runtime; call ProcessManager bootstrap scripts; `kubectl apply -k platform-config/infrastructure` |
| **Flux** | All routine Kubernetes resources from `platform-config` | — |
| **OpenTofu** | Node/DNS/firewall/backup contracts → `infra/handoff/` | Kubernetes API objects |

## Prerequisites

1. OpenTofu applied for home-lab (`infra/environments/home-lab`).
2. Control machine: `ansible` / `ansible-playbook`, Python 3 + PyYAML, SSH to the application node.
3. Install collections: `ansible-galaxy collection install -r requirements.yml`
4. Node arch **amd64**.
5. Operator-supplied secrets on the control machine:
   - `ansible/.secrets/flux-deploy-key` — SSH deploy key registered automatically as read-only in Forgejo
   - `ansible/.secrets/sops-age/age.key` — SOPS age private key (`age.agekey` secret)
   - `k3s_secrets_encryption_key` — vault or env (see below)

## Run

```bash
cd infra/environments/home-lab && cp terraform.tfvars.example terraform.tfvars  # edit, then
tofu apply
cd ../../../ansible
python3 scripts/render-inventory.py   # always refresh hosts.yaml + from_opentofu.yaml
# place flux deploy key + sops age key under ansible/.secrets/
./scripts/init-encryption-vault.sh    # interactive vault password on this PC
python3 scripts/render-inventory.py --check                        # fail-closed preflight
python3 scripts/render-inventory.py --spawn -- --ask-vault-pass    # YOU type vault password
```

Partial re-runs:

```bash
ansible-playbook playbooks/host-prep.yml
ansible-playbook playbooks/storage.yml   # data disk only; skips host firewall
ansible-playbook playbooks/firewall.yml  # host nftables only; skips storage/k3s
ansible-playbook playbooks/k3s.yml
ansible-playbook playbooks/forgejo.yml
ansible-playbook playbooks/flux.yml
```

## Encryption key (preferred: Ansible Vault + manual unlock)

Secrets stay encrypted at rest in `group_vars/application/vault.yml` (gitignored).
**Unlock is manual on the local control PC** — `ansible.cfg` does **not** set
`vault_password_file`. You type the vault password each playbook run.

```bash
cd ansible
./scripts/init-encryption-vault.sh   # prompts for New Vault password (TTY required)
ansible-playbook playbooks/site.yml --ask-vault-pass
# or: python3 scripts/render-inventory.py --spawn -- --ask-vault-pass
```

Fallback only: `export K3S_SECRETS_ENCRYPTION_KEY="$(openssl rand -base64 32)"`.
Missing key → role fails closed (no auto-generate). Never commit `*.vault.yml`.

### Vault unlock constraints

| Constraint | Why it matters |
|---|---|
| **Human + TTY required** | `--ask-vault-pass` needs an interactive local terminal. Headless/`cron`/non-TTY `--spawn` cannot unlock vault without a password file or env fallback. |
| **Password is operator memory / password manager** | Not stored in git. Losing it means `vault.yml` cannot be decrypted (regenerate key only if you accept re-encrypting cluster secrets carefully). |
| **Every bootstrap/re-run asks again** | No silent decrypt. Safer for a home-lab control laptop; less "set and forget". |
| **Preflight cannot prove unlock** | `--check` only verifies `vault.yml` exists. Wrong password fails later inside `ansible-playbook`. |
| **Not the same as `.secrets/` files** | Deploy key + SOPS age key remain files under `ansible/.secrets/` (mode 600). Vault protects the *k3s encryption key var*; those Git/SOPS credentials are a separate disk-secret threat model. |
| **Optional unattended override** | Explicit only: `ansible-playbook ... --vault-password-file /path` (do not put that path in `ansible.cfg` for the default operator flow). |
| **Env export bypasses vault** | `K3S_SECRETS_ENCRYPTION_KEY` skips vault entirely for that shell — useful for break-glass, not the default. |

## Local Forgejo bootstrap and Flux source

Ansible breaks the Git/Flux bootstrap cycle locally:

1. Query Kubernetes for the `forgejo/forgejo` Deployment.
2. If absent, install the pinned minimal Forgejo workload with retained storage.
3. If present, skip installation and preserve the existing workload and data.
4. Wait for readiness, create the `platform` organization and
   `nacfson_pipeline` repository when absent, and register the Flux public key
   as read-only.
5. If `main` is absent, bundle the committed local Git history and seed Forgejo.
6. Verify the existing or newly seeded Forgejo `main` ref over SSH.
7. Install Flux and point its `GitRepository` directly at
   `ssh://git@forgejo.forgejo.svc.cluster.local:22/platform/nacfson_pipeline.git`.

No GitHub or GitLab repository is required for bootstrap or reconciliation.
The local working tree is not mounted into Flux: Ansible transfers committed
Git history into Forgejo, and Forgejo remains the network Git authority.

Defaults in `group_vars/all/settings.yaml`:

- `flux_git_url` — in-cluster Forgejo SSH repository
- `flux_git_branch: main`
- `flux_git_path: ./platform-config/clusters/production`
- `flux_git_auth_mode: ssh`
- `flux_skip_sops_secret: false`

After `GitRepository` becomes Ready, Ansible stops applying platform and
application manifests. Flux owns routine reconciliation. Re-running
`site.yml` reports the existing Forgejo installation, preserves an existing
`main` branch, refreshes credentials idempotently, and verifies readiness
before continuing.
 
### Access Forgejo from the control Mac
 
The host firewall intentionally does not expose Forgejo's NodePort. Forward it
through the WireGuard-reachable SSH service and keep the tunnel terminal open:
 
```bash
ssh -L 30080:127.0.0.1:30080 nacfson@10.0.0.1
```
 
Open `http://127.0.0.1:30080` and sign in as `platform-bootstrap`. Retrieve the
generated password without copying it into the repository:
 
```bash
ssh nacfson@10.0.0.1 \
  "/usr/local/bin/k3s kubectl --kubeconfig /etc/rancher/k3s/k3s.yaml \
  -n forgejo get secret forgejo-bootstrap-admin \
  -o jsonpath='{.data.password}' | base64 -d; echo"
```
 
Flux has no built-in web UI. Inspect it through SSH:
 
```bash
ssh nacfson@10.0.0.1 \
  'export KUBECONFIG=/etc/rancher/k3s/k3s.yaml; /usr/local/bin/flux get all'
```

## Backup stub

`backup_agent` writes `/etc/platform/backup-target.env` from OpenTofu handoff.
Full encrypted etcd off-server upload is a **follow-up**; stub timer stays off unless
`backup_agent_enable_stub_timer: true`.

## Inventory bridge

```bash
python3 scripts/render-inventory.py                 # always refresh from OpenTofu handoff
python3 scripts/render-inventory.py --check         # fail-closed operator preflight
python3 scripts/render-inventory.py --spawn -- --ask-vault-pass   # manual vault unlock on local PC
# (site.yml needs vault vars — always pass --ask-vault-pass unless using env fallback)
```

Always regenerated (gitignored) — change servers via `terraform.tfvars` + `tofu apply`:

- `inventories/home-lab/hosts.yaml`
- `inventories/home-lab/group_vars/all/from_opentofu.yaml`

`--force` is a deprecated no-op (refresh is always on).
Use `--check` / `--spawn` for fail-closed gates (SSH TCP, `flux_git_url`,
deploy/age keys, encryption key) before launching `ansible-playbook`.
Never touches `group_vars/all/settings.yaml`, `application/settings.yaml`, `registry.yaml`, or `.secrets/`.

Tracked example: `inventories/home-lab/hosts.yaml.example`.

## Validation

```bash
# from repo root
python3 scripts/validate-ansible-bootstrap.py
cd ansible && ansible-playbook playbooks/site.yml --syntax-check
```

## Live acceptance (application node)

- `systemctl is-active k3s` → `active`
- `k3s -v` contains `v1.34.11+k3s1`
- `kubectl get node -o wide` Ready; labels include `platform.processmanager.dev/workload=true`
- `kubectl get sc local-path-retain` exists
- `flux version --client` contains `v2.9.5`
- `kubectl -n flux-system get gitrepository flux-system` Ready against configured `flux_git_url`
- `kubectl -n flux-system get kustomization flux-system` progressing/Ready for `./platform-config/clusters/production`
- Re-run `site.yml` stays idempotent; no Ansible apply under `platform-config/infrastructure/`
