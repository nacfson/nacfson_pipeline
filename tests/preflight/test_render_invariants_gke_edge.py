#!/usr/bin/env python3

import importlib.util
import os
import unittest

BASE_DIR = os.path.abspath(os.path.join(os.path.dirname(__file__), "..", ".."))
_spec = importlib.util.spec_from_file_location(
    "render_invariants", os.path.join(BASE_DIR, "scripts", "render-invariants.py"))
render_invariants = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(render_invariants)


def traefik_service(**overrides):
    svc = {
        "apiVersion": "v1",
        "kind": "Service",
        "metadata": {
            "name": "traefik",
            "namespace": "platform",
            "annotations": {"networking.gke.io/load-balancer-ip-addresses": "gke-ingress-ip"},
        },
        "spec": {
            "type": "LoadBalancer",
            "loadBalancerClass": "networking.gke.io/l4-regional-external",
            "externalTrafficPolicy": "Local",
            "ports": [{"name": "web", "port": 80, "targetPort": 8000}],
        },
    }
    for key, value in overrides.items():
        section, field = key.split("__")
        if value is None:
            svc[section].pop(field, None)
        else:
            svc[section][field] = value
    return svc


def traefik_deployment(host_ports=()):
    ports = [{"name": "web", "containerPort": 8000}, {"name": "websecure", "containerPort": 8443}]
    for port, hp in zip(ports, host_ports):
        port["hostPort"] = hp
    return {
        "apiVersion": "apps/v1",
        "kind": "Deployment",
        "metadata": {"name": "traefik", "namespace": "platform"},
        "spec": {"template": {"spec": {"containers": [{"name": "traefik", "ports": ports}]}}},
    }


def service(name, ns, svc_type):
    return {"kind": "Service", "metadata": {"name": name, "namespace": ns}, "spec": {"type": svc_type}}


class GkeEdgeInvariantTests(unittest.TestCase):
    check = staticmethod(render_invariants.check_gke_edge)

    def test_valid_edge_passes(self):
        self.assertEqual(self.check([traefik_service(), traefik_deployment()]), [])

    def test_missing_traefik_service_or_deployment(self):
        self.assertTrue(self.check([traefik_deployment()]))
        self.assertTrue(self.check([traefik_service()]))

    def test_second_loadbalancer_rejected(self):
        errors = self.check([traefik_service(), traefik_deployment(), service("extra", "platform", "LoadBalancer")])
        self.assertTrue(any("platform/extra" in e for e in errors), errors)

    def test_traefik_must_be_loadbalancer(self):
        self.assertTrue(self.check([traefik_service(spec__type="ClusterIP"), traefik_deployment()]))

    def test_wrong_class_rejected(self):
        for value in (None, "networking.gke.io/l4-regional-internal"):
            with self.subTest(value=value):
                self.assertTrue(self.check([traefik_service(spec__loadBalancerClass=value), traefik_deployment()]))

    def test_cluster_policy_rejected(self):
        self.assertTrue(self.check([traefik_service(spec__externalTrafficPolicy="Cluster"), traefik_deployment()]))

    def test_missing_or_empty_annotation_rejected(self):
        for value in ({}, {"networking.gke.io/load-balancer-ip-addresses": ""}):
            with self.subTest(value=value):
                self.assertTrue(self.check([traefik_service(metadata__annotations=value), traefik_deployment()]))

    def test_host_port_rejected(self):
        self.assertTrue(self.check([traefik_service(), traefik_deployment(host_ports=(80, 443))]))

    def test_host_network_rejected(self):
        dep = traefik_deployment()
        dep["spec"]["template"]["spec"]["hostNetwork"] = True
        errors = self.check([traefik_service(), dep])
        self.assertTrue(any("hostNetwork" in e for e in errors), errors)

    def test_node_port_rejected_anywhere(self):
        errors = self.check([traefik_service(), traefik_deployment(), service("x", "platform", "NodePort")])
        self.assertTrue(any("NodePort" in e for e in errors), errors)


if __name__ == "__main__":
    unittest.main()
