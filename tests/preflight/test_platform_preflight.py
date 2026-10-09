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
            "deploy/projects/pn/sign-in.yaml",
            "deploy/projects/pn/boundary/network-policy.yaml",
            "deploy/projects/pn/workloads/ingress-route.yaml",
            "deploy/projects/pn/workloads/backend-service.yaml",
        ):
            with open(os.path.join(BASE_DIR, rel), encoding="utf-8") as fh:
                parts.append(fh.read().strip())
        rc, data = self._run_yaml("\n---\n".join(parts) + "\n")
        self.assertEqual(rc, 0, data.get("rejectionReason"))
        self.assertEqual(data.get("validationStatus"), "PASSED")
        applied = os.path.join(BASE_DIR, "deploy/projects/pn/boundary/sign-in.yaml")
        with open(applied, encoding="utf-8") as fh:
            self.assertIn("signIn: shared", fh.read())


if __name__ == "__main__":
    unittest.main()
