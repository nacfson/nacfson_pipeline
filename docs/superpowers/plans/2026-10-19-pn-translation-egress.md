# Project PN Translation Egress Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let only `pn-backend` reach `api.wiktapi.dev` and `generativelanguage.googleapis.com` on TCP 443, and keep every other public destination dropped.

**Architecture:** Leave `NetworkPolicy/project-pn-isolation` as the portable default-deny list. Add a platform-owned `CiliumNetworkPolicy/pn-backend-translation-egress` in the project-boundaries layer, which must wait for the `cni` layer. Cilium `toFQDNs` names the hosts. A DNS `matchPattern: "*"` in that same policy keeps in-cluster lookups working. No world fallback.

**Tech Stack:** Cilium `v1.20.3` `CiliumNetworkPolicy` (`cilium.io/v2`), Flux `project-boundaries`, Python unittest, `platform-preflight.py`.

**Spec:** `docs/superpowers/specs/2026-10-19-pn-translation-egress-design.md`

## Global Constraints

- Hosts are exactly `api.wiktapi.dev` and `generativelanguage.googleapis.com`. Both are allowed now, even when `ENRICH_PRIMARY_API_KEY` is unset. That is a planner call in the spec, not an owner confirmation.
- FQDN rule port is `"443"`, protocol `TCP`. No UDP 443. No other `matchName`.
- Selector is exactly `app.kubernetes.io/name: pn-backend` in namespace `proj-pn`.
- DNS rule is kube-dns port `"53"` protocol `ANY` with the only `matchPattern` equal to `"*"`.
- No `ingress` section. No `0.0.0.0/0`. No `toEntities: ["world"]`. No `ipBlock` on `project-pn-isolation`.
- Do not set `ENRICH_PRIMARY_API_KEY`, `ENRICH_PRIMARY_MODEL`, or `ENRICH_FALLBACK_BASE_URL`.
- Do not edit `identity`, `platform`, or `registry-proxy-policy`.
- Do not convert `project-pn-isolation` to `CiliumNetworkPolicy`.
- Do not put the new object under `deploy/projects/pn/workloads/`.
- Branch from `origin/main` at `766222d`. Do not commit the dirty files already on the local `main` checkout.
- A commit that changes `deploy/`, `clusters/`, `contracts/`, `ARCHITECTURE.md`, or `scripts/platform-preflight.py` must also change `docs/platform-architecture.html`.
- Do not close GitHub issue #62.

## Review Focus

- A DNS rule limited to the two public names would refuse `vault-db-proxy.vault.svc` and `gateway.identity.svc`. Task 1 requires the only `matchPattern` to be `"*"`.
- An extra `matchName`, `matchPattern` on a public suffix, or `0.0.0.0/0` would open more than translation. Task 1 pins the host set. Task 2 rejects any other host and any world CIDR.
- A selector of `{}` or `pn-frontend` would let the site pod call those hosts. Task 1 requires `app.kubernetes.io/name: pn-backend` only.
- Copying the OpenBao RFC1918 fallback onto port 443 would open the internet on a kube-router node. Task 1 requires `project-pn-isolation` to contain no `443` and no `ipBlock`. The handbook Flannel mode must show this scenario dropped.
- Applying the policy before the Cilium CRD exists fails a fresh install, and a later edit can drop `dependsOn: cni`. Task 1 requires both cluster layer files to depend on `cni` and `governance`.

---

### Task 1: Declare the two-host allow and prove the files

**Files:**
- Create: `tests/preflight/test_translation_egress.py`
- Create: `deploy/projects/pn/boundary/translation-egress.yaml`
- Modify: `deploy/projects/pn/boundary/kustomization.yaml`
- Modify: `clusters/local-k3s/layers.yaml`
- Modify: `clusters/vps-k3s/layers.yaml`
- Modify: `contracts/CONTRACT_ISOLATION_BUDGET.md`
- Modify: `ARCHITECTURE.md`
- Modify: `docs/platform-architecture.html`
- Test: `tests/preflight/test_translation_egress.py`

