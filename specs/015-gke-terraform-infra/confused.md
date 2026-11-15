# Spec 015: confusing points, explained

Plain-language notes from the Spec 015 code review: what the problem was, how it was fixed,
and where the fix lives. One picture is used throughout:

- **Node** = an apartment building, with its own street address (public IP).
- **Pod** = one apartment inside the building. Traefik is the apartment that greets web visitors.
- **Load balancer** = a fixed front gate in front of the buildings, with a permanent address.
- **Firewall** = a security guard who turns away anyone not on the guest list.

## Summary

| # | Problem | Fix | Where |
|---|---|---|---|
| 2 | DNS pointed at a node's own IP; GKE replaces nodes, the IP changes, the site goes down | One load balancer with a reserved IP that never changes | `deploy/platform/traefik/service.yaml`, `infra/gcp/gke/ingress.tf` |
| 3 | GKE wrote the "let web traffic in" firewall rule itself, outside Git | GKE's rule is turned off; Terraform owns the only web rule | `infra/gcp/gke/cluster.tf`, `infra/gcp/gke/ingress.tf` |
| 3b | A load balancer also opens ports 30000–32767 on every node | No change needed: no firewall rule lets anyone reach them | — |
| 5 | `hostNetwork: true` on Traefik would skip the load balancer, and CI did not catch it | CI now rejects `hostNetwork` on Traefik | `scripts/render-invariants.py` (Invariant 10) |
| 6 | Check 4 would fail CI as soon as a second app got its own Google account | Check 4 now looks only at OpenBao's account | `scripts/check-gcp-shared-values.py` |

## Decision: keep the load balancer

The load balancer costs about $18/month. Removing it means Traefik answers on the node's own IP again,
which changes whenever GKE replaces the node. The alternatives were:

| Option | Keeps the address stable by | Cost of the option |
|---|---|---|
| Move the reserved IP onto each new node (KubeIP add-on) | Re-attaching the IP after each node replacement | One more add-on with Google permissions; node upgrades must delete the old node first |
| Rewrite DNS automatically (external-dns) | Updating DNS to the new node's IP | DNS provider token; outage while visitors' DNS caches expire |
| Rewrite DNS by hand | You, after every node replacement | Frequent outages (GKE upgrades nodes automatically) |
| **Keep the load balancer (chosen)** | The address belongs to the load balancer, not the node | About $18/month |

With one node and one Traefik copy, the load balancer does not remove the short outage during a node
replacement; it only keeps the address the same. It was kept because it has the fewest moving parts.

## 2. `hostPort` and the changing address

**Problem.** The old design (Spec 012) used `hostPort: 80/443`: a mail slot from the building's front
wall straight into the Traefik apartment. Visitors used the building's address. When GKE replaced the
building, the address changed and DNS pointed at nothing.

**Fix.** Visitors now use the front gate (`Service/platform/traefik`, `type: LoadBalancer`). Its address
is reserved by Terraform (`google_compute_address.ingress`) and never changes. `hostPort` was removed from
`deploy/platform/traefik/deployment.yaml`.

Other Service types do not help: `ClusterIP` is only reachable inside the cluster, and `NodePort` still
uses the building's address.

## 3. Why the load balancer needs a firewall rule

"Ingress" in a firewall rule means *incoming traffic*. It is not the Kubernetes Ingress object.

**Why a rule is needed at all.**

1. Every Google Cloud VM starts with a hidden "block all incoming" rule. Nothing gets in without an allow rule.
2. Our load balancer is a *passthrough* type. It does not open or inspect the connection; it hands the
   visitor's packets to a node unchanged, still marked "from: the visitor, to: the gate address". So the
   node's guard sees them as strangers and needs a rule to let them in.

```
visitor ──► front gate (LB IP) ──passes through──► guard at the node ──► Traefik
                                                   rule: allow TCP 80/443,
                                                   from anyone, to the LB IP only
```

**Problem.** GKE used to write that rule itself, out of sight and outside Git.

**Fix.**

- `disable_l4_lb_firewall_reconciliation = true` on the cluster turns GKE's automatic rule off.
- Terraform creates `nacfson-gke-allow-web` (`infra/gcp/gke/ingress.tf`): TCP 80/443, from anyone,
  **to the load balancer's IP only**, on the node VMs only.
- GKE still writes one rule, for Google's health checkers ("is Traefik alive?"). Google does not allow
  turning that one off.

### 3b. The extra node ports

