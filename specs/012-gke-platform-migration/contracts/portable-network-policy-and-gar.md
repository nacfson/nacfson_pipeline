# Contract: Cloud-Agnostic `NetworkPolicy` & Public Google Artifact Registry (`GAR`)

**Feature**: [`specs/012-gke-platform-migration/spec.md`](../spec.md)
**Enforced By**: `scripts/platform-preflight.py`, `scripts/render-invariants.py`, `scripts/image-bump-request.py`, `.github/workflows/image-bump-request.yaml`

---

## 1. Portable `networking.k8s.io/v1` `NetworkPolicy` Contract (`FR-005`)

Zero `cilium.io/v2` `CiliumNetworkPolicy` manifests remain in `deploy/` or `clusters/{local-k3s,gke}/`. Both environments enforce identical standard `networking.k8s.io/v1` `NetworkPolicy` boundaries:

### 1.1 Scoped Public HTTPS (`:443`) Egress (`pn-backend`, `vault-ai-proxy`, `vault-notify-proxy`)

Each of the three external-API caller policies (`proj-pn/pn-backend-translation-egress`, `vault/ai-proxy-policy`, `vault/notify-proxy-policy`) MUST satisfy all 4 preflight assertions:
1. `spec.podSelector.matchLabels["app.kubernetes.io/name"]` matches the exact single caller (`pn-backend`, `vault-ai-proxy`, or `vault-notify-proxy`) — never `{}`.
2. Public egress rule opens **only** `protocol: TCP`, `port: 443`.
3. `to[].ipBlock` declares `cidr: 0.0.0.0/0` with an `except` list containing **at minimum**:
   - `10.0.0.0/8`
   - `172.16.0.0/12`
   - `192.168.0.0/16`
   - `169.254.169.254/32` (blocks GCE/cloud instance metadata & Workload Identity token server from tenant/proxy pods)
4. DNS egress (`UDP/TCP 53`) is restricted to `kube-system` / `k8s-app: kube-dns`.

### 1.2 `openbao` API Server & GKE Cloud KMS Egress (`vault/openbao-ingress-policy`)

1. Base `deploy/platform/vault/network-policy.yaml` permits `openbao` egress only to `openbao:8201` (Raft) and RFC1918 `:443/:6443` (`kube-apiserver`, including the internal `kubernetes.default.svc` ClusterIP); `deploy/platform/cni/` (`local-k3s`) sets `policy-cidr-match-mode: "nodes"` in `ConfigMap/cilium-config` so standard `ipBlock` matches the K3s single-node API server IP.
2. `clusters/gke/layers.yaml` patches `openbao-ingress-policy` (and only `openbao-ingress-policy`) to add:
   - `169.254.169.254/32` on TCP `:80` and `:988` (for GKE Workload Identity `gke-metadata-server` token exchange by `ServiceAccount/openbao`).
   - `199.36.153.8/30` (`private.googleapis.com`) on TCP `:443` (for `cloudkms.googleapis.com` auto-unseal via Private Google Access, with zero `0.0.0.0/0` internet egress on `openbao`).

---

## 2. Cloud Build + Public Google Artifact Registry (`GAR`) Contract (`FR-004`)

1. **Canonical Image Reference Format**:
   - First-party platform and hosted project images target GCP project `nacfson-pipeline` in `asia-northeast3` (Seoul):
     `asia-northeast3-docker.pkg.dev/nacfson-pipeline/nacfson-public/<name>@sha256:<64-hex>`
     (during transition prior to Gate 4 image rebuilds, `platform-preflight.py` accepts immutable `@sha256:<64-hex>` digests matching either `ghcr.io/nacfson/` or `[a-z0-9-]+-docker.pkg.dev/nacfson-pipeline/nacfson-public/`, keeping `asia-northeast1` Tokyo drop-in compatible while rejecting mutable tags without `@sha256:` on hosted project workloads).
2. **Zero Registry Credentials (`vault-registry-proxy` Retirement)**:
   - `deploy/platform/vault-proxies/` removes `registry-proxy.yaml` (`Deployment/vault-registry-proxy`, `Service/vault-registry-proxy`) and its `kustomization.yaml` entry.
   - `deploy/platform/vault/` removes `NetworkPolicy/registry-proxy-policy`, `ServiceAccount/vault-registry-proxy` (`service-accounts.yaml`), `policies/registry-proxy.hcl`, and `registries.yaml.template`.
   - `scripts/vault-configure-k8s-auth.sh` removes role `vault-registry-proxy`.
   - Zero `imagePullSecrets` exist in any namespace across `local-k3s` and `gke`.
3. **GAR Free-Tier Storage Guardrail (`< 0.5 GB/mo`)**:
   - Declarative GAR repository policy (`deploy/gcp/gar-cleanup-policy.json`) keeps the latest `2` versions per image package and deletes unreferenced/older digests older than `7d`.
4. **Image-Bump Dispatch Compatibility**:
   - `scripts/image-bump-request.py`, `.github/workflows/image-bump-request.yaml`, and `.agents/skills/update-project-images/SKILL.md` accept `<region>-docker.pkg.dev/nacfson-pipeline/...` package digests and Cloud Build provenance links alongside GitHub commit references.
