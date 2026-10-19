## Intent

<!-- Summarize why this change is needed and what problem or contract it addresses. -->

## Testing & Verification

<!-- Paste output from the verification commands you ran locally (e.g., python3 scripts/review-cycle.py verify --scope branch). -->

- [ ] `python3 -m unittest discover tests/preflight`
- [ ] `python3 -m unittest discover tests/review_cycle`
- [ ] `python3 scripts/check-arch-doc-sync.py --worktree`
- [ ] `./scripts/render-validate.sh local-k3s && ./scripts/render-validate.sh vps-k3s` (if `deploy/` or `clusters/` changed)

## Risk Tier

<!-- Output from: python3 scripts/review-cycle.py classify-risk --scope branch -->

- **Effective Tier**: `Low` | `Medium` | `High`
- **Review Depth**: `Quick` | `Standard` | `Deep`
- **Circuit Breaker State**: `CLOSED` | `OPEN`

## How to Revert

<!-- Exact command to roll back this change and trip the incident circuit breaker if post-merge regressions occur. -->

```bash
./scripts/rollback-change.sh <merge-commit-sha>
```

## Automated Review Receipt

<!-- Paste output from: python3 scripts/review-cycle.py summary --markdown -->
`0 Important, 0 Security, 0 Nit, 0 Pre-existing`
