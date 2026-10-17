#!/usr/bin/env python3
"""
Platform Preflight Check (SPEC.md DEPLOY-04, contracts/platform-preflight.md).
Validates candidate renders against capacity, isolation, QoS, memory freeze, and project budget.
"""

import argparse
import json
import math
import os
import re
import sys
import yaml

# Validation status enums
STATUS_PASSED = "PASSED"
STATUS_REJECTED_NO_MEASUREMENT = "REJECTED_NO_MEASUREMENT"
STATUS_REJECTED_ISOLATION = "REJECTED_ISOLATION"
STATUS_REJECTED_QOS = "REJECTED_QOS"
STATUS_REJECTED_UNBOUNDED = "REJECTED_UNBOUNDED"
STATUS_REJECTED_MEMORY_SHRINK = "REJECTED_MEMORY_SHRINK"
STATUS_REJECTED_OVER_BUDGET = "REJECTED_OVER_BUDGET"

PLATFORM_NAMESPACES = {"flux-system", "platform", "vault", "identity", "governance", "kube-system", "cert-manager"}


def parse_cpu(val):
    if val is None:
        return None
    val_str = str(val).strip()
    if val_str.endswith("m"):
        return int(val_str[:-1])
    return int(float(val_str) * 1000)


def parse_memory_mib(val):
    if val is None:
        return None
    val_str = str(val).strip()
    if val_str.endswith("Ki"):
        return int(val_str[:-2]) / 1024.0
    if val_str.endswith("Mi"):
        return float(val_str[:-2])
    if val_str.endswith("Gi"):
        return float(val_str[:-2]) * 1024.0
    if val_str.endswith("Ti"):
        return float(val_str[:-2]) * 1024.0 * 1024.0
    # plain bytes
    return float(val_str) / (1024.0 * 1024.0)


def load_yaml_docs(file_path):
    if not os.path.exists(file_path):
        return []
    try:
        with open(file_path, "r", encoding="utf-8") as f:
            content = f.read()
            if not content.strip():
                return []
            return [doc for doc in yaml.safe_load_all(content) if doc is not None]
    except Exception as e:
        sys.stderr.write(f"Error parsing YAML from {file_path}: {e}\n")
        sys.exit(2)


def sign_in_choice(docs):
    """Return (choice, error). Absent means none."""
    choice = None
    for doc in docs:
        if not isinstance(doc, dict):
            continue
        if isinstance(doc.get("signIn"), str):
            choice = doc.get("signIn")
        data = doc.get("data")
        if isinstance(data, dict) and isinstance(data.get("signIn"), str):
            choice = data.get("signIn")
        spec = doc.get("spec")
        if isinstance(spec, dict) and isinstance(spec.get("signIn"), str):
            choice = spec.get("signIn")
    if choice is None or choice == "":
        return "none", None
    if choice not in ("shared", "none"):
        return choice, f"signIn must be shared or none, got {choice!r}"
    return choice, None


def _as_port(value):
    if isinstance(value, int) and not isinstance(value, bool):
        return value
    if isinstance(value, str) and value.isdigit():
        return int(value)
    return None


def _platform_ingress_ports(docs):
    ports = set()
    for doc in docs:
        if doc.get("kind") != "NetworkPolicy":
            continue
        for rule in (doc.get("spec") or {}).get("ingress") or []:
            from_platform = False
            for src in rule.get("from") or []:
                labels = (src.get("namespaceSelector") or {}).get("matchLabels") or {}
                if labels.get("name") == "platform" or labels.get("kubernetes.io/metadata.name") == "platform":
                    from_platform = True
            if not from_platform:
                continue
            for entry in rule.get("ports") or []:
                port = _as_port(entry.get("port"))
                if port is not None:
                    ports.add(port)
    return ports


def _services_by_name(docs):
    found = {}
    for doc in docs:
        if doc.get("kind") != "Service":
            continue
        name = (doc.get("metadata") or {}).get("name")
        if name:
            found[name] = doc
    return found


def _service_ports(svc):
    ports = set()
    for entry in (svc.get("spec") or {}).get("ports") or []:
        port = _as_port(entry.get("port"))
        if port is not None:
            ports.add(port)
    return ports


def _middleware_names(route):
    names = []
    for mw in route.get("middlewares") or []:
        if isinstance(mw, dict):
            names.append(mw.get("name") or "")
        elif isinstance(mw, str):
            names.append(mw)
    return names


def _gateway_or_signin_egress(rule):
    hits = []
    for dest in rule.get("to") or []:
        ns_labels = (dest.get("namespaceSelector") or {}).get("matchLabels") or {}
        pod_labels = (dest.get("podSelector") or {}).get("matchLabels") or {}
        ns = ns_labels.get("kubernetes.io/metadata.name") or ns_labels.get("name") or ""
        name = pod_labels.get("app.kubernetes.io/name") or pod_labels.get("app") or ""
        if ns == "identity" or name in ("gateway", "keycloak"):
            ports = []
            for entry in rule.get("ports") or []:
                port = _as_port(entry.get("port"))
                if port is not None:
                    ports.append(port)
            hits.append((name, ports))
    return hits