**Interfaces:**
- Consumes: the Policy section of the spec. Copy that object. Do not invent fields.
- Produces: `CiliumNetworkPolicy/pn-backend-translation-egress` in namespace `proj-pn`, selected by `project-boundaries`, with `dependsOn` names `cni` and `governance`. Task 2 reads that object from the rendered candidate.

- [ ] **Step 1: Branch from origin/main**

```bash
git fetch origin main
git switch -c fix/pn-translation-egress origin/main
```

Expected: `git rev-parse --short HEAD` prints `766222d`. `git status --short` is empty. Do not carry the dirty files from the previous checkout.

- [ ] **Step 2: Write the failing test**

Create `tests/preflight/test_translation_egress.py`:

```python
import os
import unittest

import yaml

BASE = os.path.abspath(os.path.join(os.path.dirname(__file__), "..", ".."))
POLICY = os.path.join(BASE, "deploy", "projects", "pn", "boundary", "translation-egress.yaml")
PORTABLE = os.path.join(BASE, "deploy", "projects", "pn", "boundary", "network-policy.yaml")
KUSTOMIZATION = os.path.join(BASE, "deploy", "projects", "pn", "boundary", "kustomization.yaml")
BACKEND = os.path.join(BASE, "deploy", "projects", "pn", "workloads", "backend-deployment.yaml")
HANDBOOK = os.path.join(BASE, "docs", "platform-architecture.html")
HOSTS = frozenset({"api.wiktapi.dev", "generativelanguage.googleapis.com"})


def _load(path):
    with open(path, encoding="utf-8") as fh:
        return yaml.safe_load(fh)


def _walk_key(node, key, found):
    if isinstance(node, dict):
        for item_key, item_value in node.items():
            if item_key == key:
                found.append(item_value)
            else:
                _walk_key(item_value, key, found)
    elif isinstance(node, list):
        for item in node:
            _walk_key(item, key, found)


class TestPnTranslationEgress(unittest.TestCase):
    def test_policy_names_only_the_two_translation_hosts(self):
        doc = _load(POLICY)
        self.assertEqual(doc["apiVersion"], "cilium.io/v2")
        self.assertEqual(doc["kind"], "CiliumNetworkPolicy")
        self.assertEqual(doc["metadata"]["name"], "pn-backend-translation-egress")
        self.assertEqual(doc["metadata"]["namespace"], "proj-pn")
        self.assertEqual(
            doc["spec"]["endpointSelector"]["matchLabels"],
            {"app.kubernetes.io/name": "pn-backend"},
        )
        self.assertNotIn("ingress", doc["spec"])
        rules = doc["spec"]["egress"]
        self.assertEqual(len(rules), 2)
        dns = next(rule for rule in rules if "toEndpoints" in rule)
        self.assertEqual(
            dns["toEndpoints"],
            [
                {
                    "matchLabels": {
                        "k8s:io.kubernetes.pod.namespace": "kube-system",
                        "k8s:k8s-app": "kube-dns",
                    }
                }
            ],
        )
        self.assertEqual(
            dns["toPorts"][0]["ports"],
            [{"port": "53", "protocol": "ANY"}],
        )
        fqdn = next(rule for rule in rules if "toFQDNs" in rule)
        self.assertEqual({item["matchName"] for item in fqdn["toFQDNs"]}, HOSTS)
        self.assertEqual(
            fqdn["toPorts"],
            [{"ports": [{"port": "443", "protocol": "TCP"}]}],
        )
        patterns = []
        names = []
        _walk_key(doc, "matchPattern", patterns)
        _walk_key(doc, "matchName", names)
        self.assertEqual(patterns, ["*"])
        self.assertEqual(set(names), HOSTS)
        dumped = yaml.dump(doc)
        self.assertNotIn("0.0.0.0/0", dumped)
        self.assertNotIn("world", dumped)

    def test_portable_policy_has_no_public_egress(self):
        doc = _load(PORTABLE)
        self.assertEqual(doc["kind"], "NetworkPolicy")
        self.assertEqual(doc["metadata"]["name"], "project-pn-isolation")
        dumped = yaml.dump(doc)
        self.assertNotIn("443", dumped)
        self.assertNotIn("ipBlock", dumped)
        self.assertNotIn("toFQDNs", dumped)
        self.assertNotIn("0.0.0.0/0", dumped)

    def test_boundary_kustomization_includes_the_policy(self):
        doc = _load(KUSTOMIZATION)
        self.assertIn("translation-egress.yaml", doc["resources"])

    def test_project_boundaries_wait_for_cilium(self):
        for env in ("local-k3s", "vps-k3s"):
            path = os.path.join(BASE, "clusters", env, "layers.yaml")
            with open(path, encoding="utf-8") as fh:
                docs = [item for item in yaml.safe_load_all(fh) if item]
            layer = next(
                item
                for item in docs
                if item.get("kind") == "Kustomization"
                and item.get("metadata", {}).get("name") == "project-boundaries"
            )
            names = [item["name"] for item in layer["spec"]["dependsOn"]]
            self.assertIn("cni", names)
            self.assertIn("governance", names)

    def test_backend_does_not_receive_the_enrich_key(self):
        with open(BACKEND, encoding="utf-8") as fh:
            text = fh.read()
        self.assertNotIn("ENRICH_PRIMARY_API_KEY", text)
        self.assertNotIn("ENRICH_FALLBACK_BASE_URL", text)

    def test_handbook_drops_translation_on_flannel(self):
        with open(HANDBOOK, encoding="utf-8") as fh:
            text = fh.read()
        self.assertIn("api.wiktapi.dev", text)
        self.assertIn("generativelanguage.googleapis.com", text)
        self.assertIn("pn-translation", text)
        self.assertIn("currentCniScenario === 'pn-translation'", text)
```

