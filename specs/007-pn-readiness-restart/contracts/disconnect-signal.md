# Contract: Frontend↔Backend Disconnect Signal

**Purpose**: Define the operator-visible signal that means “frontend is unconnected from backend,” reserved for future notification consumers.

## Signal definition

| Property | Value |
| --- | --- |
| Name | Frontend disconnect signal |
| Source workload | `pn-frontend` in namespace `proj-pn` |
| Condition | Pod (or Ready container) is **not Ready** because `readinessProbe` to FE `/healthz` failed after startup succeeded |
| Semantic meaning | Frontend cannot successfully complete the backend **process-availability** hop |
| Traffic effect | Pod removed from `pn-frontend` Service Endpoints |
| Restart effect | **None** from this signal alone |

## Observable surfaces (no push notify required)

| Surface | How an operator or future consumer reads it |
| --- | --- |
| Pod Ready condition | `Ready=False` with readiness probe failure |
| Endpoints | `pn-frontend` Endpoints empty or missing the pod IP |
| Events | Warning events such as `Unhealthy` / readiness probe failed |
| Flux (indirect) | `proj-pn` may stay or become not Ready while FE Deployment is unavailable |

## What this signal is not

| Non-signal | Why |
| --- | --- |
| FE liveness failure | That is process death → restart |
| FE startup failure before first success | Boot gate / restart after window — not a post-start disconnect notification event |
| BE `/readyz` failure alone | Backend↔DB problem; FE may still be “connected” to BE `/healthz` |
| Traefik `/api` errors alone | Door path to BE; does not replace FE readiness |

## Future notification consumer requirements (deferred)

When a later feature adds notifications, it MUST:

1. Consume this Ready/Endpoints/Event signal (or an aggregator of it).
2. **Not** invent a second continuous FE→BE probe type for the same hop.
3. Apply its own soak/dedup policy (007 does not define paging thresholds beyond probe `failureThreshold`).
4. Keep delivery channels private/operator-facing; MUST NOT expose Keycloak or platform-admin health publicly.

## Acceptance checks for 007

- After FE has been Ready once, inducing FE→BE process-availability failure yields FE NotReady without a readiness-driven restart storm.
- An operator can conclude “unconnected” from Pod Ready / Endpoints / Events alone ([quickstart.md](../quickstart.md)).
- No Alertmanager, webhook, or chat integration ships in 007.
