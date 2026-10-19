---
name: explain-slice
description: Break down a diff or pull request into logical, dependency-ordered review slices with risk annotations and verification commands.
compatibility: Works in Antigravity (agy) and compatible multi-agent harnesses.
---

## User Input
```text
$ARGUMENTS
```

## Purpose
Organize a multi-file diff into a logical reading order (Contracts & Policy → Core Go/Python Logic → Kubernetes Manifests & Flux Layers → Tests & Architecture Handbook) so human and agent reviewers can audit changes without cognitive overload.

## Step-by-Step Workflow

1. **Inspect Risk & Changed Files**:
   - Run `python3 scripts/review-cycle.py classify-risk --scope worktree` (or `staged` / `branch`) to identify the effective risk tier (`Low`, `Medium`, `High`).
2. **Group Files into Dependency-Ordered Slices**:
   - **Slice 1 — Contracts, Specs & Policy**: `contracts/`, `specs/`, `.specify/memory/constitution.md`, `REVIEW.md`, `config/`.
   - **Slice 2 — Runtime & Automation Logic**: `gateway/`, `services/vault-proxies/`, `scripts/`.
   - **Slice 3 — Declarative Cluster & Project Manifests**: `deploy/`, `clusters/`, `bootstrap/`.
   - **Slice 4 — Verification & Architecture Handbook**: `tests/`, `docs/platform-architecture.html`.
3. **Output Structured Walkthrough**:
   - For each slice, state the exact files, the behavioral invariant being preserved or changed, the failure modes from `REVIEW.md` to watch for, and the exact verification command to run.
