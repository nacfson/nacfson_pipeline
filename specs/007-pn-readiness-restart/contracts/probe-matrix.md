# Contract: Project PN Probe Matrix

**Scope**: `deploy/projects/pn/workloads/frontend-deployment.yaml`, `backend-deployment.yaml`

**Non-goals**: Image rebuilds, NetworkPolicy changes, IngressRoute changes, notification sinks.

## Matrix

| Workload | Role | Mechanism | Path | Port | Pass means | Fail means |
| --- | --- | --- | --- | --- | --- | --- |
| `pn-frontend` | startup | `httpGet` | `/healthz` | `80` | FE reached BE process-availability | In-window: keep Starting; **threshold exhausted: kill + restart pod/container** |
| `pn-frontend` | readiness | `httpGet` | `/healthz` | `80` | Connected FE→BE (disconnect signal clear) | NotReady = unconnected; **process NOT restarted** |
| `pn-frontend` | liveness | `httpGet` | `/` | `80` | Local site process up | Kill + restart |
| `pn-backend` | readiness | `httpGet` | `/readyz` | `8080` | Data dependency reachable | NotReady for API; **process NOT restarted** |
| `pn-backend` | liveness | `httpGet` | `/healthz` | `8080` | Backend process up | Kill + restart |

## Timing (declared defaults)

| Probe | `periodSeconds` | `timeoutSeconds` | `failureThreshold` | Notes |
| --- | --- | --- | --- | --- |
| FE startup | `5` | `3` | `36` | ≈180s boot window |
| FE readiness | `5` | `3` | `3` | Disconnect visible ~15s after sustained fail |
| FE liveness | `10` | default/`3` | default/`3` | Process-local |
| BE readiness | `5` | `3` | `3` | Dependency readiness |
| BE liveness | `10` | default/`3` | default/`3` | Process-local |

## Restart policy

```yaml
spec:
  template:
    spec:
      restartPolicy: Always
```

Required on both frontend and backend Pod templates.

## Invariants

1. FE startup and readiness MUST target the same process-availability hop (`/healthz` on FE port 80).
2. FE liveness MUST NOT target `/healthz` or `/readyz`.
3. BE readiness MUST be `/readyz`, not `/healthz`.
4. Probes MUST remain pod-local kubelet checks (not public door requirements).
5. Candidates that add NodePort/LoadBalancer, new egress peers, or privileged probe sidecars are rejected.

## Acceptance checks

- Rendered/frontend Deployment YAML contains `startupProbe.httpGet.path: /healthz` and `readinessProbe.httpGet.path: /healthz` and `livenessProbe.httpGet.path: /`.
- Backend Deployment YAML contains `readinessProbe.httpGet.path: /readyz` and `livenessProbe.httpGet.path: /healthz`.
- Both declare `restartPolicy: Always`.
- `ingress-route.yaml` and `network-policy.yaml` are unchanged by this feature.