def check_project_doors(projects_dict):
    """Reject a candidate door that does not match the project's internal network.

    A rejection does not rewrite the last accepted door. This check only reads
    the candidate and reports it.
    """
    violations = []
    kept = "; last accepted door is not replaced"
    for proj_id, docs in sorted(projects_dict.items()):
        choice, choice_err = sign_in_choice(docs)
        if choice_err:
            violations.append({
                "rule": 2,
                "object": f"project/{proj_id}",
                "detail": choice_err + kept,
            })
            continue
        allowed_ports = _platform_ingress_ports(docs)
        services = _services_by_name(docs)
        for doc in docs:
            if doc.get("kind") != "NetworkPolicy":
                continue
            obj = f"{proj_id}/NetworkPolicy/{(doc.get('metadata') or {}).get('name', '')}"
            for rule in (doc.get("spec") or {}).get("egress") or []:
                for name, ports in _gateway_or_signin_egress(rule):
                    gateway_confirm = name == "gateway" and ports == [8080]
                    if choice == "shared" and gateway_confirm:
                        continue
                    if choice != "shared":
                        detail = f"signIn {choice} forbids egress to the gateway or the sign-in service"
                    else:
                        detail = f"shared sign-in egress must be the gateway on port 8080, got {name or 'identity'} ports {ports}"
                    violations.append({"rule": 2, "object": obj, "detail": detail + kept})
        for doc in docs:
            if doc.get("kind") != "IngressRoute":
                continue
            obj = f"{proj_id}/IngressRoute/{(doc.get('metadata') or {}).get('name', '')}"
            for route in (doc.get("spec") or {}).get("routes") or []:
                names = _middleware_names(route)
                if "forward-auth" in names:
                    violations.append({
                        "rule": 2,
                        "object": obj,
                        "detail": "project door must not name forward-auth" + kept,
                    })
                if "strip-forged-headers" not in names:
                    violations.append({
                        "rule": 2,
                        "object": obj,
                        "detail": "project door must name strip-forged-headers" + kept,
                    })
                for svc_ref in route.get("services") or []:
                    svc_name = svc_ref.get("name")
                    foreign_ns = svc_ref.get("namespace")
                    if foreign_ns and foreign_ns != proj_id:
                        violations.append({
                            "rule": 2,
                            "object": obj,
                            "detail": f"service {svc_name} is outside project namespace {proj_id}" + kept,
                        })
                        continue
                    svc = services.get(svc_name)
                    if svc is None:
                        violations.append({
                            "rule": 2,
                            "object": obj,
                            "detail": f"service {svc_name} is not in project namespace {proj_id}" + kept,
                        })
                        continue
                    port = _as_port(svc_ref.get("port"))
                    if port is None or port not in _service_ports(svc):
                        violations.append({
                            "rule": 2,
                            "object": obj,
                            "detail": f"route port {svc_ref.get('port')} is not present on service {svc_name}" + kept,
                        })
                        continue
                    if port not in allowed_ports:
                        violations.append({
                            "rule": 2,
                            "object": obj,
                            "detail": f"route port {port} is not allowed from the platform namespace" + kept,
                        })
        forbidden_auth_envs = {"AUTH_JWT_JWKS_URL", "CENTRAL_AUTH_URL", "CENTRAL_AUTH_INTERNAL_URL"}
        for doc in docs:
            pod_spec, _ = get_pod_spec_and_meta(doc)
            if not pod_spec:
                continue
            obj = f"{proj_id}/{doc.get('kind', '')}/{(doc.get('metadata') or {}).get('name', '')}"
            for c in (pod_spec.get("containers") or []) + (pod_spec.get("initContainers") or []):
                for env_entry in c.get("env") or []:
                    if not isinstance(env_entry, dict):
                        continue
                    ename = str(env_entry.get("name") or "").strip()
                    eval_str = str(env_entry.get("value") or "").strip()
                    if ename in forbidden_auth_envs or "keycloak-service" in eval_str:
                        violations.append({
                            "rule": 2,
                            "object": f"{obj}:{c.get('name', '')}",
                            "detail": f"project container must not hold Keycloak config or JWKS URL ({ename})" + kept,
                        })
    return violations


