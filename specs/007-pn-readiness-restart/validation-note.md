# Validation note (007 implement)

**Date**: 2026-10-12

## Static (quickstart §0) — PASS

- FE: `restartPolicy: Always`; `startupProbe` `/healthz` `failureThreshold: 36`; `readinessProbe` `/healthz` `failureThreshold: 3`; `livenessProbe` `/`
- BE: `restartPolicy: Always`; `readinessProbe` `/readyz`; `livenessProbe` `/healthz`
- `network-policy.yaml` / `ingress-route.yaml`: no feature diff
- No notification sinks added under `deploy/projects/pn` or `deploy/platform`

## Cluster (quickstart §1–§4) — NOT RUN

`kubectl` against the local API returned ServiceUnavailable. SC-001–SC-007 live scenarios remain to be executed when the cluster is healthy.

## Constitution

Principle VII / Amendment 2.1.0 present in working tree; maintainer approval still required before commit as binding governance.