- [ ] **Step 3: Run the test to verify it fails**

Run: `python3 -m unittest tests.preflight.test_translation_egress -v`

Expected: FAIL because `translation-egress.yaml` does not exist.

- [ ] **Step 4: Add the policy and the layer wait**

Create `deploy/projects/pn/boundary/translation-egress.yaml` as the Policy section of the spec, with no added fields.

Add `translation-egress.yaml` to `resources` in `deploy/projects/pn/boundary/kustomization.yaml`.

In both `clusters/local-k3s/layers.yaml` and `clusters/vps-k3s/layers.yaml`, change the `project-boundaries` `dependsOn` from only `governance` to `cni` then `governance`. Do not remove `governance`. Do not add `postBuild`.

- [ ] **Step 5: Update the contract and the two architecture docs**

In `contracts/CONTRACT_ISOLATION_BUDGET.md` §2, after the intra-namespace egress bullet, add:

```markdown
- **Translation egress (Project PN backend only):** not a rule in the portable `NetworkPolicy`. `CiliumNetworkPolicy/pn-backend-translation-egress` (`deploy/projects/pn/boundary/translation-egress.yaml`) selects only `app.kubernetes.io/name: pn-backend` and allows TCP 443 to exactly `api.wiktapi.dev` and `generativelanguage.googleapis.com`. DNS to kube-dns on port 53 uses `matchPattern: "*"` so in-cluster names still resolve. No other public name, no `0.0.0.0/0`, and no kube-router world fallback. Enforced only while Cilium owns the pod.
```

In `ARCHITECTURE.md` §3.6, after the OpenBao egress bullet, add one bullet: portable `proj-*` policies stay on `networking.k8s.io/v1`; `CiliumNetworkPolicy/pn-backend-translation-egress` names only those two hosts on TCP 443 for `pn-backend`; other world destinations stay dropped; no `0.0.0.0/0` fallback.

In `docs/platform-architecture.html`:

