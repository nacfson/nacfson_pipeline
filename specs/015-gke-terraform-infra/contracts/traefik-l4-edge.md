# Contract: GKE edge ingress through a single L4 pass-through load balancer

**Replaces**: `specs/012-gke-platform-migration/contracts/gke-capacity-and-placement.md` §3 ("Zero-LoadBalancer Edge Ingress Contract"). That section is rewritten to point here (FR-022).
**Covers**: spec FR-007, FR-007a, and the clarification of 2026-11-14 (load balancer).

## 1. `Service/traefik` (namespace `platform`, GKE only)

```yaml
apiVersion: v1
kind: Service
metadata:
  name: traefik
  namespace: platform
  annotations:
    networking.gke.io/load-balancer-ip-addresses: gke-ingress-ip   # reserved by infra/gcp/gke (name, not IP)
spec:
  type: LoadBalancer
  loadBalancerClass: networking.gke.io/l4-regional-external        # backend-service-based pass-through NLB
  externalTrafficPolicy: Local                                     # keep client IPs; only nodes with Traefik pass health checks
  ports:
    - {name: web, port: 80, targetPort: 8000, protocol: TCP}
    - {name: websecure, port: 443, targetPort: 8443, protocol: TCP}
  selector:
    app.kubernetes.io/name: traefik
```

`deploy/platform/traefik/` is deployed only by `clusters/gke/`. The change goes directly in the base files, and local-k3s and vps-k3s are unaffected.

## 2. `Deployment/traefik`

- **No `hostPort`.** It is removed from `web` and `websecure`.
- **No `hostNetwork`.** Traefik must not share the node's network (it would answer on the node IP and bypass the load balancer).
- **`nodeSelector` unchanged.** It stays `cloud.google.com/gke-provisioning: standard`, so Traefik never runs on Spot.
- **`strategy: Recreate` kept.** Rollouts never surge past the 1/n reservation.
- **Unchanged:** `/ping` on `:8082` and the readiness and liveness probes.

## 3. Render invariant (replaces Invariant 10 in `scripts/render-invariants.py`)

On `gke` the render MUST satisfy all of the following:

1. Exactly one `Service` of `type: LoadBalancer`, which is `platform/traefik`.
2. That Service has `loadBalancerClass: networking.gke.io/l4-regional-external`, `externalTrafficPolicy: Local`, and a non-empty `networking.gke.io/load-balancer-ip-addresses` annotation.
3. `Deployment/platform/traefik` declares no `hostPort` and does not set `hostNetwork: true`.
4. No `Service` of `type: NodePort` exists anywhere.

## 4. Preflight (`scripts/platform-preflight.py`)

- **Rule 2 (gke):** reject every `LoadBalancer` Service except one matching §1 exactly (namespace, name, class, policy, annotation present).
- **Project namespaces:** unchanged. `NodePort` and `LoadBalancer` stay rejected (Constitution, Controlled Ingress Bypass Prevention).
- **`test_30_*`:** updated. New cases cover:
  - the allowed Traefik shape passes
  - a project `LoadBalancer` is rejected
  - a second platform `LoadBalancer` is rejected
  - the Traefik Service with the wrong class, `Cluster` policy or a missing annotation is rejected

## 5. Behavior guarantees

| Situation | Expected result |
|---|---|
| Normal operation | `kubectl get svc -n platform traefik` shows `EXTERNAL-IP` = `terraform output ingress_ip` |
| `ondemand-core` replaced | The health check fails on the old node and passes on the new one once Traefik is ready. Downtime is the pod restart time, and the IP is unchanged. |
| `spot-controllers` node | Never healthy for this load balancer (no Traefik pod), so it never receives web traffic |
| TLS | Terminated by Traefik with platform-owned certificates. Google never decrypts. |
| Client IP | Traefik sees the visitor's real source IP |
| Service deleted | GKE removes the forwarding rule, backend service, health check and health-check firewall rule. The reserved address and the web firewall rule `nacfson-gke-allow-web` stay (Terraform owns them). |
| Firewall | Terraform owns the only web opening (TCP 80/443 to the reserved address, node VMs only). GKE's automatic load balancer firewall rule is off (`disable_l4_lb_firewall_reconciliation`). Node IPs and node ports are not opened. |
