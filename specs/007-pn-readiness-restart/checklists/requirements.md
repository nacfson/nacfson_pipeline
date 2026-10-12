# Specification Quality Checklist: 007-pn-readiness-restart

**Purpose**: Validate completeness and quality of the revised specification before / during implementation  
**Created**: 2026-10-12  
**Updated**: 2026-10-12 (browser API origin path)  
**Feature**: [spec.md](../spec.md)

## Content Quality

- [x] No implementation-only jargon without a user/operator-visible outcome
- [x] Cluster hop vs browser API path are both explained in plain language
- [x] Scope boundaries clear (no notification sinks; no image rebuild; no door/NP edits)
- [x] Requirements are testable via quickstart / static rg / CORS preflight
- [x] Success criteria are measurable (SC-001–SC-010)
- [x] Proven production failure mode (Ready pods + SPA connect error) is captured

## Requirement Completeness

- [x] FR-001–FR-013 cover probe/restart/governance
- [x] FR-014–FR-017 cover browser origin allowlist + validation + documentation warning
- [x] User Story 5 acceptance scenarios cover allowlist, preflight ACAO, and “Ready ≠ CORS”
- [x] Edge cases include kubelet inability to assert CORS headers
- [x] Assumptions updated so “access backend explicitly” means both paths
- [x] Contracts exist for probe matrix, disconnect signal, and browser-api-access
- [x] Plan and tasks include US5 and Flux env patches

## Consistency

- [x] Spec ↔ plan ↔ research R10/R11 agree: config+validation, not readiness ACAO probe
- [x] Constitution Principle VII draft mentions browser origin allowlist (after constitution edit in same change set)
- [x] Quickstart maps SC-008–SC-010

## Notes

- Items unchecked only if a later edit regresses them; re-run this checklist after material spec edits.
