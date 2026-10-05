# Contract: Multi-Architecture CI Image Publishing

**Repository**: `nacfson/nacfson_pipeline`  
**CI Engine**: GitHub Actions (`.github/workflows/build-images.yaml`)  
**Target Registry**: GitHub Container Registry (`ghcr.io/nacfson/*`)  

---

## 1. Scope of In-House Images

| Image Target | Source Code Path | Context | Dockerfile |
|---|---|---|---|
| `ghcr.io/nacfson/vault-db-proxy` | `services/vault-proxies/` | `.` | `services/vault-proxies/Dockerfile.db-proxy` |
| `ghcr.io/nacfson/vault-registry-proxy` | `services/vault-proxies/` | `.` | `services/vault-proxies/Dockerfile.registry-proxy` |
| `ghcr.io/nacfson/platform-auth-gateway` | `gateway/` | `gateway/` | `gateway/Dockerfile` |

---

## 2. GitHub Actions Workflow Contract

1. **Trigger Condition**:
   - Push to `main` modifying `services/vault-proxies/**`, `gateway/**`, or the workflow itself.
   - Manual execution via `workflow_dispatch`.

2. **Builder Specifications**:
   - **Platforms**: `linux/amd64`, `linux/arm64` (aarch64 required for Oracle Cloud VPS).
   - **Base Images**: Minimal distroless non-root runtime (`gcr.io/distroless/static:nonroot`).
   - **Authentication**: Native `secrets.GITHUB_TOKEN` with permissions `packages: write` and `contents: read`.

3. **Tagging & Digest Contract**:
   - Every build tags:
     - `latest`
     - `${{ github.sha }}`
   - Manifest digest is output and logged for immutable referencing per Constitution Principle I.