def check_database_and_readiness_contract(projects_dict, platform_docs):
    """Enforce CONTRACT_DATABASE.md and CONTRACT_PROBES_READINESS.md on database-backed workloads."""
    violations = []
    for proj_id, docs in sorted(projects_dict.items()):
        slug = proj_id[len("proj-"):].replace("-", "_") if proj_id.startswith("proj-") else proj_id.replace("-", "_")
        expected_db = f"proj_{slug}"
        expected_user_prefix = f"project:{slug}:workload:"
        for doc in docs:
            pod_spec, _ = get_pod_spec_and_meta(doc)
            if not pod_spec:
                continue
            obj = f"{proj_id}/{doc.get('kind', '')}/{(doc.get('metadata') or {}).get('name', '')}"
            for c in pod_spec.get("containers") or []:
                c_obj = f"{obj}:{c.get('name', '')}"
                env_map = {}
                for env_entry in c.get("env") or []:
                    if isinstance(env_entry, dict) and env_entry.get("name"):
                        env_map[str(env_entry["name"]).strip()] = str(env_entry.get("value") or "").strip()

                db_keys = {"DATABASE_URL", "DB_HOST", "DB_NAME", "DB_USER", "DB_TOKEN"}
                if not (db_keys & set(env_map.keys())):
                    continue

                # 1. Verify zero-plaintext proxy routing and tenant scope
                db_host = env_map.get("DB_HOST", "")
                if db_host and db_host != "vault-db-proxy.vault.svc":
                    violations.append({
                        "rule": 2,
                        "object": c_obj,
                        "detail": f"DB_HOST must route through vault-db-proxy.vault.svc, got {db_host!r}",
                    })

                db_name = env_map.get("DB_NAME", "")
                if db_name and db_name != expected_db:
                    violations.append({
                        "rule": 2,
                        "object": c_obj,
                        "detail": f"DB_NAME for namespace {proj_id} must be {expected_db!r}, got {db_name!r}",
                    })

                db_user = env_map.get("DB_USER", "")
                if db_user and not db_user.startswith(expected_user_prefix):
                    violations.append({
                        "rule": 2,
                        "object": c_obj,
                        "detail": f"DB_USER must start with {expected_user_prefix!r}, got {db_user!r}",
                    })

                db_url = env_map.get("DATABASE_URL", "")
                if db_url:
                    if "postgres-service" in db_url or "@vault-db-proxy.vault.svc:5432/" + expected_db not in db_url:
                        violations.append({
                            "rule": 2,
                            "object": c_obj,
                            "detail": (
                                f"DATABASE_URL must connect via @vault-db-proxy.vault.svc:5432/{expected_db} "
                                f"and never directly to postgres-service"
                            ),
                        })

                # 2. Verify Readiness (/readyz) vs Liveness (/healthz) Probe Separation
                readiness = c.get("readinessProbe")
                liveness = c.get("livenessProbe")
                r_path = ((readiness or {}).get("httpGet") or {}).get("path")
                l_path = ((liveness or {}).get("httpGet") or {}).get("path")
                if r_path != "/readyz":
                    violations.append({
                        "rule": 2,
                        "object": c_obj,
                        "detail": (
                            f"database-backed project container must declare readinessProbe.httpGet.path: /readyz "
                            f"(got {r_path!r})"
                        ),
                    })
                if liveness is not None and l_path != "/healthz":
                    violations.append({
                        "rule": 2,
                        "object": c_obj,
                        "detail": (
                            f"database-backed project container must declare livenessProbe.httpGet.path: /healthz "
                            f"(got {l_path!r})"
                        ),
                    })

    # Verify vault-db-proxy platform readinessProbe (/readyz) and livenessProbe (/healthz)
    for doc in platform_docs:
        if not isinstance(doc, dict) or doc.get("kind") != "Deployment":
            continue
        meta = doc.get("metadata") or {}
        if meta.get("name") != "vault-db-proxy":
            continue
        pod_spec, _ = get_pod_spec_and_meta(doc)
        if not pod_spec:
            continue
        for c in pod_spec.get("containers") or []:
            if c.get("name") != "db-proxy":
                continue
            r_path = ((c.get("readinessProbe") or {}).get("httpGet") or {}).get("path")
            l_path = ((c.get("livenessProbe") or {}).get("httpGet") or {}).get("path")
            if r_path != "/readyz" or l_path != "/healthz":
                violations.append({
                    "rule": 2,
                    "object": "vault/Deployment/vault-db-proxy:db-proxy",
                    "detail": (
                        f"vault-db-proxy must separate readinessProbe (/readyz) and livenessProbe (/healthz), "
                        f"got readiness={r_path!r} liveness={l_path!r}"
                    ),
                })

    return violations


