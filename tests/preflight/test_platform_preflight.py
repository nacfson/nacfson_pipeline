#!/usr/bin/env python3
"""
Unit tests for platform-preflight (tests/preflight/test_platform_preflight.py).
Tests all 10 required scenarios from contracts/platform-preflight.md §6.
"""

import json
import os
import subprocess
import sys
import unittest

BASE_DIR = os.path.abspath(os.path.join(os.path.dirname(__file__), "..", ".."))
SCRIPT = os.path.join(BASE_DIR, "scripts", "platform-preflight.py")
FIXTURES = os.path.join(BASE_DIR, "tests", "preflight", "fixtures")
if os.path.join(BASE_DIR, "scripts") not in sys.path:
    sys.path.insert(0, os.path.join(BASE_DIR, "scripts"))
from kustomize_render import kustomize_build


class TestPlatformPreflight(unittest.TestCase):

    def run_preflight(self, candidate, capacity, baseline=None, env="test-env", extra_args=None):
        cmd = [
            sys.executable, SCRIPT,
            "--environment", env,
            "--candidate", os.path.join(FIXTURES, candidate),
            "--capacity", os.path.join(FIXTURES, capacity),
            "--json"
        ]
        if baseline:
            cmd.extend(["--baseline", os.path.join(FIXTURES, baseline)])
        if extra_args:
            cmd.extend(extra_args)
        res = subprocess.run(cmd, capture_output=True, text=True)
        try:
            data = json.loads(res.stdout) if res.stdout.strip() else {}
        except Exception:
            data = {}
        return res.returncode, data, res.stderr

    def test_01_valid_render_passed(self):
        """Fixture 1: Valid render passes with exit code 0"""
        rc, data, _ = self.run_preflight("valid_render.yaml", "valid_capacity.yaml")
        self.assertEqual(rc, 0)
        self.assertEqual(data.get("validationStatus"), "PASSED")

    def test_02_zero_projects_passed(self):
        """Fixture 2: Zero project layers passes without divide by zero"""
        rc, data, _ = self.run_preflight("zero_projects.yaml", "valid_capacity.yaml")
        self.assertEqual(rc, 0)
        self.assertEqual(data.get("validationStatus"), "PASSED")
        self.assertEqual(data.get("projectCount"), 0)

    def test_03_missing_or_zero_capacity_rejected(self):
        """Fixture 3: Missing or zero capacity rejects with REJECTED_NO_MEASUREMENT (exit 1)"""
        rc, data, _ = self.run_preflight("valid_render.yaml", "nonexistent_capacity.yaml")
        self.assertEqual(rc, 1)
        self.assertEqual(data.get("validationStatus"), "REJECTED_NO_MEASUREMENT")

        rc, data, _ = self.run_preflight("valid_render.yaml", "zero_capacity.yaml")
        self.assertEqual(rc, 1)
        self.assertEqual(data.get("validationStatus"), "REJECTED_NO_MEASUREMENT")

    def test_04_project_service_nodeport_rejected(self):
        """Fixture 4: Project Service NodePort rejects with REJECTED_ISOLATION (exit 1)"""
        rc, data, _ = self.run_preflight("isolation_nodeport.yaml", "valid_capacity.yaml")
        self.assertEqual(rc, 1)
        self.assertEqual(data.get("validationStatus"), "REJECTED_ISOLATION")

    def test_05_project_pod_hostpath_rejected(self):
        """Fixture 5: Project pod with hostPath rejects with REJECTED_ISOLATION (exit 1)"""
        rc, data, _ = self.run_preflight("isolation_hostpath.yaml", "valid_capacity.yaml")
        self.assertEqual(rc, 1)
        self.assertEqual(data.get("validationStatus"), "REJECTED_ISOLATION")

    def test_06_project_qos_mismatch_rejected(self):
        """Fixture 6: Project container with requests != limits rejects with REJECTED_QOS (exit 1)"""
        rc, data, _ = self.run_preflight("qos_mismatch.yaml", "valid_capacity.yaml")
        self.assertEqual(rc, 1)
        self.assertEqual(data.get("validationStatus"), "REJECTED_QOS")

    def test_07_platform_container_unbounded_rejected(self):
        """Fixture 7: Platform container without limits rejects with REJECTED_UNBOUNDED (exit 1)"""
        rc, data, _ = self.run_preflight("platform_unbounded.yaml", "valid_capacity.yaml")
        self.assertEqual(rc, 1)
        self.assertEqual(data.get("validationStatus"), "REJECTED_UNBOUNDED")

    def test_08_memory_shrink_rejected(self):
        """Fixture 8: Project memory limit lowered vs baseline rejects with REJECTED_MEMORY_SHRINK (exit 1)"""
        rc, data, _ = self.run_preflight(
            candidate="memory_shrink.yaml",
            capacity="valid_capacity.yaml",
            baseline="valid_render.yaml"
        )
        self.assertEqual(rc, 1)
        self.assertEqual(data.get("validationStatus"), "REJECTED_MEMORY_SHRINK")

    def test_09_over_budget_rejected(self):
        """Fixture 9: Project replicas raised beyond slice rejects with REJECTED_OVER_BUDGET (exit 1)"""
        rc, data, _ = self.run_preflight("over_budget.yaml", "valid_capacity.yaml")
        self.assertEqual(rc, 1)
        self.assertEqual(data.get("validationStatus"), "REJECTED_OVER_BUDGET")

    def test_10_malformed_render_exit_2(self):
        """Fixture 10: Malformed render exits with code 2"""
        rc, _, _ = self.run_preflight("malformed.yaml", "valid_capacity.yaml")
        self.assertEqual(rc, 2)

    def test_11_sign_in_none_door_accepted(self):
        """A door that omits signIn and matches its NetworkPolicy is accepted."""
        rc, data, err = self.run_preflight("sign_in_none_door.yaml", "valid_capacity.yaml")
        self.assertEqual(rc, 0, err or data.get("rejectionReason"))
        self.assertEqual(data.get("validationStatus"), "PASSED")

    def test_12_door_port_mismatch_leaves_accepted_door(self):
        """Port 9090 is rejected and the previously accepted door file is unchanged."""
        accepted = os.path.join(FIXTURES, "sign_in_none_door.yaml")
        candidate = os.path.join(FIXTURES, "door_port_mismatch.yaml")
        with open(accepted, "rb") as f:
            accepted_before = f.read()
        with open(candidate, "rb") as f:
            candidate_before = f.read()

        rc, data, _ = self.run_preflight(
            candidate="door_port_mismatch.yaml",
            capacity="valid_capacity.yaml",
            baseline="sign_in_none_door.yaml",
        )

        with open(accepted, "rb") as f:
            accepted_after = f.read()
        with open(candidate, "rb") as f:
            candidate_after = f.read()
        self.assertEqual(accepted_before, accepted_after)
        self.assertEqual(candidate_before, candidate_after)
        self.assertEqual(rc, 1)
        self.assertEqual(data.get("validationStatus"), "REJECTED_ISOLATION")
        self.assertIn("9090", data.get("rejectionReason", ""))
        self.assertIn("not replaced", data.get("rejectionReason", ""))

    def _run_yaml(self, text):
        import tempfile
        with tempfile.NamedTemporaryFile("w", suffix=".yaml", delete=False) as tmp:
            tmp.write(text)
            path = tmp.name
        cmd = [
            sys.executable, SCRIPT,
            "--environment", "test-env",
            "--candidate", path,
            "--capacity", os.path.join(FIXTURES, "valid_capacity.yaml"),
            "--json",
        ]
        res = subprocess.run(cmd, capture_output=True, text=True)
        os.remove(path)
        data = json.loads(res.stdout) if res.stdout.strip() else {}
        return res.returncode, data

    def test_13_forward_auth_and_none_gateway_egress_rejected(self):
        """forward-auth on a project route, and gateway egress when signIn is omitted, are rejected."""
        door = """
apiVersion: v1
kind: Service
metadata:
  name: web
  namespace: proj-bad
spec:
  type: ClusterIP
  ports:
    - port: 80
---
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: isolation
  namespace: proj-bad
spec:
  podSelector: {}
  policyTypes: [Ingress]
  ingress:
    - from:
        - namespaceSelector:
            matchLabels:
              name: platform
      ports:
        - port: 80
---
apiVersion: traefik.io/v1alpha1
kind: IngressRoute
metadata:
  name: door
  namespace: proj-bad
spec:
  routes:
    - match: Host(`bad.example.com`)
      middlewares:
        - name: strip-forged-headers
        - name: forward-auth
      services:
        - name: web
          port: 80
"""
        rc, data = self._run_yaml(door)
        self.assertEqual(rc, 1)
        self.assertEqual(data.get("validationStatus"), "REJECTED_ISOLATION")
        self.assertIn("forward-auth", data.get("rejectionReason", ""))

        egress = """
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: isolation
  namespace: proj-none
spec:
  podSelector: {}
  policyTypes: [Egress]
  egress:
    - to:
        - namespaceSelector:
            matchLabels:
              kubernetes.io/metadata.name: identity
          podSelector:
            matchLabels:
              app.kubernetes.io/name: gateway
      ports:
        - port: 8080
"""
        rc, data = self._run_yaml(egress)
        self.assertEqual(rc, 1)
        self.assertEqual(data.get("validationStatus"), "REJECTED_ISOLATION")
        self.assertIn("forbids egress", data.get("rejectionReason", ""))

    def test_14_project_pn_shared_door_accepted(self):
        """Project PN's published door matches ports 80 and 8080 and may call the gateway."""
        parts = []
        for rel in (
            "deploy/platform/tls/certificates.yaml",
            "deploy/platform/tls/tls-store.yaml",
            "deploy/projects/pn/sign-in.yaml",
            "deploy/projects/pn/boundary/network-policy.yaml",
            "deploy/projects/pn/boundary/translation-egress.yaml",
            "deploy/projects/pn/workloads/ingress-route.yaml",
            "deploy/projects/pn/workloads/backend-service.yaml",
            "deploy/projects/pn/workloads/backend-deployment.yaml",
        ):
            with open(os.path.join(BASE_DIR, rel), encoding="utf-8") as fh:
                parts.append(fh.read().strip())
        rc, data = self._run_yaml("\n---\n".join(parts) + "\n")
        self.assertEqual(rc, 0, data.get("rejectionReason"))
        self.assertEqual(data.get("validationStatus"), "PASSED")
        applied = os.path.join(BASE_DIR, "deploy/projects/pn/boundary/sign-in.yaml")
        with open(applied, encoding="utf-8") as fh:
            self.assertIn("signIn: shared", fh.read())

    def test_15_valid_tls_render_passed(self):
        """Fixture valid_tls_render.yaml passes with exit code 0."""
        rc, data, err = self.run_preflight("valid_tls_render.yaml", "valid_capacity.yaml")
        self.assertEqual(rc, 0, err or data.get("rejectionReason"))
        self.assertEqual(data.get("validationStatus"), "PASSED")

    def test_16_tls_project_certificate_rejected(self):
        """TLS-1: Certificate in project namespace is rejected with rule 2."""
        rc, data, _ = self.run_preflight("tls_project_certificate.yaml", "valid_capacity.yaml")
        self.assertEqual(rc, 1)
        self.assertEqual(data.get("validationStatus"), "REJECTED_ISOLATION")
        rule2_details = [v.get("detail", "") for v in data.get("violations", []) if v.get("rule") == 2]
        self.assertTrue(any(d.startswith("TLS objects are platform-owned;") for d in rule2_details))
        self.assertTrue(data.get("rejectionReason", "").startswith("TLS objects are platform-owned;"))

    def test_17_tls_route_secret_name_rejected(self):
        """TLS-2: Project IngressRoute with tls.secretName is rejected with rule 2."""
        rc, data, _ = self.run_preflight("tls_route_secret_name.yaml", "valid_capacity.yaml")
        self.assertEqual(rc, 1)
        self.assertEqual(data.get("validationStatus"), "REJECTED_ISOLATION")
        rule2_details = [v.get("detail", "") for v in data.get("violations", []) if v.get("rule") == 2]
        self.assertTrue(any(d.startswith("route must declare tls: {} and let the platform select the certificate") for d in rule2_details))
        self.assertTrue(data.get("rejectionReason", "").startswith("route must declare tls: {} and let the platform select the certificate"))

    def test_18_tls_route_web_entrypoint_rejected(self):
        """TLS-3: Project IngressRoute without websecure-only entrypoint is rejected with rule 2."""
        rc, data, _ = self.run_preflight("tls_route_web_entrypoint.yaml", "valid_capacity.yaml")
        self.assertEqual(rc, 1)
        self.assertEqual(data.get("validationStatus"), "REJECTED_ISOLATION")
        rule2_details = [v.get("detail", "") for v in data.get("violations", []) if v.get("rule") == 2]
        self.assertTrue(any(d.startswith("project route entry point must be websecure only") for d in rule2_details))
        self.assertTrue(data.get("rejectionReason", "").startswith("project route entry point must be websecure only"))

    def test_19_tls_route_host_regexp_rejected(self):
        """TLS-4: Route match with HostRegexp is rejected with rule 2."""
        rc, data, _ = self.run_preflight("tls_route_host_regexp.yaml", "valid_capacity.yaml")
        self.assertEqual(rc, 1)
        self.assertEqual(data.get("validationStatus"), "REJECTED_ISOLATION")
        rule2_details = [v.get("detail", "") for v in data.get("violations", []) if v.get("rule") == 2]
        self.assertTrue(any(d.startswith("route match must name its hostname with Host()") for d in rule2_details))
        self.assertTrue(data.get("rejectionReason", "").startswith("route match must name its hostname with Host()"))

    def test_20_tls_uncovered_host_rejected(self):
        """TLS-5: Route match with uncovered hostname is rejected with rule 2."""
        rc, data, _ = self.run_preflight("tls_uncovered_host.yaml", "valid_capacity.yaml")
        self.assertEqual(rc, 1)
        self.assertEqual(data.get("validationStatus"), "REJECTED_ISOLATION")
        rule2_details = [v.get("detail", "") for v in data.get("violations", []) if v.get("rule") == 2]
        self.assertTrue(any(d.startswith("hostname shop.example.com has no platform certificate; add it to deploy/platform/tls first") for d in rule2_details))
        self.assertTrue(data.get("rejectionReason", "").startswith("hostname shop.example.com has no platform certificate; add it to deploy/platform/tls first"))

    def test_21_tls_store_missing_secret_rejected(self):
        """TLS-6: TLSStore listing secret with no Certificate is rejected with rule 2."""
        rc, data, _ = self.run_preflight("tls_store_missing_secret.yaml", "valid_capacity.yaml")
        self.assertEqual(rc, 1)
        self.assertEqual(data.get("validationStatus"), "REJECTED_ISOLATION")
        rule2_details = [v.get("detail", "") for v in data.get("violations", []) if v.get("rule") == 2]
        self.assertTrue(any(d.startswith("certificate inventory:") for d in rule2_details))
        self.assertTrue(data.get("rejectionReason", "").startswith("certificate inventory:"))


    def test_22_tls_platform_route_secret_name_rejected(self):
        """TLS-2 (platform): a platform websecure route naming a secret is rejected with rule 2."""
        rc, data, _ = self.run_preflight("tls_platform_route_secret_name.yaml", "valid_capacity.yaml")
        self.assertEqual(rc, 1)
        self.assertEqual(data.get("validationStatus"), "REJECTED_ISOLATION")
        details = [v.get("detail", "") for v in data.get("violations", []) if v.get("rule") == 2]
        self.assertTrue(any(d.startswith("route must declare tls: {} and let the platform select the certificate") for d in details))

    def test_23_db_contract_and_readiness_probe_enforced(self):
        """Database-backed project containers must route via vault-db-proxy and use /readyz for readinessProbe."""
        bad_db_and_probe = """
apiVersion: apps/v1
kind: Deployment
metadata:
  name: pn-backend
  namespace: proj-pn
spec:
  replicas: 1
  strategy:
    type: Recreate
  template:
    spec:
      automountServiceAccountToken: false
      containers:
        - name: backend
          image: ghcr.io/nacfson/project-pn-backend@sha256:d3ab8644467bbbb2a49b0afc6631b2600ce344908ce15ddf99e8478c11ecaf21
          env:
            - name: DB_HOST
              value: "postgres-service.identity.svc"
            - name: DB_NAME
              value: "keycloak"
          readinessProbe:
            httpGet:
              path: /healthz
              port: 8080
          livenessProbe:
            httpGet:
              path: /healthz
              port: 8080
          resources:
            requests:
              cpu: 200m
              memory: 256Mi
            limits:
              cpu: 200m
              memory: 256Mi
          securityContext:
            allowPrivilegeEscalation: false
            privileged: false
            capabilities:
              drop:
                - ALL
"""
        rc, data = self._run_yaml(bad_db_and_probe)
        self.assertEqual(rc, 1)
        self.assertEqual(data.get("validationStatus"), "REJECTED_ISOLATION")
        details = [v.get("detail", "") for v in data.get("violations", []) if v.get("rule") == 2]
        self.assertTrue(any("vault-db-proxy.vault.svc" in d for d in details))
        self.assertTrue(any("proj_pn" in d for d in details))
        self.assertTrue(any("readinessProbe.httpGet.path: /readyz" in d for d in details))

    def test_24_cni_reservation_and_openbao_netpol_accepted(self):
        """CNI kustomization (+225m CPU / +1248Mi memory), portable OpenBao NetworkPolicy, and Hubble ClusterIP invariants pass."""
        import yaml

        cni_yaml = kustomize_build(os.path.join(BASE_DIR, "deploy", "platform", "cni"))
        with open(os.path.join(BASE_DIR, "deploy", "platform", "vault", "network-policy.yaml"), encoding="utf-8") as fh:
            vault_np_yaml = fh.read()

        rc, data = self._run_yaml(cni_yaml + "\n---\n" + vault_np_yaml)
        self.assertEqual(rc, 0, data.get("rejectionReason"))
        self.assertEqual(data.get("validationStatus"), "PASSED")
        # valid_capacity.yaml has systemReserved: 200m CPU, 512Mi memory.
        # CNI adds +225m CPU (total 425m) and +1248Mi memory (total 1760Mi).
        self.assertEqual(data.get("platformReservations", {}).get("cpuMillicores"), 425)
        self.assertEqual(data.get("platformReservations", {}).get("memoryMib"), 1760)

        # Verify OpenBao and Vault portable NetworkPolicy invariants (zero CiliumNetworkPolicy, zero registry-proxy-policy)
        vault_docs = [d for d in yaml.safe_load_all(vault_np_yaml) if d]
        self.assertFalse(any(d.get("kind") == "CiliumNetworkPolicy" for d in vault_docs))
        self.assertFalse(any((d.get("metadata") or {}).get("name") == "registry-proxy-policy" for d in vault_docs))
        openbao_ingress = next(d for d in vault_docs if d.get("metadata", {}).get("name") == "openbao-ingress-policy")
        self.assertNotIn("0.0.0.0/0", yaml.dump(openbao_ingress))
        openbao_backup = next(d for d in vault_docs if d.get("metadata", {}).get("name") == "openbao-backup-policy")
        self.assertEqual(openbao_backup["spec"]["podSelector"]["matchLabels"]["app.kubernetes.io/name"], "openbao-backup")

        # Verify Cilium ConfigMap enables policy-cidr-match-mode=nodes for single-node K3s ipBlock apiserver matching
        cni_docs = [d for d in yaml.safe_load_all(cni_yaml) if d]
        cilium_cm = next(d for d in cni_docs if d.get("kind") == "ConfigMap" and d.get("metadata", {}).get("name") == "cilium-config")
        self.assertEqual((cilium_cm.get("data") or {}).get("policy-cidr-match-mode"), "nodes")

        # Verify Hubble services are strictly ClusterIP and have zero IngressRoutes
        hubble_svcs = {
            d["metadata"]["name"]: d["spec"].get("type", "ClusterIP")
            for d in cni_docs
            if d.get("kind") == "Service" and d.get("metadata", {}).get("name", "").startswith("hubble-")
        }
        self.assertEqual(hubble_svcs.get("hubble-ui"), "ClusterIP")
        self.assertEqual(hubble_svcs.get("hubble-relay"), "ClusterIP")
        self.assertEqual(hubble_svcs.get("hubble-metrics"), "ClusterIP")
        self.assertFalse(any(d.get("kind") == "IngressRoute" for d in cni_docs))

    def test_25_observability_platform_hostpath_accepted(self):
        """observability is a platform namespace; hostPath and root are exempt from project Rule 2/3."""
        text = """
apiVersion: apps/v1
kind: DaemonSet
metadata:
  name: fluent-bit
  namespace: observability
spec:
  selector:
    matchLabels:
      app: fluent-bit
  template:
    metadata:
      labels:
        app: fluent-bit
    spec:
      volumes:
        - name: varlogpods
          hostPath:
            path: /var/log/pods
            type: Directory
        - name: state
          hostPath:
            path: /var/lib/fluent-bit-state
            type: DirectoryOrCreate
      containers:
        - name: fluent-bit
          image: fluent/fluent-bit:1.9
          securityContext:
            runAsUser: 0
          volumeMounts:
            - name: varlogpods
              mountPath: /var/log/pods
              readOnly: true
            - name: state
              mountPath: /state
              readOnly: false
          resources:
            requests:
              cpu: 10m
              memory: 32Mi
            limits:
              cpu: 10m
              memory: 32Mi
"""
        rc, data = self._run_yaml(text)
        self.assertEqual(rc, 0, data.get("rejectionReason"))
        self.assertEqual(data["validationStatus"], "PASSED")

        # Mounting /var/lib/fluent-bit-state on Deployment/error-detector must be rejected
        bad_detector = text.replace("kind: DaemonSet", "kind: Deployment").replace("name: fluent-bit", "name: error-detector")
        rc_bad, data_bad = self._run_yaml(bad_detector)
        self.assertEqual(rc_bad, 1)
        self.assertEqual(data_bad["validationStatus"], "REJECTED_ISOLATION")

    def test_26_project_health_vps_and_local_render_accepted(self):
        """local-k3s, vps-k3s, and gke render proj-pn + proj-health without spec.images overrides."""
        import yaml

        kustomize_dirs = [
            os.path.join(BASE_DIR, "deploy", "projects", "pn", "boundary"),
            os.path.join(BASE_DIR, "deploy", "projects", "pn", "workloads"),
            os.path.join(BASE_DIR, "deploy", "projects", "health", "boundary"),
            os.path.join(BASE_DIR, "deploy", "projects", "health", "workloads"),
            os.path.join(BASE_DIR, "deploy", "platform", "tls"),
        ]
        base_chunks = [kustomize_build(kdir) for kdir in kustomize_dirs]
        base_yaml = "\n---\n".join(base_chunks)

        for env, expected_host, expected_pn_host in (
            ("local-k3s", "health.example.com", "pn.example.com"),
            ("vps-k3s", "health.nacfson.cloud", "pn.nacfson.cloud"),
            ("gke", "health.nacfson.cloud", "pn.nacfson.cloud"),
        ):
            proj_kustom = os.path.join(BASE_DIR, "clusters", env, "projects", "kustomization.yaml")
            with open(proj_kustom, encoding="utf-8") as fh:
                kdoc = yaml.safe_load(fh)
            self.assertIn("proj-pn.yaml", kdoc.get("resources") or [])
            self.assertIn("proj-health.yaml", kdoc.get("resources") or [])

            proj_health_path = os.path.join(BASE_DIR, "clusters", env, "projects", "proj-health.yaml")
            self.assertTrue(os.path.exists(proj_health_path), f"Missing {proj_health_path}")
            with open(proj_health_path, encoding="utf-8") as fh:
                hdoc = yaml.safe_load(fh)
            self.assertEqual(hdoc["metadata"]["name"], "proj-health")
            self.assertEqual(hdoc["metadata"]["namespace"], "proj-health")
            self.assertEqual(hdoc["spec"]["serviceAccountName"], "health-reconciler")
            self.assertEqual(hdoc["spec"]["path"], "./deploy/projects/health/workloads")
            self.assertNotIn("images", hdoc["spec"], f"{proj_health_path} must not override workload digest pins via spec.images")

            patches = hdoc["spec"].get("patches") or []
            ing_patch_ops = next(
                yaml.safe_load(p["patch"])
                for p in patches
                if p.get("target", {}).get("kind") == "IngressRoute"
                and p.get("target", {}).get("name") == "project-health-ingress"
            )
            op_map = {op["path"]: op["value"] for op in ing_patch_ops}
            self.assertEqual(op_map.get("/spec/routes/0/match"), f"Host(`{expected_host}`)")
            self.assertEqual(op_map.get("/spec/routes/1/match"), f"Host(`{expected_host}`) && PathPrefix(`/api`)")

            dep_patch = next(
                yaml.safe_load(p["patch"])
                for p in patches
                if p.get("target", {}).get("kind") == "Deployment"
                and p.get("target", {}).get("name") == "health-backend"
            )
            env_list = dep_patch["spec"]["template"]["spec"]["containers"][0]["env"]
            env_map = {e["name"]: e["value"] for e in env_list}
            self.assertEqual(env_map.get("PUBLIC_APP_URL"), f"https://{expected_host}")
            self.assertIn(f"https://{expected_host}", env_map.get("ALLOWED_ORIGINS", "").split(","))

            if env == "gke":
                continue

            # Apply environment host patches onto base_yaml and validate via platform-preflight.py
            docs = [d for d in yaml.safe_load_all(base_yaml) if isinstance(d, dict)]
            for d in docs:
                kind = d.get("kind")
                name = (d.get("metadata") or {}).get("name")
                if kind == "IngressRoute" and name == "project-health-ingress":
                    d["spec"]["routes"][0]["match"] = op_map["/spec/routes/0/match"]
                    d["spec"]["routes"][1]["match"] = op_map["/spec/routes/1/match"]
                elif kind == "IngressRoute" and name == "project-pn-ingress":
                    d["spec"]["routes"][0]["match"] = f"Host(`{expected_pn_host}`)"
                    d["spec"]["routes"][1]["match"] = f"Host(`{expected_pn_host}`) && PathPrefix(`/api`)"
                elif kind == "Deployment" and name == "health-backend":
                    c_envs = d["spec"]["template"]["spec"]["containers"][0]["env"]
                    for e in c_envs:
                        if e["name"] in env_map:
                            e["value"] = env_map[e["name"]]
                elif kind == "Deployment" and name == "pn-backend":
                    c_envs = d["spec"]["template"]["spec"]["containers"][0]["env"]
                    for e in c_envs:
                        if e["name"] == "PUBLIC_APP_URL":
                            e["value"] = f"https://{expected_pn_host}"
                        elif e["name"] == "ALLOWED_ORIGINS":
                            e["value"] = f"https://{expected_pn_host}"
                elif kind == "Certificate" and name == "health-tls":
                    d["spec"]["commonName"] = expected_host
                    d["spec"]["dnsNames"] = [expected_host]
                elif kind == "Certificate" and name == "pn-tls":
                    d["spec"]["commonName"] = expected_pn_host
                    d["spec"]["dnsNames"] = [expected_pn_host]

            import tempfile
            with tempfile.NamedTemporaryFile("w", suffix=".yaml", delete=False) as tmp:
                tmp.write(yaml.safe_dump_all(docs))
                cand_path = tmp.name
            try:
                rc, data, err = self.run_preflight(
                    cand_path,
                    os.path.join(BASE_DIR, "clusters", env, "capacity.yaml"),
                    env=env,
                )
                self.assertEqual(rc, 0, err or data.get("rejectionReason"))
                self.assertEqual(data.get("validationStatus"), "PASSED")
                self.assertEqual(data.get("projectCount"), 2)
            finally:
                if os.path.exists(cand_path):
                    os.remove(cand_path)

    def test_27_vault_seed_and_rotate_cover_all_project_slugs(self):
        """vault-seed-inventory.sh and vault-rotate.sh must cover every slug in postgres-init-job PROJECT_SLUGS."""
        import yaml

        init_job_path = os.path.join(BASE_DIR, "deploy", "platform", "database", "postgres-init-job.yaml")
        with open(init_job_path, encoding="utf-8") as fh:
            job_doc = yaml.safe_load(fh)
        init_envs = {
            e["name"]: e["value"]
            for e in job_doc["spec"]["template"]["spec"]["initContainers"][0]["env"]
        }
        slugs = [s.strip() for s in init_envs["PROJECT_SLUGS"].split(",") if s.strip()]
        self.assertIn("pn", slugs)
        self.assertIn("health", slugs)

        with open(os.path.join(BASE_DIR, "scripts", "vault-seed-inventory.sh"), encoding="utf-8") as fh:
            seed_script = fh.read()
        with open(os.path.join(BASE_DIR, "scripts", "vault-rotate.sh"), encoding="utf-8") as fh:
            rotate_script = fh.read()

        for slug in slugs:
            self.assertIn(f'seed_secret "kv/database/{slug}-user"', seed_script)
            self.assertIn(f'username="user_{slug}"', seed_script)
            self.assertIn(f'seed_secret "kv/projects/{slug}/client-secret"', seed_script)
            self.assertIn(f'client_id="project-{slug}"', seed_script)
            self.assertIn(f"postgres-{slug}-db-password) echo \"kv/database/{slug}-user\"", rotate_script)
            self.assertIn(f"project-{slug}-client-secret) echo \"kv/projects/{slug}/client-secret\"", rotate_script)

    def _run_with_custom_capacity(self, candidate_yaml: str, capacity_yaml: str, env: str = "gke"):
        import tempfile
        with tempfile.NamedTemporaryFile("w", suffix=".yaml", delete=False) as c_tmp:
            c_tmp.write(candidate_yaml)
            c_path = c_tmp.name
        with tempfile.NamedTemporaryFile("w", suffix=".yaml", delete=False) as cap_tmp:
            cap_tmp.write(capacity_yaml)
            cap_path = cap_tmp.name
        try:
            cmd = [
                sys.executable, SCRIPT,
                "--environment", env,
                "--candidate", c_path,
                "--capacity", cap_path,
                "--json",
            ]
            res = subprocess.run(cmd, capture_output=True, text=True)
            data = json.loads(res.stdout) if res.stdout.strip() else {}
            return res.returncode, data
        finally:
            for p in (c_path, cap_path):
                if os.path.exists(p):
                    os.remove(p)

    def test_28_gke_multinode_capacity_daemonset_multiplier_and_paused_deployment(self):
        """Multi-node ClusterCapacityProfile validates pool sum, multiplies DaemonSet by tolerated nodes, and pre-reserves max(replicas,1) on platform Deployment."""
        cap = """
environment: gke
nodeName: gke-zonal-standard
observedAt: "2026-11-10T00:00:00Z"
nodeCount: 2
allocatable:
  cpuMillicores: 1880
  memoryMib: 4408
systemReserved:
  cpuMillicores: 350
  memoryMib: 576
pools:
  - name: ondemand-core
    machineType: e2-medium
    nodeCount: 1
    provisioning: standard
    allocatable:
      cpuMillicores: 940
      memoryMib: 2972
    systemReserved:
      cpuMillicores: 200
      memoryMib: 320
    labels:
      cloud.google.com/gke-provisioning: "standard"
    taints: []
  - name: spot-controllers
    machineType: e2-small
    nodeCount: 1
    provisioning: spot
    allocatable:
      cpuMillicores: 940
      memoryMib: 1436
    systemReserved:
      cpuMillicores: 150
      memoryMib: 256
    labels:
      cloud.google.com/gke-spot: "true"
      cloud.google.com/gke-provisioning: "spot"
    taints:
      - "cloud.google.com/gke-spot=true:NoSchedule"
"""
        manifests = """
apiVersion: apps/v1
kind: DaemonSet
metadata:
  name: fluent-bit
  namespace: observability
spec:
  template:
    spec:
      tolerations:
        - key: cloud.google.com/gke-spot
          operator: Equal
          value: "true"
          effect: NoSchedule
      containers:
        - name: fluent-bit
          image: fluent/fluent-bit:4.2.8
          resources:
            requests:
              cpu: 25m
              memory: 64Mi
            limits:
              cpu: 50m
              memory: 64Mi
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: error-detector
  namespace: observability
spec:
  replicas: 0
  template:
    spec:
      containers:
        - name: error-detector
          image: asia-northeast3-docker.pkg.dev/nacfson-pipeline/nacfson-public/error-detector@sha256:1111111111111111111111111111111111111111111111111111111111111111
          resources:
            requests:
              cpu: 25m
              memory: 64Mi
            limits:
              cpu: 50m
              memory: 64Mi
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: source-controller
  namespace: flux-system
spec:
  replicas: 1
  template:
    spec:
      nodeSelector:
        cloud.google.com/gke-spot: "true"
      tolerations:
        - key: cloud.google.com/gke-spot
          operator: Equal
          value: "true"
          effect: NoSchedule
      containers:
        - name: manager
          image: ghcr.io/fluxcd/source-controller:v1.5.0
          resources:
            requests:
              cpu: 50m
              memory: 64Mi
            limits:
              cpu: 500m
              memory: 128Mi
"""
        rc, data = self._run_with_custom_capacity(manifests, cap)
        self.assertEqual(rc, 0, data.get("rejectionReason"))
        # 350m sys + 2*25m fluent-bit + 1*25m error-detector (max(0,1)) + 50m source-controller = 475m
        # 576Mi sys + 2*64Mi fluent-bit + 1*64Mi error-detector + 128Mi source-controller = 896Mi
        self.assertEqual(data["platformReservations"]["cpuMillicores"], 475)
        self.assertEqual(data["platformReservations"]["memoryMib"], 896)

        # Mismatched pool allocatable sum fails Rule 1
        bad_cap = cap.replace("cpuMillicores: 1880", "cpuMillicores: 2000")
        rc_bad, data_bad = self._run_with_custom_capacity(manifests, bad_cap)
        self.assertEqual(rc_bad, 1)
        self.assertEqual(data_bad.get("validationStatus"), "REJECTED_NO_MEASUREMENT")

    def test_29_spot_pool_forbids_rwo_pvc_keycloak_and_tenant_pods(self):
        """Scheduling keycloak, proj-* workloads, or PVC-mounting pods onto spot-controllers is rejected."""
        with open(os.path.join(BASE_DIR, "clusters", "gke", "capacity.yaml"), encoding="utf-8") as fh:
            gke_cap = fh.read()

        bad_keycloak_on_spot = """
apiVersion: apps/v1
kind: Deployment
metadata:
  name: keycloak
  namespace: identity
spec:
  replicas: 1
  template:
    spec:
      nodeSelector:
        cloud.google.com/gke-spot: "true"
      tolerations:
        - key: cloud.google.com/gke-spot
          operator: Equal
          value: "true"
          effect: NoSchedule
      containers:
        - name: keycloak
          image: quay.io/keycloak/keycloak:24.0.5
          resources:
            requests:
              cpu: 100m
              memory: 256Mi
            limits:
              cpu: 500m
              memory: 256Mi
"""
        rc1, data1 = self._run_with_custom_capacity(bad_keycloak_on_spot, gke_cap)
        self.assertEqual(rc1, 1)
        self.assertIn("spot", data1.get("rejectionReason", "").lower())

        bad_pvc_on_spot = """
apiVersion: apps/v1
kind: Deployment
metadata:
  name: cert-manager
  namespace: cert-manager
spec:
  replicas: 1
  template:
    spec:
      nodeSelector:
        cloud.google.com/gke-spot: "true"
      tolerations:
        - key: cloud.google.com/gke-spot
          operator: Equal
          value: "true"
          effect: NoSchedule
      volumes:
        - name: state
          persistentVolumeClaim:
            claimName: rwo-pvc
      containers:
        - name: cert-manager
          image: quay.io/jetstack/cert-manager-controller:v1.21.2
          resources:
            requests:
              cpu: 25m
              memory: 64Mi
            limits:
              cpu: 100m
              memory: 128Mi
"""
        rc2, data2 = self._run_with_custom_capacity(bad_pvc_on_spot, gke_cap)
        self.assertEqual(rc2, 1)
        self.assertIn("spot", data2.get("rejectionReason", "").lower())

    def test_30_ondemand_backup_headroom_and_loadbalancer_rejected_on_gke(self):
        """ondemand-core must retain >= 50m CPU and >= 192 MiB memory headroom, and Service type LoadBalancer is rejected."""
        with open(os.path.join(BASE_DIR, "clusters", "gke", "capacity.yaml"), encoding="utf-8") as fh:
            gke_cap = fh.read()

        # 200m sys + 710m steady-state = 910m -> only 30m CPU headroom (< 50m required)
        exhausted_ondemand = """
apiVersion: apps/v1
kind: Deployment
metadata:
  name: gateway
  namespace: identity
spec:
  replicas: 1
  template:
    spec:
      containers:
        - name: gateway
          image: asia-northeast3-docker.pkg.dev/nacfson-pipeline/nacfson-public/platform-auth-gateway@sha256:2222222222222222222222222222222222222222222222222222222222222222
          resources:
            requests:
              cpu: 710m
              memory: 64Mi
            limits:
              cpu: 710m
              memory: 64Mi
"""
        rc, data = self._run_with_custom_capacity(exhausted_ondemand, gke_cap)
        self.assertEqual(rc, 1)
        self.assertIn("headroom", data.get("rejectionReason", "").lower())

        lb_svc = """
apiVersion: v1
kind: Service
metadata:
  name: traefik
  namespace: platform
spec:
  type: LoadBalancer
  ports:
    - port: 443
      targetPort: 8443
"""
        rc_lb, data_lb = self._run_with_custom_capacity(lb_svc, gke_cap)
        self.assertEqual(rc_lb, 1)
        self.assertIn("LoadBalancer", data_lb.get("rejectionReason", ""))

    def test_31_gar_digest_accepted_and_mutable_project_tag_rejected(self):
        """Project workloads accept public GAR @sha256 digests and reject mutable tags or foreign GAR projects."""
        with open(os.path.join(BASE_DIR, "clusters", "gke", "capacity.yaml"), encoding="utf-8") as fh:
            gke_cap = fh.read()

        gar_proj = """
apiVersion: apps/v1
kind: Deployment
metadata:
  name: blog-web
  namespace: proj-blog
spec:
  replicas: 1
  strategy:
    type: RollingUpdate
    rollingUpdate:
      maxSurge: 0
      maxUnavailable: 1
  template:
    metadata:
      labels:
        app.kubernetes.io/version: v1.2.3
      annotations:
        logs.nacfson.cloud/blog-web: v3/nginx
    spec:
      automountServiceAccountToken: false
      containers:
        - name: blog-web
          image: asia-northeast3-docker.pkg.dev/nacfson-pipeline/nacfson-public/blog-web@sha256:abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789
          securityContext:
            allowPrivilegeEscalation: false
            capabilities:
              drop: ["ALL"]
          resources:
            requests:
              cpu: 50m
              memory: 128Mi
            limits:
              cpu: 50m
              memory: 128Mi
"""
        rc_ok, data_ok = self._run_with_custom_capacity(gar_proj, gke_cap)
        self.assertEqual(rc_ok, 0, data_ok.get("rejectionReason"))

        bad_tag = gar_proj.replace(
            "@sha256:abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789",
            ":v1.2.3",
        )
        rc_tag, data_tag = self._run_with_custom_capacity(bad_tag, gke_cap)
        self.assertEqual(rc_tag, 1)
        self.assertIn("digest", data_tag.get("rejectionReason", "").lower())


if __name__ == "__main__":
    unittest.main()