- On the boundary node, keep the in-cluster egress lines. Add two lines: `api.wiktapi.dev:443` and `generativelanguage.googleapis.com:443`, labeled as Cilium FQDN allows for `pn-backend` only. Grow the box so the text is not clipped. Change the "Egress ONLY to" line so it no longer claims those in-cluster lines are the only egress.
- In the `pn-boundary` inspector bullets, name the same two hosts and say other world destinations stay dropped.
- In the Flux DAG `project-boundaries` entry, set `deps` and both environment YAML strings to `dependsOn: [cni, governance]`, and list `CiliumNetworkPolicy pn-backend-translation-egress` next to `NetworkPolicy project-pn-isolation`.
- Add `CNI_SCENARIOS['pn-translation']` with `allowed: true`, destination `api.wiktapi.dev:443 and generativelanguage.googleapis.com:443`, and a drop explanation for any other world host. Leave `pn-internet` as a drop for `1.1.1.1:443`.
- In the Flannel branch of `renderCniSim`, before the generic `s.allowed` badge, if `currentCniScenario === 'pn-translation'` show a drop: kube-router ignores `CiliumNetworkPolicy`, and `project-pn-isolation` has no world rule. Do not mark it allowed under Flannel.

- [ ] **Step 6: Run the test to verify it passes**

Run: `python3 -m unittest tests.preflight.test_translation_egress -v`

Expected: PASS, 6 tests.

- [ ] **Step 7: Commit**

```bash
git add tests/preflight/test_translation_egress.py \
  deploy/projects/pn/boundary/translation-egress.yaml \
  deploy/projects/pn/boundary/kustomization.yaml \
  clusters/local-k3s/layers.yaml \
  clusters/vps-k3s/layers.yaml \
  contracts/CONTRACT_ISOLATION_BUDGET.md \
  ARCHITECTURE.md \
  docs/platform-architecture.html
git commit -m "$(cat <<'EOF'
fix(pn): allow backend translation hosts by DNS name

Permit only pn-backend to reach api.wiktapi.dev and
generativelanguage.googleapis.com on TCP 443. Other public
destinations stay dropped, including on kube-router.
EOF
)"
```

---

### Task 2: Reject a widened translation allow in preflight

**Files:**
- Modify: `scripts/platform-preflight.py`
- Modify: `tests/preflight/test_translation_egress.py`
- Modify: `docs/platform-architecture.html`
- Test: `tests/preflight/test_translation_egress.py`

**Interfaces:**
- Consumes: `CiliumNetworkPolicy/pn-backend-translation-egress` from Task 1, and `NetworkPolicy/project-pn-isolation`.
- Produces: `check_translation_egress(docs) -> list[dict]` in `scripts/platform-preflight.py`. Each dict has `"rule": 2`, `"object"`, and `"detail"`. `main()` calls it on the candidate when `NetworkPolicy/project-pn-isolation` in namespace `proj-pn` is present.

- [ ] **Step 1: Write the failing test**

Add these imports and tests to `tests/preflight/test_translation_egress.py`:

```python
import copy
import importlib.util

PREFLIGHT = os.path.join(BASE, "scripts", "platform-preflight.py")


def _preflight():
    spec = importlib.util.spec_from_file_location("platform_preflight", PREFLIGHT)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


class TestTranslationEgressPreflight(unittest.TestCase):
    def test_real_policy_is_accepted(self):
        module = _preflight()
        docs = [_load(POLICY), _load(PORTABLE)]
        self.assertEqual(module.check_translation_egress(docs), [])

    def test_extra_host_is_rejected(self):
        module = _preflight()
        doc = copy.deepcopy(_load(POLICY))
        doc["spec"]["egress"][1]["toFQDNs"].append({"matchName": "evil.example"})
        violations = module.check_translation_egress([doc, _load(PORTABLE)])
        self.assertTrue(any("evil.example" in item["detail"] for item in violations))
        self.assertTrue(all(item["rule"] == 2 for item in violations))

    def test_world_cidr_is_rejected(self):
        module = _preflight()
        doc = copy.deepcopy(_load(POLICY))
        doc["spec"]["egress"].append(
            {"toCIDR": ["0.0.0.0/0"], "toPorts": [{"ports": [{"port": "443", "protocol": "TCP"}]}]}
        )
        violations = module.check_translation_egress([doc, _load(PORTABLE)])
        self.assertTrue(any("0.0.0.0/0" in item["detail"] for item in violations))

    def test_missing_policy_is_rejected_when_isolation_is_present(self):
        module = _preflight()
        violations = module.check_translation_egress([_load(PORTABLE)])
        self.assertTrue(any("missing" in item["detail"] for item in violations))

    def test_frontend_selector_is_rejected(self):
        module = _preflight()
        doc = copy.deepcopy(_load(POLICY))
        doc["spec"]["endpointSelector"]["matchLabels"] = {
            "app.kubernetes.io/name": "pn-frontend"
        }
        violations = module.check_translation_egress([doc, _load(PORTABLE)])
        self.assertTrue(any("pn-backend" in item["detail"] for item in violations))

    def test_udp_443_is_rejected(self):
        module = _preflight()
        doc = copy.deepcopy(_load(POLICY))
        doc["spec"]["egress"][1]["toPorts"] = [
            {"ports": [{"port": "443", "protocol": "UDP"}]}
        ]
        violations = module.check_translation_egress([doc, _load(PORTABLE)])
        self.assertTrue(any("TCP 443" in item["detail"] for item in violations))
```