def check_edge_tls(project_docs, platform_docs):
    """Enforce edge TLS rules TLS-1 through TLS-6 (contracts/preflight-tls-rules.md)."""
    violations = []
    kept = "; last accepted door is not replaced"

    all_docs = platform_docs + project_docs

    # Gather platform certificates and default TLSStore to compute covered_hosts
    platform_certs = []
    for doc in platform_docs:
        if not isinstance(doc, dict):
            continue
        if doc.get("kind") == "Certificate":
            c_ns = doc.get("metadata", {}).get("namespace", "")
            if not c_ns:
                c_ns = doc.get("metadata", {}).get("labels", {}).get("kustomize.toolkit.fluxcd.io/namespace", "")
            if c_ns == "platform":
                platform_certs.append(doc)

    default_stores = []
    all_stores = []
    for doc in all_docs:
        if not isinstance(doc, dict):
            continue
        if doc.get("kind") == "TLSStore":
            all_stores.append(doc)
            s_name = (doc.get("metadata") or {}).get("name")
            if s_name == "default":
                default_stores.append(doc)

    default_store = None
    for s in default_stores:
        s_ns = s.get("metadata", {}).get("namespace", "")
        if not s_ns:
            s_ns = s.get("metadata", {}).get("labels", {}).get("kustomize.toolkit.fluxcd.io/namespace", "")
        if s_ns == "platform":
            default_store = s
            break

    covered_hosts = set()
    if default_store:
        store_spec = default_store.get("spec") or {}
        listed_secrets = {
            c.get("secretName")
            for c in (store_spec.get("certificates") or [])
            if isinstance(c, dict) and c.get("secretName")
        }
        for c in platform_certs:
            c_secret = (c.get("spec") or {}).get("secretName")
            if c_secret in listed_secrets:
                for dns in (c.get("spec") or {}).get("dnsNames") or []:
                    covered_hosts.add(str(dns).strip().lower())

    # TLS-1: Project kinds Certificate, CertificateRequest, Issuer, Order, Challenge, TLSStore, TLSOption, Secret kubernetes.io/tls
    tls_kinds = {"Certificate", "CertificateRequest", "Issuer", "Order", "Challenge", "TLSStore", "TLSOption"}
    for doc in project_docs:
        if not isinstance(doc, dict):
            continue
        kind = doc.get("kind", "")
        ns = doc.get("metadata", {}).get("namespace", "")
        if not ns:
            ns = doc.get("metadata", {}).get("labels", {}).get("kustomize.toolkit.fluxcd.io/namespace", "")
        name = (doc.get("metadata") or {}).get("name", "")
        obj = f"{ns}/{kind}/{name}"
        if kind in tls_kinds:
            violations.append({
                "rule": 2,
                "object": obj,
                "detail": f"TLS objects are platform-owned; {kind} is not allowed in project namespace {ns}" + kept,
            })
        elif kind == "Secret" and (doc.get("type") == "kubernetes.io/tls" or (doc.get("spec") or {}).get("type") == "kubernetes.io/tls"):
            violations.append({
                "rule": 2,
                "object": obj,
                "detail": f"TLS objects are platform-owned; Secret is not allowed in project namespace {ns}" + kept,
            })

    # TLS-2, TLS-3, TLS-4, TLS-5: Project IngressRoute checks
    for doc in project_docs:
        if not isinstance(doc, dict):
            continue
        if doc.get("kind") != "IngressRoute":
            continue
        ns = doc.get("metadata", {}).get("namespace", "")
        if not ns:
            ns = doc.get("metadata", {}).get("labels", {}).get("kustomize.toolkit.fluxcd.io/namespace", "")
        name = (doc.get("metadata") or {}).get("name", "")
        obj = f"{ns}/IngressRoute/{name}"
        spec = doc.get("spec") or {}

        # TLS-2: project IngressRoute whose spec.tls is missing or has any key
        tls_field = spec.get("tls")
        if "tls" not in spec or not isinstance(tls_field, dict) or len(tls_field) > 0:
            violations.append({
                "rule": 2,
                "object": obj,
                "detail": "route must declare tls: {} and let the platform select the certificate" + kept,
            })

        # TLS-3: spec.entryPoints != ["websecure"]
        entry_points = spec.get("entryPoints")
        if entry_points != ["websecure"]:
            violations.append({
                "rule": 2,
                "object": obj,
                "detail": "project route entry point must be websecure only" + kept,
            })

        # TLS-4 & TLS-5: route match and covered_hosts
        for route in spec.get("routes") or []:
            match_expr = route.get("match", "")
            has_forbidden = any(token in match_expr for token in ("HostRegexp", "HostSNI", "HostHeader"))
            host_groups = re.findall(r"Host\(([^)]*)\)", match_expr)
            extracted_hosts = []
            for grp in host_groups:
                for h in re.findall(r"`([^`]+)`", grp):
                    h_clean = h.strip()
                    if h_clean:
                        extracted_hosts.append(h_clean)

            if has_forbidden or not extracted_hosts:
                violations.append({
                    "rule": 2,
                    "object": obj,
                    "detail": "route match must name its hostname with Host()" + kept,
                })
            else:
                for h in extracted_hosts:
                    if h.lower() not in covered_hosts:
                        violations.append({
                            "rule": 2,
                            "object": obj,
                            "detail": f"hostname {h} has no platform certificate; add it to deploy/platform/tls first" + kept,
                        })

    # TLS-2 (platform): websecure IngressRoutes in platform must also declare tls: {}
    for doc in platform_docs:
        if not isinstance(doc, dict) or doc.get("kind") != "IngressRoute":
            continue
        meta = doc.get("metadata") or {}
        ns = meta.get("namespace", "")
        if not ns:
            ns = (meta.get("labels") or {}).get("kustomize.toolkit.fluxcd.io/namespace", "")
        if ns != "platform":
            continue
        spec = doc.get("spec") or {}
        if "websecure" not in (spec.get("entryPoints") or []):
            continue
        tls_field = spec.get("tls")
        if "tls" not in spec or not isinstance(tls_field, dict) or len(tls_field) > 0:
            violations.append({
                "rule": 2,
                "object": f"platform/IngressRoute/{meta.get('name', '')}",
                "detail": "route must declare tls: {} and let the platform select the certificate",
            })

    # TLS-6: Certificate inventory invariants (Platform render)
    # Check stores outside platform or multiple defaults
    for s in all_stores:
        s_ns = s.get("metadata", {}).get("namespace", "")
        if not s_ns:
            s_ns = s.get("metadata", {}).get("labels", {}).get("kustomize.toolkit.fluxcd.io/namespace", "")
        s_name = (s.get("metadata") or {}).get("name", "")
        if s_ns != "platform":
            violations.append({
                "rule": 2,
                "object": f"{s_ns}/TLSStore/{s_name}",
                "detail": f"certificate inventory: TLSStore outside platform namespace ({s_ns})",
            })

    if len(default_stores) > 1:
        violations.append({
            "rule": 2,
            "object": "platform/TLSStore/default",
            "detail": "certificate inventory: more than one TLSStore named default",
        })

    if default_store:
        store_spec = default_store.get("spec") or {}
        if store_spec.get("defaultCertificate") is not None or store_spec.get("defaultGeneratedCert") is not None:
            violations.append({
                "rule": 2,
                "object": "platform/TLSStore/default",
                "detail": "certificate inventory: defaultCertificate and defaultGeneratedCert must be unset",
            })

        platform_cert_secrets = {
            (c.get("spec") or {}).get("secretName")
            for c in platform_certs
            if (c.get("spec") or {}).get("secretName")
        }
        for cert_entry in store_spec.get("certificates") or []:
            s_name = cert_entry.get("secretName") if isinstance(cert_entry, dict) else None
            if s_name and s_name not in platform_cert_secrets:
                violations.append({
                    "rule": 2,
                    "object": "platform/TLSStore/default",
                    "detail": f"certificate inventory: listed secretName '{s_name}' has no matching Certificate in platform",
                })

    # Check hostname duplicate across platform certificates
    host_to_certs = {}
    for c in platform_certs:
        c_name = (c.get("metadata") or {}).get("name", "")
        for dns in (c.get("spec") or {}).get("dnsNames") or []:
            dns_clean = str(dns).strip().lower()
            host_to_certs.setdefault(dns_clean, []).append(c_name)
    for host, c_names in host_to_certs.items():
        if len(c_names) > 1:
            violations.append({
                "rule": 2,
                "object": f"platform/Certificate/{c_names[0]}",
                "detail": f"certificate inventory: hostname '{host}' appears in multiple certificates: {', '.join(c_names)}",
            })

    # Check edge Certificate outside platform (in platform namespaces other than platform)
    for doc in all_docs:
        if not isinstance(doc, dict):
            continue
        if doc.get("kind") == "Certificate":
            c_ns = doc.get("metadata", {}).get("namespace", "")
            if not c_ns:
                c_ns = doc.get("metadata", {}).get("labels", {}).get("kustomize.toolkit.fluxcd.io/namespace", "")
            c_name = (doc.get("metadata") or {}).get("name", "")
            if c_ns and c_ns != "platform" and c_ns in PLATFORM_NAMESPACES:
                violations.append({
                    "rule": 2,
                    "object": f"{c_ns}/Certificate/{c_name}",
                    "detail": f"certificate inventory: Certificate '{c_name}' outside platform namespace ({c_ns})",
                })

    return violations


