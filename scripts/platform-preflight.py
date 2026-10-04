#!/usr/bin/env python3
"""
Platform Preflight Check (SPEC.md DEPLOY-04, contracts/platform-preflight.md).
Validates candidate renders against capacity, isolation, QoS, memory freeze, and project budget.
"""

import argparse
import json
import math
import os
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

PLATFORM_NAMESPACES = {"flux-system", "platform", "vault", "identity", "governance", "kube-system"}


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
