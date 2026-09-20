# Ansible host bootstrap (k3s + Flux)

Ownership boundaries (Ansible **must not** manage routine cluster apps after Flux handover):

| Owner | May | Must not |
|---|---|---|
| **Ansible** | Host OS harden, mounts, nftables firewall, pinned k3s, one-time `local-path-retain` StorageClass, Flux controllers + Git source secret + root GitRepository/Kustomization + `sops-age` secret | Apply CNPG/Keycloak/ingress HelmReleases, ProcessManager manifests, or project apps; install Docker as the process runtime; call ProcessManager `scripts/bootstrap-control-plane.sh` / `bootstrap-worker.sh`; `kubectl apply -k platform-config/infrastructure` |
| **Flux** | All routine Kubernetes resources from `platform-config` | — |
| **OpenTofu** | Node/DNS/firewall/backup contracts → `infra/handoff/` | Kubernetes API objects |

## Prerequisites

1. OpenTofu applied for home-lab (`infra/environments/home-lab`).
2. Control machine: `ansible` / `ansible-playbook`, Python 3 + PyYAML, SSH to the application node.
3. Install collections: `ansible-galaxy collection install -r requirements.yml`
4. Node arch **amd64**.
5. Operator-supplied secrets on the control machine:
   - `ansible/.secrets/flux-deploy-key` — SSH deploy key for `flux_git_url` (default auth mode `ssh`)
   - `ansible/.secrets/sops-age/age.key` — SOPS age private key (`age.agekey` secret)
   - `k3s_secrets_encryption_key` — vault or env (see below)

## Run

```bash
cd infra/environments/home-lab && cp terraform.tfvars.example terraform.tfvars  # edit, then
tofu apply
cd ../../../ansible
python3 scripts/render-inventory.py   # always refresh hosts.yaml + from_opentofu.yaml
# place flux deploy key + sops age key under ansible/.secrets/
# set flux_git_url in group_vars/all.yaml to the private off-server seed mirror
./scripts/init-encryption-vault.sh    # interactive vault password on this PC
python3 scripts/render-inventory.py --check                        # fail-closed preflight
python3 scripts/render-inventory.py --spawn -- --ask-vault-pass    # YOU type vault password
```

Partial re-runs:

```bash
ansible-playbook playbooks/host-prep.yml
ansible-playbook playbooks/k3s.yml
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

## Flux Git source

Defaults in `group_vars/all.yaml`:

- `flux_git_url` — private **off-server seed/recovery** remote used for the first bootstrap
- `flux_git_branch: main`
- `flux_git_path: ./platform-config/clusters/production` for the current monorepo seed
- `flux_git_auth_mode: ssh` — set `https` + `flux_https_username` / `flux_https_password` for HTTPS remotes
- `flux_skip_sops_secret: false` — fail if age key missing (infrastructure unit needs decrypt)

Placeholder URLs (`<owner>`, `<org>`, `example.com`) are rejected by both the role
assertion and `render-inventory.py --check`.

After GitRepository Ready, Ansible's Kubernetes write phase ends. Deeper units
(`infrastructure`, apps) come from Git via Flux — live tasks **4.2+** start only
after that sync works. Deeper Kustomizations may be NotReady until CNPG/images/DNS exist;
Ansible still succeeds when GitRepository is Ready.

### Two-stage Git source (seed → Forgejo)

`ARCHITECTURE.md` section 4 specifies Forgejo **inside** the cluster, deployed by Flux.
Ansible does not install Forgejo. This avoids the bootstrap cycle
(Flux needs Git to install Forgejo; Forgejo must exist to host Git):

| Stage | Flux source | Who acts |
|---|---|---|
| Bootstrap | private off-server mirror (`flux_git_url`) | Ansible `flux_bootstrap` |
| Steady state | `ssh://git@forgejo.internal/platform/platform-config.git` | Flux reconciles Forgejo from the mirror, then source is cut over |

Ansible scope ends at stage 1: controllers, Git auth secret, root GitRepository,
`kustomization flux-system`, `sops-age`. It must not create the Forgejo workload,
the Forgejo repository, or the cutover — those are Flux/operator Git changes.

Cutover (operator, after Forgejo is Ready) is Git work, not Ansible work:

1. Create `platform/platform-config` in the Forgejo UI (empty, default branch `main`).
2. Mirror the seed repository into it, preserving history.
3. Register the Flux **public** deploy key (`ansible/.secrets/flux-deploy-key.pub`)
   as a read-only deploy key on that repository.
4. Commit the `GitRepository` URL change in `clusters/production/flux-system/gotk-sync.yaml`
   to the Forgejo repository (or to the mirror and re-sync once).
5. Verify `kubectl -n flux-system get gitrepository flux-system` becomes Ready against
   `forgejo.internal`, then keep the off-server mirror as the recovery source.

Do not point `flux_git_url` at `forgejo.internal` before Flux has created Forgejo:
the first bootstrap would fail with no Git server reachable.

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
- `inventories/home-lab/group_vars/from_opentofu.yaml`

`--force` is a deprecated no-op (refresh is always on).
Use `--check` / `--spawn` for fail-closed gates (SSH TCP, `flux_git_url`,
deploy/age keys, encryption key) before launching `ansible-playbook`.
Never touches `group_vars/all.yaml`, `application.yaml`, `registry.yaml`, or `.secrets/`.

Tracked example: `inventories/home-lab/hosts.yaml.example`.

## Validation

```bash
# from repo root
python3 scripts/validate-ansible-bootstrap.py
cd ansible && ansible-playbook playbooks/site.yml --syntax-check
```

## Live acceptance (application node)

- `systemctl is-active k3s` → `active`
- `k3s -v` contains `v1.32.13+k3s1`
- `kubectl get node -o wide` Ready; labels include `platform.processmanager.dev/workload=true`
- `kubectl get sc local-path-retain` exists
- `flux version --client` contains `v2.9.5`
- `kubectl -n flux-system get gitrepository flux-system` Ready against configured `flux_git_url`
- `kubectl -n flux-system get kustomization flux-system` progressing/Ready for `./clusters/production`
- Re-run `site.yml` stays idempotent; no Ansible apply under `platform-config/infrastructure/`