def get_pod_spec_and_meta(doc):
    kind = doc.get("kind", "")
    spec = doc.get("spec", {})
    if kind in ("Deployment", "StatefulSet", "DaemonSet", "Job"):
        template = spec.get("template", {})
        return template.get("spec", {}), template.get("metadata", {})
    elif kind == "CronJob":
        job_template = spec.get("jobTemplate", {}).get("spec", {})
        template = job_template.get("template", {})
        return template.get("spec", {}), template.get("metadata", {})
    elif kind == "Pod":
        return spec, doc.get("metadata", {})
    return None, None


def main():
    parser = argparse.ArgumentParser(description="Platform Preflight Required Check")
    parser.add_argument("--environment", required=True, help="Target environment (e.g. local-k3s, vps-k3s)")
    parser.add_argument("--candidate", required=True, help="Path to rendered candidate YAML")
    parser.add_argument("--baseline", help="Path to rendered baseline YAML (optional)")
    parser.add_argument("--capacity", required=True, help="Path to capacity.yaml")
    parser.add_argument("--json", action="store_true", help="Output JSON result")

    args = parser.parse_args()

    violations = []
    rejection_reason = ""
    status = STATUS_PASSED
    exit_code = 0

    # Rule 1: Capacity validation
    cap_data = None
    if not os.path.exists(args.capacity):
        status = STATUS_REJECTED_NO_MEASUREMENT
        rejection_reason = f"Capacity file not found at {args.capacity}"
        exit_code = 1
    else:
        try:
            with open(args.capacity, "r", encoding="utf-8") as f:
                cap_data = yaml.safe_load(f)
        except Exception:
            sys.exit(2)

    if cap_data is not None:
        env = cap_data.get("environment")
        alloc = cap_data.get("allocatable", {})
        sys_res = cap_data.get("systemReserved", {})
        alloc_cpu = alloc.get("cpuMillicores", 0)
        alloc_mem = alloc.get("memoryMib", 0)
        sys_cpu = sys_res.get("cpuMillicores", 0)
        sys_mem = sys_res.get("memoryMib", 0)

        if env != args.environment or alloc_cpu <= 0 or alloc_mem <= 0 or sys_cpu <= 0 or sys_mem <= 0:
            status = STATUS_REJECTED_NO_MEASUREMENT
            rejection_reason = "Invalid capacity: environment mismatch or non-positive measurements"
            exit_code = 1

    candidate_docs = load_yaml_docs(args.candidate)
    baseline_docs = load_yaml_docs(args.baseline) if args.baseline else []

    # If Rule 1 already failed, skip checking other rules
    if status == STATUS_PASSED:
        # Separate documents by layer/namespace
        project_docs = []
        platform_docs = []
        for doc in candidate_docs:
            ns = doc.get("metadata", {}).get("namespace", "")
            # check flux label if metadata.namespace missing
            if not ns:
                ns = doc.get("metadata", {}).get("labels", {}).get("kustomize.toolkit.fluxcd.io/namespace", "")
            if ns and ns not in PLATFORM_NAMESPACES:
                project_docs.append(doc)
            else:
                platform_docs.append(doc)

        # Rule 2: Isolation in project namespaces
        for doc in project_docs:
            kind = doc.get("kind", "")
            obj_name = f"{doc.get('metadata', {}).get('namespace', '')}/{kind}/{doc.get('metadata', {}).get('name', '')}"
            if kind == "Service":
                svc_type = doc.get("spec", {}).get("type", "ClusterIP")
                if svc_type in ("NodePort", "LoadBalancer"):
                    violations.append({"rule": 2, "object": obj_name, "detail": f"Service type {svc_type} prohibited in project namespace"})
            
            pod_spec, _ = get_pod_spec_and_meta(doc)
            if pod_spec:
                if pod_spec.get("hostNetwork") or pod_spec.get("hostPID") or pod_spec.get("hostIPC"):
                    violations.append({"rule": 2, "object": obj_name, "detail": "hostNetwork/hostPID/hostIPC prohibited"})
                
                # automountServiceAccountToken MUST be false on pods
                if pod_spec.get("automountServiceAccountToken") is not False:
                    violations.append({"rule": 2, "object": obj_name, "detail": "automountServiceAccountToken must be false on project pods"})
                
                # Check volumes for hostPath
                for vol in pod_spec.get("volumes", []):
                    if "hostPath" in vol:
                        violations.append({"rule": 2, "object": obj_name, "detail": f"hostPath volume '{vol.get('name')}' prohibited"})
                
                # Check containers
                all_c = pod_spec.get("containers", []) + pod_spec.get("initContainers", [])
                for c in all_c:
                    sc = c.get("securityContext", {})
                    if sc.get("privileged") is True:
                        violations.append({"rule": 2, "object": f"{obj_name}:{c.get('name')}", "detail": "privileged: true prohibited"})
                    if sc.get("allowPrivilegeEscalation") is True:
                        violations.append({"rule": 2, "object": f"{obj_name}:{c.get('name')}", "detail": "allowPrivilegeEscalation: true prohibited"})
                    caps = sc.get("capabilities", {})
                    add_caps = caps.get("add", [])
                    for cap in add_caps:
                        if cap != "NET_BIND_SERVICE":
                            violations.append({"rule": 2, "object": f"{obj_name}:{c.get('name')}", "detail": f"capability add {cap} prohibited"})

        if violations and status == STATUS_PASSED:
            status = STATUS_REJECTED_ISOLATION
            rejection_reason = violations[0]["detail"]
            exit_code = 1

        # Rule 3: Strict QoS in project layers (requests == limits)
        if status == STATUS_PASSED:
            for doc in project_docs:
                pod_spec, _ = get_pod_spec_and_meta(doc)
                if pod_spec:
                    obj_name = f"{doc.get('metadata', {}).get('namespace', '')}/{doc.get('kind', '')}/{doc.get('metadata', {}).get('name', '')}"
                    all_c = pod_spec.get("containers", []) + pod_spec.get("initContainers", [])
                    for c in all_c:
                        res = c.get("resources", {})
                        req = res.get("requests", {})
                        lim = res.get("limits", {})
                        cpu_req = parse_cpu(req.get("cpu"))
                        cpu_lim = parse_cpu(lim.get("cpu"))
                        mem_req = parse_memory_mib(req.get("memory"))
                        mem_lim = parse_memory_mib(lim.get("memory"))
                        if cpu_req is None or cpu_lim is None or cpu_req != cpu_lim or mem_req is None or mem_lim is None or mem_req != mem_lim:
                            status = STATUS_REJECTED_QOS
                            rejection_reason = f"Container {obj_name}:{c.get('name')} requests not equal to limits"
                            violations.append({"rule": 3, "object": f"{obj_name}:{c.get('name')}", "detail": rejection_reason})
                            exit_code = 1
                            break
                    if status != STATUS_PASSED:
                        break

        # Rule 4: Every container across all layers declares requests and limits
        if status == STATUS_PASSED:
            for doc in candidate_docs:
                pod_spec, _ = get_pod_spec_and_meta(doc)
                if pod_spec:
                    obj_name = f"{doc.get('metadata', {}).get('namespace', '')}/{doc.get('kind', '')}/{doc.get('metadata', {}).get('name', '')}"
                    all_c = pod_spec.get("containers", []) + pod_spec.get("initContainers", [])
                    for c in all_c:
                        res = c.get("resources", {})
                        req = res.get("requests", {})
                        lim = res.get("limits", {})
                        if not req.get("cpu") or not lim.get("cpu") or not req.get("memory") or not lim.get("memory"):
                            status = STATUS_REJECTED_UNBOUNDED
                            rejection_reason = f"Container {obj_name}:{c.get('name')} unbounded (missing requests or limits)"
                            violations.append({"rule": 4, "object": f"{obj_name}:{c.get('name')}", "detail": rejection_reason})
                            exit_code = 1
                            break
                    if status != STATUS_PASSED:
                        break

        # Rule 5: Memory freeze vs baseline
        if status == STATUS_PASSED and baseline_docs:
            baseline_containers = {}
            for doc in baseline_docs:
                ns = doc.get("metadata", {}).get("namespace", "")
                if ns and ns not in PLATFORM_NAMESPACES:
                    pod_spec, _ = get_pod_spec_and_meta(doc)
                    if pod_spec:
                        obj_name = f"{ns}/{doc.get('kind', '')}/{doc.get('metadata', {}).get('name', '')}"
                        all_c = pod_spec.get("containers", []) + pod_spec.get("initContainers", [])
                        for c in all_c:
                            mem_lim = parse_memory_mib(c.get("resources", {}).get("limits", {}).get("memory"))
                            baseline_containers[f"{obj_name}:{c.get('name')}"] = mem_lim

            for doc in project_docs:
                pod_spec, _ = get_pod_spec_and_meta(doc)
                if pod_spec:
                    obj_name = f"{doc.get('metadata', {}).get('namespace', '')}/{doc.get('kind', '')}/{doc.get('metadata', {}).get('name', '')}"
                    all_c = pod_spec.get("containers", []) + pod_spec.get("initContainers", [])
                    for c in all_c:
                        key = f"{obj_name}:{c.get('name')}"
                        if key in baseline_containers and baseline_containers[key] is not None:
                            cand_mem = parse_memory_mib(c.get("resources", {}).get("limits", {}).get("memory"))
                            if cand_mem is not None and cand_mem < baseline_containers[key]:
                                status = STATUS_REJECTED_MEMORY_SHRINK
                                rejection_reason = f"Container {key} memory limit shrunk from {baseline_containers[key]}Mi to {cand_mem}Mi"
                                violations.append({"rule": 5, "object": key, "detail": rejection_reason})
                                exit_code = 1
                                break
                    if status != STATUS_PASSED:
                        break

    # Calculations for Rule 6 and JSON output
    alloc_cpu = cap_data.get("allocatable", {}).get("cpuMillicores", 0) if cap_data else 0
    alloc_mem = cap_data.get("allocatable", {}).get("memoryMib", 0) if cap_data else 0
    sys_cpu = cap_data.get("systemReserved", {}).get("cpuMillicores", 0) if cap_data else 0
    sys_mem = cap_data.get("systemReserved", {}).get("memoryMib", 0) if cap_data else 0

    plat_req_cpu = sys_cpu
    plat_lim_mem = sys_mem

    if cap_data is not None:
        for doc in candidate_docs:
            ns = doc.get("metadata", {}).get("namespace", "")
            if not ns:
                ns = doc.get("metadata", {}).get("labels", {}).get("kustomize.toolkit.fluxcd.io/namespace", "")
            if not ns or ns in PLATFORM_NAMESPACES:
                pod_spec, _ = get_pod_spec_and_meta(doc)
                if pod_spec:
                    all_c = pod_spec.get("containers", []) + pod_spec.get("initContainers", [])
                    for c in all_c:
                        res = c.get("resources", {})
                        cpu = parse_cpu(res.get("requests", {}).get("cpu"))
                        mem = parse_memory_mib(res.get("limits", {}).get("memory"))
                        if cpu:
                            plat_req_cpu += cpu
                        if mem:
                            plat_lim_mem += mem

    app_cap_cpu = alloc_cpu - plat_req_cpu
    app_cap_mem = alloc_mem - plat_lim_mem

    if cap_data is not None and (app_cap_cpu <= 0 or app_cap_mem <= 0) and status == STATUS_PASSED:
        status = STATUS_REJECTED_NO_MEASUREMENT
        rejection_reason = "Platform reservation exceeds allocatable capacity"
        exit_code = 1

    # Project groupings
    projects_dict = {}
    for doc in candidate_docs:
        ns = doc.get("metadata", {}).get("namespace", "")
        if not ns:
            ns = doc.get("metadata", {}).get("labels", {}).get("kustomize.toolkit.fluxcd.io/namespace", "")
        if ns and ns not in PLATFORM_NAMESPACES:
            if ns not in projects_dict:
                projects_dict[ns] = []
            projects_dict[ns].append(doc)

    if status == STATUS_PASSED:
        door_violations = check_project_doors(projects_dict)
        if door_violations:
            violations.extend(door_violations)
            status = STATUS_REJECTED_ISOLATION
            rejection_reason = door_violations[0]["detail"]
            exit_code = 1

        tls_violations = check_edge_tls(project_docs, platform_docs)
        if tls_violations:
            violations.extend(tls_violations)
            status = STATUS_REJECTED_ISOLATION
            if not rejection_reason:
                rejection_reason = tls_violations[0]["detail"]
            exit_code = 1

        db_probe_violations = check_database_and_readiness_contract(projects_dict, platform_docs)
        if db_probe_violations:
            violations.extend(db_probe_violations)
            status = STATUS_REJECTED_ISOLATION
            if not rejection_reason:
                rejection_reason = db_probe_violations[0]["detail"]
            exit_code = 1

    project_count = len(projects_dict)
    slice_cpu = app_cap_cpu / project_count if project_count > 0 else app_cap_cpu
    slice_mem = app_cap_mem / project_count if project_count > 0 else app_cap_mem

    projects_summary = []
    candidate_peak_cpu = 0
    candidate_peak_mem = 0

    for proj_id, docs in sorted(projects_dict.items()):
        proj_cpu = 0
        proj_mem = 0
        for doc in docs:
            kind = doc.get("kind", "")
            spec = doc.get("spec", {})
            pod_spec, _ = get_pod_spec_and_meta(doc)
            if pod_spec:
                c_cpu = sum(parse_cpu(c.get("resources", {}).get("requests", {}).get("cpu")) or 0 for c in pod_spec.get("containers", []))
                init_c_cpu = max([parse_cpu(c.get("resources", {}).get("requests", {}).get("cpu")) or 0 for c in pod_spec.get("initContainers", [])] or [0])
                pod_cpu = c_cpu + init_c_cpu

                c_mem = sum(parse_memory_mib(c.get("resources", {}).get("requests", {}).get("memory")) or 0 for c in pod_spec.get("containers", []))
                init_c_mem = max([parse_memory_mib(c.get("resources", {}).get("requests", {}).get("memory")) or 0 for c in pod_spec.get("initContainers", [])] or [0])
                pod_mem = c_mem + init_c_mem

                if kind == "Deployment":
                    replicas = spec.get("replicas", 1)
                    strategy = spec.get("strategy", {})
                    if strategy.get("type") == "Recreate":
                        multiplier = replicas
                    else:
                        surge = strategy.get("rollingUpdate", {}).get("maxSurge", "25%")
                        if isinstance(surge, str) and surge.endswith("%"):
                            surge_val = math.ceil(replicas * float(surge[:-1]) / 100.0)
                        else:
                            surge_val = int(surge)
                        multiplier = replicas + surge_val
                    proj_cpu += pod_cpu * multiplier
                    proj_mem += pod_mem * multiplier
                elif kind == "StatefulSet":
                    replicas = spec.get("replicas", 1)
                    proj_cpu += pod_cpu * replicas
                    proj_mem += pod_mem * replicas
                elif kind == "Job":
                    parallelism = spec.get("parallelism", 1)
                    proj_cpu += pod_cpu * parallelism
                    proj_mem += pod_mem * parallelism
                elif kind == "CronJob":
                    concurrency = spec.get("concurrencyPolicy", "Allow")
                    multiplier = 2 if concurrency == "Allow" else 1
                    proj_cpu += pod_cpu * multiplier
                    proj_mem += pod_mem * multiplier
                elif kind == "Pod":
                    proj_cpu += pod_cpu
                    proj_mem += pod_mem

        projects_summary.append({
            "id": proj_id,
            "peak": {
                "cpuMillicores": int(round(proj_cpu)),
                "memoryMib": int(round(proj_mem))
            }
        })
        if proj_cpu > candidate_peak_cpu:
            candidate_peak_cpu = proj_cpu
        if proj_mem > candidate_peak_mem:
            candidate_peak_mem = proj_mem

        # Rule 6 check
        if status == STATUS_PASSED:
            if proj_cpu > slice_cpu or proj_mem > slice_mem:
                status = STATUS_REJECTED_OVER_BUDGET
                rejection_reason = f"Project '{proj_id}' peak ({proj_cpu}m, {proj_mem}Mi) exceeds slice ({slice_cpu}m, {slice_mem}Mi)"
                violations.append({"rule": 6, "object": f"project/{proj_id}", "detail": rejection_reason})
                exit_code = 1

    result_obj = {
        "environment": args.environment,
        "measuredAllocatable": {
            "cpuMillicores": alloc_cpu,
            "memoryMib": alloc_mem
        },
        "platformReservations": {
            "cpuMillicores": int(round(plat_req_cpu)),
            "memoryMib": int(round(plat_lim_mem))
        },
        "projectCount": project_count,
        "calculatedPerProjectBudget": {
            "cpuMillicores": int(round(slice_cpu)),
            "memoryMib": int(round(slice_mem)),
            "strictQoS": True
        },
        "candidatePeakFootprint": {
            "cpuMillicores": int(round(candidate_peak_cpu)),
            "memoryMib": int(round(candidate_peak_mem))
        },
        "projects": projects_summary,
        "validationStatus": status,
        "rejectionReason": rejection_reason,
        "violations": violations
    }

    if args.json:
        print(json.dumps(result_obj, indent=2))
    else:
        print(f"Platform Preflight Status: {status}")
        if rejection_reason:
            print(f"Reason: {rejection_reason}")
        if violations:
            print("Violations:")
            for v in violations:
                print(f"  - [Rule {v['rule']}] {v['object']}: {v['detail']}")
        print(f"Allocatable: CPU={alloc_cpu}m, Mem={alloc_mem}Mi")
        print(f"Platform Reservation: CPU={plat_req_cpu}m, Mem={plat_lim_mem}Mi")
        print(f"Project Count: {project_count}, Slice: CPU={slice_cpu}m, Mem={slice_mem}Mi")

    sys.exit(exit_code)


if __name__ == "__main__":
    main()
