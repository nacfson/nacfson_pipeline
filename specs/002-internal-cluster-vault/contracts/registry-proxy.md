# Contract: Registry Proxy Integration

**Service**: `vault-registry-proxy`  
**Endpoint**: `http://127.0.0.1:5000` (Node Host Loopback Listener)  
**Consumer**: K3s Node `containerd` runtime  
**Upstream**: `https://ghcr.io`  

---

## 1. Request Contract (Node ➔ Proxy)

The node's container runtime requests image manifests and layer blobs without sending upstream credentials.

### 1.1 Caller Verification & Network Isolation
* **Listener Boundary**: The proxy listens exclusively on host loopback (`127.0.0.1:5000`).
* **Caller Enforcement**: Under Kubernetes Restricted Pod Security Profile, application pods are strictly forbidden from setting `hostNetwork: true`. Therefore, only host-network processes (specifically K3s `containerd`) can route traffic to `127.0.0.1:5000`.
* **Prohibited**: The node request MUST NOT contain an `Authorization` header.

### 1.2 Manifest Pull
* **Method**: `GET /v2/{organization}/{repository}/manifests/{digest}`
* **Headers**:
  * `Host: ghcr.io` (or loopback host header)
  * `Accept: application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.v2+json, application/vnd.oci.image.index.v1+json`
  * `User-Agent: containerd/{version}`
* **Constraint**: Must reference an immutable digest (`sha256:...`) matching registered deployment manifests.

### 1.3 Blob Pull
* **Method**: `GET /v2/{organization}/{repository}/blobs/{digest}`
* **Headers**:
  * `Accept: application/octet-stream`

---

## 2. Upstream Request & Token-Exchange Contract (Proxy ➔ ghcr.io)

The proxy executes the Docker Registry v2 / OCI Token Authentication Specification internally within trusted execution.

```text
[Proxy] ──1. GET /v2/.../manifests/sha256:...──> [ghcr.io]
[Proxy] <──2. 401 Unauthorized (Www-Authenticate: Bearer realm="https://ghcr.io/token", scope="repository:...")── [ghcr.io]
[Proxy] ──3. GET https://ghcr.io/token?scope=... (Authorization: Basic <PAT>)──> [ghcr.io/token]
[Proxy] <──4. 200 OK ({"token": "temporary_bearer_token", "expires_in": 300})── [ghcr.io/token]
[Proxy] ──5. GET /v2/.../manifests/sha256:... (Authorization: Bearer <temporary_bearer_token>)──> [ghcr.io]
[Proxy] <──6. 200 OK (OCI Manifest Payload)── [ghcr.io]
```

### 2.1 Token Exchange Flow
1. **Initial Probe / Request**: Proxy initiates the request to `https://ghcr.io/v2/...`.
2. **Challenge Detection**: GitHub responds with `401 Unauthorized` containing:
   ```http
   Www-Authenticate: Bearer realm="https://ghcr.io/token",service="ghcr.io",scope="repository:{org}/{repo}:pull"
   ```
3. **Token Acquisition**: The proxy parses the `realm`, `service`, and `scope`, retrieves the active `ghcr-pull-token` (PAT) from OpenBao, and requests an ephemeral bearer token:
   * **Endpoint**: `https://ghcr.io/token?service=ghcr.io&scope=repository:{org}/{repo}:pull`
   * **Header**: `Authorization: Basic {base64(token_user:active_pat)}`
4. **Cached Ephemeral Bearer**: GitHub issues a short-lived bearer token (valid 5 minutes). The proxy caches this token in memory for sub-requests during the pull session.
5. **Authenticated Request**: The proxy retries the original request with `Authorization: Bearer {ephemeral_bearer_token}`.

### 2.2 Controlled Blob Redirects (HTTP 307)
* When downloading layer blobs (`/v2/.../blobs/...`), GitHub issues an `HTTP 307 Temporary Redirect` to GitHub's storage servers (e.g. `github-cloud.s3.amazonaws.com`).
* The proxy follows this redirect to stream binary layers.
* **Security Guard**: The proxy automatically strips the `Authorization` header when following redirects to external storage domains to avoid leaking tokens.

---

## 3. Response Contract (Proxy ➔ Node)

The proxy streams the image layers to containerd while stripping all upstream auth headers.

* **Status**: `200 OK`
* **Headers Preserved**:
  * `Content-Type`: Matching OCI or Docker manifest/blob types.
  * `Docker-Content-Digest`: Verifiable SHA-256 digest.
  * `Content-Length`: Size of binary layer.
* **Headers Stripped (Security Invariant)**:
  * Any `Www-Authenticate`, `Set-Cookie`, `X-GitHub-*`, or bearer token redirects are strictly stripped.
* **Body**: Raw binary OCI layer stream.