A `LoadBalancer` Service also opens a random port between 30000 and 32767 on every node, as a back door
to the same pod. Nobody can use it: the only web rule allows traffic to the load balancer's IP, not to
the nodes' own IPs. `allocateLoadBalancerNodePorts: false` would remove the back door entirely, but Google
does not document support for it with this load balancer type, so it is left alone.

## 5. `hostNetwork`

**Problem.** `hostNetwork: true` is a bigger version of `hostPort`: the Traefik apartment knocks down its
walls and becomes the building's lobby. Traefik would answer on the building's own address and skip the
front gate. The CI check caught `hostPort` but not `hostNetwork`.

**Fix.** Invariant 10 in `scripts/render-invariants.py` now also fails when Traefik has `hostNetwork: true`.

| Traefik setting | Before | After |
|---|---|---|
| `hostPort: 443` | CI fails | CI fails |
| `hostNetwork: true` | CI passes (mistake slips through) | CI fails |

Test: `test_host_network_rejected` in `tests/preflight/test_render_invariants_gke_edge.py`.

## 6. Check 4

### Why OpenBao talks to Google

OpenBao is the safe that holds every password (databases, Keycloak, projects). Each time OpenBao restarts,
the safe is **locked**, and every app that needs a password fails until it is unlocked.

On GKE, the unlock key is kept **at Google** (Google's key service, KMS). On every restart OpenBao asks
Google: "please unlock me".

### Google only answers someone on its list

Google first asks "who are you?" and checks who is allowed to use the key. Two things must therefore be
written down, in two different places:

| | Where | What it says |
|---|---|---|
| ① Google's guest list | Terraform, `infra/gcp/gke/kms.tf` | "Account **`openbao-kms`** may use the unlock key." |
| ② OpenBao's name tag | Kubernetes, `clusters/gke/layers.yaml` | "OpenBao introduces itself to Google as **`openbao-kms`**." |

OpenBao gets in only if the name tag ② matches a name on the list ①. The name-tag mechanism is what
Google calls **Workload Identity**: OpenBao proves who it is without a password file stored in the cluster.

### What goes wrong if they differ

```
OpenBao restarts → "unlock me, I'm openbao-ksm"   (typo)
Google checks its list → no such name → refused
OpenBao stays locked → every app that needs a password fails
```

You would find out only after deploying to GKE.

### What check 4 does

`scripts/check-gcp-shared-values.py` runs in CI on every PR and makes 11 comparisons between Terraform
and the Kubernetes files. Check 4 is the 4th:

```
name Terraform will create  → openbao-kms   (infra/gcp/shared-values.json)
OpenBao's name tag          → openbao-kms   (clusters/gke/layers.yaml)
same? yes → PR passes;  no → PR fails
```

### The problem and the fix

**Problem.** Check 4 read **every** name tag in `layers.yaml` and required each one to say `openbao-kms`.
If cert-manager later got its own Google account (`cert-manager-dns`), CI would fail even though nothing
was wrong.

**Fix.** Check 4 now reads only the name tag on OpenBao's own account (`vault/openbao`, from `kms.ksa`
in `shared-values.json`).

| Situation | Before | After |
|---|---|---|
| OpenBao's tag has a typo | caught | caught |
| OpenBao's tag is missing | caught | caught |
| cert-manager has its own, correct tag | false alarm | passes |

Tests: `test_check_4_gsa_annotation`, `test_check_4_missing_openbao_annotation` and
`test_check_4_other_workload_identity_users_are_ignored` in `tests/infra/test_check_gcp_shared_values.py`.

## Sources

- [GKE: user-managed firewall rules for LoadBalancer Services](https://docs.cloud.google.com/kubernetes-engine/docs/how-to/user-managed-firewall-rules)
- [GKE: automatically created firewall rules](https://docs.cloud.google.com/kubernetes-engine/docs/concepts/firewall-rules)
- [GKE: LoadBalancer Service parameters](https://docs.cloud.google.com/kubernetes-engine/docs/concepts/service-load-balancer-parameters)
- [GKE: backend service-based external load balancer](https://docs.cloud.google.com/kubernetes-engine/docs/how-to/backend-service-based-external-load-balancer)
- [Google Cloud: VPC firewall rules](https://cloud.google.com/firewall/docs/firewalls)
- [GKE: Workload Identity Federation](https://cloud.google.com/kubernetes-engine/docs/concepts/workload-identity)
