# Data Model: 007-pn-readiness-restart

This feature has no persistent business database. Entities are **lifecycle signals** and **workload declarations**.

## Entities

### FrontendStartupSignal

| Field | Description |
| --- | --- |
| Source | `pn-frontend` container startup check |
| Target | Backend process-availability via FE `/healthz` → BE `/healthz` |
| Cardinality | Once per container start (until success or startup window exhausted) |
| Success | Startup succeeds; liveness/readiness evaluation may proceed |
| Failure within window | Startup not yet successful; container not fully started |
| Failure after window | Container restart under always-restart policy |

### FrontendDisconnectSignal (Readiness)

| Field | Description |
| --- | --- |
| Source | `pn-frontend` container readiness check |
| Target | Same FE→BE process-availability hop as startup |
| Cardinality | Continuous after startup success |
| Ready | FE receives new Service traffic; connected |
| NotReady | Unconnected; Endpoints omit pod; durable hook for future notifications |
| Must not | Restart the container solely for NotReady after startup |

### BackendReadinessSignal

| Field | Description |
| --- | --- |
| Source | `pn-backend` readiness check |
| Target | BE `/readyz` (data-store ping) |
| Ready | API Service may send traffic |
| NotReady | Process may still be alive; API traffic withheld |

### AliveSignal

| Field | Workload | Target |
| --- | --- | --- |
| Frontend | `pn-frontend` | Local `/` (site content) |
| Backend | `pn-backend` | Local `/healthz` (process up) |

Independent of peer/DB reachability after startup.

### RestartPolicyDeclaration

| Field | Value |
| --- | --- |
| Workloads | `pn-frontend`, `pn-backend` |
| Policy | Always restart |
| Trigger | Process exit / failed liveness / failed startup window — **not** readiness-only |

### FutureNotificationConsumer (out of scope)

| Field | Description |
| --- | --- |
| Input | FrontendDisconnectSignal (Ready → NotReady / sustained NotReady) |
| Output | Push/chat/email — **not built in 007** |

## State transitions

### Frontend container lifecycle

```text
[Created]
    |
    v
[Starting] --startup /healthz fail--> stay Starting (until window)
    |
    | startup /healthz success
    v
[Running]
    |-- readiness /healthz success --> Ready (in Endpoints)
    |-- readiness /healthz fail ----> NotReady (disconnect signal)
    |-- liveness / fail ------------> Restart (Always)
    |-- startup window exhausted --> Restart (Always)
```

### Backend container lifecycle

```text
[Running process]
    |-- liveness /healthz fail --> Restart (Always)
    |-- readiness /readyz ok ----> Ready for API Service
    |-- readiness /readyz fail --> NotReady (DB/dependency)
```

## Validation rules

1. FE startup and readiness MUST share the backend **process-availability** hop, not DB readiness.
2. FE liveness MUST NOT use the backend hop.
3. BE readiness MUST NOT equal BE liveness path after this feature.
4. `restartPolicy` MUST be explicitly `Always` on both Deployments.
5. NetworkPolicy MUST still allow only the existing FE→BE path used by probes.
