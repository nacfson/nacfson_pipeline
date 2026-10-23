---
name: security-review
description: Execute a deep security and trust-boundary audit using fresh subagents and record the security review receipt via scripts/review-cycle.py.
compatibility: Works in Antigravity (agy) and compatible multi-agent harnesses.
---

## User Input
```text
$ARGUMENTS
```

## Purpose
Perform a focused security audit across authentication (`gateway/`, `deploy/platform/identity/`), OpenBao & DB proxies (`deploy/platform/vault/`, `services/vault-proxies/`), CNI/eBPF policies (`deploy/platform/cni/`), Edge TLS (`deploy/platform/tls/`), and tenant pod security (`deploy/projects/`).

## Step-by-Step Workflow

1. **Run Deterministic Verification & Risk Classification**:
   ```bash
   python3 scripts/review-cycle.py verify --scope staged --json
   python3 scripts/review-cycle.py classify-risk --scope staged --json
   ```

2. **Dispatch Security & Risk Subagents (`invoke_subagent`)**:
   - Launch fresh-context subagents via `invoke_subagent` (`TypeName: "research"`, `Model: "inherit"`) instructed by:
     - `.agents/review-prompts/security-reviewer.md`
     - `.agents/review-prompts/contract-reviewer.md`
     - `.agents/review-prompts/risk-classifier.md`

3. **Verify Findings with `finding-verifier`**:
   - Dispatch a fresh verifier subagent via `invoke_subagent` with `.agents/review-prompts/finding-verifier.md` to confirm every `Security` and `Important` finding cites exact `file:line` (`line_start`–`line_end`) evidence and aligns with `REVIEW.md`.

4. **Record Receipt & Enforce Gate**:
   - Persist the verified receipt and check the security gate:
     ```bash
     python3 scripts/review-cycle.py record-receipt --scope staged --input <verified-payload.json>
     python3 scripts/review-cycle.py check-gate --scope staged
     ```
   - Do not set `"pipeline": "code-review"`. That field is reserved for `.agents/skills/code-review/SKILL.md`. This receipt does not satisfy the commit hook.
   - Report the standard `REVIEW.md` header (`X Important, Y Security, N Nit, M Pre-existing`).
