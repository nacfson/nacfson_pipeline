# Claude Code & Multi-Agent Entrypoint (`nacfson_pipeline`)

To prevent rule drift across agent harnesses (`agy`, `claude`, `omp`, `opencode`, `codex`, `grok`, `cursor`), this file does not duplicate repository instructions. Read and follow these canonical rulebooks before exploring or editing the repository:

1. **Architecture, Context & Engineering Rules**: [`AGENTS.md`](AGENTS.md)
2. **Code Review & Verification Rulebook**: [`REVIEW.md`](REVIEW.md)
3. **Constitutional Invariants**: [`.specify/memory/constitution.md`](.specify/memory/constitution.md)
4. **Canonical Interactive Architecture Handbook**: [`docs/platform-architecture.html`](docs/platform-architecture.html)

## Required Verification & Quality Gates

Run the relevant validation commands from [`AGENTS.md` §5](AGENTS.md) before claiming work is complete or committing:

```bash
# 1. Go Auth Gateway unit & contract tests
(cd gateway && go test -v ./... && go vet ./...)

# 2. Go Vault Proxies build & vet check
(cd services/vault-proxies && go test -v ./... && go vet ./...)

# 3. Platform preflight unit tests (1/n budget, QoS, isolation, door & TLS rules)
python3 -m unittest discover tests/preflight

# 4. Full Kustomize render, kubeconform schema validation & preflight gate (local-k3s & vps-k3s)
./scripts/render-validate.sh local-k3s
./scripts/render-validate.sh vps-k3s

# 5. Canonical Architecture Handbook synchronization & invariant check
python3 scripts/check-arch-doc-sync.py --worktree
```
