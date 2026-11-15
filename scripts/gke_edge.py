"""Single source for the GKE edge rules (Spec 015, specs/015-gke-terraform-infra/contracts/traefik-l4-edge.md).

Used by scripts/platform-preflight.py (Rule 2), scripts/render-invariants.py (Invariant 10) and
scripts/check-gcp-shared-values.py (check 8), so the three can never disagree.
"""

EDGE_NAMESPACE = "platform"
EDGE_NAME = "traefik"
LB_CLASS = "networking.gke.io/l4-regional-external"
LB_ADDRESS_ANNOTATION = "networking.gke.io/load-balancer-ip-addresses"


def is_edge_service(doc) -> bool:
    """True for Service platform/traefik, whatever its spec."""
    meta = doc.get("metadata") or {}
    return doc.get("kind") == "Service" and meta.get("namespace") == EDGE_NAMESPACE and meta.get("name") == EDGE_NAME


def edge_service_problems(doc) -> list[str]:
    """Reasons Service platform/traefik does not match the edge contract (empty list = valid)."""
    meta = doc.get("metadata") or {}
    spec = doc.get("spec") or {}
    problems = []
    if spec.get("type", "ClusterIP") != "LoadBalancer":
        problems.append(f"type must be 'LoadBalancer', got '{spec.get('type', 'ClusterIP')}'")
    if spec.get("loadBalancerClass") != LB_CLASS:
        problems.append(f"loadBalancerClass must be '{LB_CLASS}', got '{spec.get('loadBalancerClass')}'")
    if spec.get("externalTrafficPolicy") != "Local":
        problems.append(f"externalTrafficPolicy must be 'Local', got '{spec.get('externalTrafficPolicy')}'")
    if not (meta.get("annotations") or {}).get(LB_ADDRESS_ANNOTATION):
        problems.append(f"needs a non-empty '{LB_ADDRESS_ANNOTATION}' annotation")
    return problems


def is_allowed_edge_lb(doc) -> bool:
    """True only for the single allowed LoadBalancer: platform/traefik matching the contract."""
    return is_edge_service(doc) and not edge_service_problems(doc)
