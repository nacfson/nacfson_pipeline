# Quickstart: Validate Platform-Owned TLS Termination

**Feature**: [spec.md](spec.md) · **Contracts**: [project-route-tls.md](contracts/project-route-tls.md), [certificate-inventory.md](contracts/certificate-inventory.md), [preflight-tls-rules.md](contracts/preflight-tls-rules.md)

Run these scenarios in order. Each lists its command and the expected result.

## Prerequisites

- Tools on PATH: `kubectl`, `flux`, `kubeconform` (optional locally), `python3` with `scripts/requirements-preflight.txt`, `openssl`, `curl`.
- For cluster checks: read-only operator kubeconfig (`scripts/operator-kubeconfig.sh`), or a shell on the VM using `sudo k3s kubectl`.

## 1. Offline: render and preflight (both environments)

```bash
for env in local-k3s vps-k3s; do
  CANDIDATE=$(./scripts/render-validate.sh "$env" | tail -n 1)   # also runs render-invariants
  python3 scripts/platform-preflight.py --environment "$env" \
    --candidate "$CANDIDATE" --capacity "clusters/$env/capacity.yaml"
done
```

**Expected**: render-validate succeeds, and preflight prints `passed` for both environments. The render contains:

```bash
grep -A3 'kind: TLSStore' "$CANDIDATE"          # name: default, namespace: platform
grep -B2 -A3 'name: pn-tls' "$CANDIDATE"        # Certificate in namespace platform, not proj-pn
grep -c 'allow-acme-http01-solver' "$CANDIDATE" # 0
grep 'serviceType: ClusterIP' "$CANDIDATE"      # present for both ACME issuers
```

## 2. Offline: preflight rejects project TLS misuse (FR-002, FR-003, FR-010)

```bash
python3 -m pytest tests/preflight -q
```

**Expected**: all tests pass, including one rejection per rule TLS-1 … TLS-6 ([preflight-tls-rules.md](contracts/preflight-tls-rules.md#fixtures)).

Manual negative check: in a scratch copy, change the PN route host to `shop.nacfson.cloud` and re-run step 1 for `vps-k3s`. Preflight must reject it with `hostname shop.nacfson.cloud has no platform certificate`.

## 3. Cluster: certificates are platform-owned and Ready (after PR A reconciles)

```bash
kubectl get tlsstore -A                                 # only platform/default
kubectl get certificate -A                              # platform/auth-tls, platform/pn-tls, READY=True
kubectl get certificate,certificaterequest,order,challenge -n proj-pn   # No resources found
kubectl get secret -n proj-pn --field-selector type=kubernetes.io/tls    # No resources found
kubectl get networkpolicy -n proj-pn                    # no allow-acme-http01-solver
kubectl describe resourcequota -n proj-pn               # services.nodeports used: 0
./scripts/verify-tls.sh                                 # exit 0; prints notAfter and renewalTime per hostname
```

If a stale `proj-pn` Challenge or Order from PR #26 remains, remove it once under break-glass and record it (operations §12):

```bash
sudo k3s kubectl --as=break-glass:nacfson --as-group=platform:break-glass \
  delete challenge,order -n proj-pn --all
```

## 4. Outside the cluster: trusted HTTPS and redirect (SC-001, SC-006)

```bash
for h in pn.nacfson.cloud auth.nacfson.cloud; do
  curl -sS -o /dev/null -w "$h https %{http_code}\n" "https://$h/"
  curl -sS -o /dev/null -w "$h http  %{http_code} -> %{redirect_url}\n" "http://$h/"
  echo | openssl s_client -connect "$h:443" -servername "$h" 2>/dev/null \
    | openssl x509 -noout -issuer -enddate
done
./scripts/verify-tls.sh --external
```

**Expected**:

- `https` returns a 2xx/3xx/401 status with curl exit code 0 (no `-k`).
- `http` returns 301/308 to `https://…`.
- The issuer is Let's Encrypt, not `TRAEFIK DEFAULT CERT`.

Raw IP check (edge case): `openssl s_client -connect 168.110.101.69:443` with no `-servername` returns Traefik's generated certificate, not `pn` or `auth`.

## 5. Native app smoke (SC-002)

Run the iOS app against `https://pn.nacfson.cloud`: sign in, and load one screen that calls `/api`. **Expected**: zero TLS handshake errors.

## 6. Migration safety (User Story 4, SC-007)

During the PR A and PR B reconciles, sample both hosts every 30 s from outside:

```bash
while true; do date +%T; for h in auth.nacfson.cloud pn.nacfson.cloud; do
  echo | openssl s_client -connect "$h:443" -servername "$h" 2>/dev/null | openssl x509 -noout -issuer; done; sleep 30; done
```

**Expected**: `auth` never shows `TRAEFIK DEFAULT CERT`. `pn` switches to Let's Encrypt within 15 minutes of PR A reconciling.

## 7. PR B checks

```bash
grep -n "request.namespace in \['platform', 'cert-manager'\]" deploy/platform/governance/manual-change-policy.yaml
kubectl get ingressroute -n platform public-auth-allowlist -o jsonpath='{.spec.tls}'   # {}
```

Then repeat steps 1, 3 and 4.
