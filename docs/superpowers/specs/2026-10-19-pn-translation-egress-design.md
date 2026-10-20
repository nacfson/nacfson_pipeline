# Project PN translation egress

## Intent

GitHub issue #62. The `pn-backend` pod must be able to call the translation hosts by DNS name and receive the reply on that same connection. Every other public destination stays blocked.

This is the only part of #62 in scope. Identity and platform egress, the registry-proxy `443` rule, and a Hubble world-flow check are not this change.

## Why

`NetworkPolicy/project-pn-isolation` is default-deny egress. It allows kube-dns, `vault-db-proxy:5432`, `identity/gateway:8080`, and same-namespace `pn-backend:8080`. It cannot name a DNS host. A direct call to `api.wiktapi.dev` is dropped. Opening `0.0.0.0/0` would fix the call and would also allow every other public host. That is not the fix.

## What the owner stated

- Only the translation path leaves the cluster, and only from the backend.
- Required: `GET https://api.wiktapi.dev/v1/en/word/{lemma}/translations`. No API key. Allow `api.wiktapi.dev` on TCP 443.
- If `ENRICH_PRIMARY_API_KEY` is set, the backend also calls `POST https://generativelanguage.googleapis.com/v1beta/openai/chat/completions`. That host is `generativelanguage.googleapis.com`.
- `ENRICH_FALLBACK_BASE_URL` is empty. There is no third host.
- The app does not call these hosts. The backend does, on a cache miss or a retranslate.

## Planner calls

These were not confirmed. Reject the plan if one of them is wrong.

1. Allow both hosts now, including `generativelanguage.googleapis.com` while the key is unset. A network rule cannot see the secret. The app does not call Gemini without the key. Leaving the host out would make the first key rollout fail closed until a second policy change.
2. Do not deliver `ENRICH_PRIMARY_API_KEY`. Principle V forbids putting that credential in Git, a Kubernetes Secret, or a pod env var. Key delivery is a later Vault change.
3. Do not add an egress proxy. The backend calls the hosts directly.
4. Do not add `ipBlock: 0.0.0.0/0` or `toEntities: ["world"]`, including as a kube-router fallback. On a node Cilium does not yet own, translation stays dropped. That is the safe failure. The OpenBao RFC1918 fallback must not be copied here.
5. Keep `NetworkPolicy/project-pn-isolation` on `networking.k8s.io/v1`. Add a platform-owned `CiliumNetworkPolicy` beside it. Do not convert the portable policy to Cilium.
6. Put that object in `deploy/projects/pn/boundary/`, applied by the `project-boundaries` Flux layer (controller identity), not by `pn-reconciler`. Invariant 6 still forbids it in the project workload layer.
7. `project-boundaries` must `dependsOn` `cni` as well as `governance`, in both `local-k3s` and `vps-k3s`, so the Cilium CRD exists before the policy is applied.
8. The Cilium policy must allow DNS to kube-dns on port 53 with `matchPattern: "*"`. Cilium redirects DNS for any pod selected by `toFQDNs`. A DNS rule limited to the two public names would refuse `vault-db-proxy.vault.svc` and `gateway.identity.svc`. The `*` is only for lookups. Connections stay limited to the two names plus the existing in-cluster allows.
9. Do not close #62 in the implementation commits.

## Policy

Write this object to `deploy/projects/pn/boundary/translation-egress.yaml`. Do not add fields.

```yaml
apiVersion: cilium.io/v2
kind: CiliumNetworkPolicy
metadata:
  name: pn-backend-translation-egress
  namespace: proj-pn
spec:
  endpointSelector:
    matchLabels:
      app.kubernetes.io/name: pn-backend
  egress:
    - toEndpoints:
        - matchLabels:
            "k8s:io.kubernetes.pod.namespace": kube-system
            "k8s:k8s-app": kube-dns
      toPorts:
        - ports:
            - port: "53"
              protocol: ANY
          rules:
            dns:
              - matchPattern: "*"
    - toFQDNs:
        - matchName: api.wiktapi.dev
        - matchName: generativelanguage.googleapis.com
      toPorts:
        - ports:
            - port: "443"
              protocol: TCP
```

List it in `deploy/projects/pn/boundary/kustomization.yaml` `resources`.

## Allowed and denied

| From | To | Cilium owns the pod | kube-router still owns the pod |
|---|---|---|---|
| `pn-backend` | `api.wiktapi.dev:443` TCP | allowed | dropped |
| `pn-backend` | `generativelanguage.googleapis.com:443` TCP | allowed | dropped |
| `pn-backend` | any other public name, IP, or port | dropped | dropped |
| `pn-frontend` or any other pod | either translation host | dropped | dropped |
| `pn-backend` | kube-dns, vault-db-proxy, gateway | still allowed by `project-pn-isolation` | still allowed |
| internet | `pn-backend` | not opened by this policy | not opened |

The reply is the TCP return traffic on the connection the backend opened. This policy has no `ingress` section.

The network rule allows the host and port. It does not allow or deny the HTTP path. Path choice stays in the backend image.

## Non-goals

- No third host, no `matchPattern` on a public suffix, no UDP 443.
- No change to `identity` or `platform` NetworkPolicy.
- No narrowing of `registry-proxy-policy`.
- No load balancer in front of Traefik (#63).
- No constitution amendment. Principle II already requires explicit policy. Principle VI is not met for a cluster without the Cilium CRD; omitting this object there is future work, matching the existing OpenBao `CiliumNetworkPolicy` which also lives in the shared deploy path.
- No image rebuild and no `ENRICH_*` env var.

## Docs that must move with the policy

- `contracts/CONTRACT_ISOLATION_BUDGET.md` §2 names the two hosts and says they are not a portable `NetworkPolicy` rule.
- `ARCHITECTURE.md` §3.6 states the same limit: portable tenant policy stays, this one Cilium object names the hosts.
- `docs/platform-architecture.html` shows the two hosts on the boundary node, `project-boundaries` depending on `cni`, a forwarded Cilium scenario for the two hosts, and a drop for any other world destination. Flannel mode must show the translation scenario as dropped.