- [ ] **Step 2: Run the new tests to verify they fail**

Run: `python3 -m unittest tests.preflight.test_translation_egress.TestTranslationEgressPreflight -v`

Expected: FAIL with `check_translation_egress` missing.

- [ ] **Step 3: Implement `check_translation_egress(docs) -> list` in `scripts/platform-preflight.py`**

Add the function next to `check_project_doors`. Required hosts are the spec's two names. When `project-pn-isolation` is absent, return `[]` so existing fixtures stay valid. When it is present:

- missing `CiliumNetworkPolicy/pn-backend-translation-egress` in `proj-pn` yields detail `proj-pn translation egress policy is missing`
- selector other than `{"app.kubernetes.io/name": "pn-backend"}` yields detail `translation egress must select only pn-backend`
- a `matchName` outside the two hosts yields detail `translation egress host <name> is not allowed`
- a missing required host yields detail `translation egress is missing host <name>`
- any `0.0.0.0/0`, `world`, or `ipBlock` in either the Cilium policy or `project-pn-isolation` yields detail `translation egress must not allow world or 0.0.0.0/0`
- a `matchPattern` other than `"*"`, or no `"*"` pattern, yields detail `translation egress must allow kube-dns lookups with matchPattern *`
- an FQDN `toPorts` value other than TCP `"443"` yields detail `translation egress must be TCP 443 only`

Call it from `main()` immediately after the `check_project_doors` block, using the same violation status `STATUS_REJECTED_ISOLATION`. Pass `candidate_docs`.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `python3 -m unittest tests.preflight.test_translation_egress -v`

Expected: PASS.

Run: `python3 -m unittest discover tests/preflight`

Expected: PASS. Existing door and budget fixtures still pass because they do not contain `project-pn-isolation`.

- [ ] **Step 5: Name the preflight check in the handbook**

In the `pn-boundary` inspector bullets in `docs/platform-architecture.html`, add that `check_translation_egress` rejects any other public name. This touch is required because `scripts/platform-preflight.py` is an architecture-watched file.

- [ ] **Step 6: Commit**

```bash
git add scripts/platform-preflight.py \
  tests/preflight/test_translation_egress.py \
  docs/platform-architecture.html
git commit -m "$(cat <<'EOF'
fix(preflight): reject a widened pn translation allow-list

Fail the candidate when proj-pn isolation is present but the
backend translation policy is missing, retargeted, or opened
beyond the two named hosts.
EOF
)"
```

- [ ] **Step 7: Render both environments**

Run: `./scripts/render-validate.sh local-k3s`

Expected: exit 0. The render contains `kind: CiliumNetworkPolicy` and `name: pn-backend-translation-egress`. kubeconform may print a skip notice if it is not installed; that notice is not a failure. A schema error on this object is a failure.

Run: `./scripts/render-validate.sh vps-k3s`

Expected: exit 0, same object present.

Run: `python3 scripts/check-arch-doc-sync.py --worktree`

Expected: `[ARCH-DOC-SYNC PASS]`.
