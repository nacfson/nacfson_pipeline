# Contract: Manual-Change Policy

**Feature**: [spec.md](../spec.md) Story 7, FR-015, FR-016, SC-011 | **Decisions**: [research.md](../research.md) R14, R16

This is the binding shape of `deploy/platform/governance/manual-change-policy.yaml` and the two identity files next to it. The `governance` layer applies all three. CI enforces the invariants in §5.

## 1. Objects

| File | Objects |
|---|---|
| `manual-change-policy.yaml` | `ValidatingAdmissionPolicy` `gitops-managed-namespaces` + its `ValidatingAdmissionPolicyBinding`; `ValidatingAdmissionPolicy` `gitops-managed-cluster-kinds` + its binding |
| `break-glass-rbac.yaml` | `ClusterRoleBinding` `platform-break-glass`: group `platform:break-glass` → ClusterRole `cluster-admin` |
| `operator-access.yaml` | `ServiceAccount` `governance/operator-readonly` (`automountServiceAccountToken: false`); `ClusterRoleBinding` `operator-readonly-view` → ClusterRole `view` |

## 2. Match scope

| Policy | `resourceRules` | Operations | Binding selector |
|---|---|---|---|
| `gitops-managed-namespaces` | all groups, versions, resources; `scope: Namespaced` | CREATE, UPDATE, DELETE | `namespaceSelector`: `kubernetes.io/metadata.name` In [`default`, `flux-system`, `governance`, `identity`, `platform`, `vault`, `proj-pn`] |
| `gitops-managed-cluster-kinds` | `namespaces`, `customresourcedefinitions`, `clusterroles`, `clusterrolebindings`, `validatingadmissionpolicies`, `validatingadmissionpolicybindings`, `validatingwebhookconfigurations`, `mutatingwebhookconfigurations` | CREATE, UPDATE, DELETE | none (all objects of these kinds) |

GET, LIST, WATCH and CONNECT (`exec`, `attach`, `port-forward`, `proxy`) are never matched.

## 3. Allow rule (identical in both policies)

A request is **allowed** if any of these is true; otherwise it is denied:

| # | Condition | Why |
|---|---|---|
| 1 | `username` starts with `system:serviceaccount:flux-system:` | Flux controllers (apply, status, leases, events) |
| 2 | `username == system:serviceaccount:proj-pn:pn-reconciler` | Project layer (RBAC limits it further) |
| 3 | `username` starts with `system:serviceaccount:kube-system:` or `system:node:`, or is one of `system:kube-controller-manager`, `system:kube-scheduler`, `system:apiserver` | Kubernetes control plane: pods from ReplicaSets/Jobs, PVCs from StatefulSets, status, garbage collection |
| 4 | `username` is a K3s internal user observed on the test cluster (recorded here after [quickstart F0](../quickstart.md#f-manual-change-rejection-sc-011)) | K3s supervisor and controllers |
| 5 | `groups` contains `platform:break-glass` | FR-016 |
| 6 | `resource == pods`, no subresource, `operation == DELETE` | Restart by deleting a pod (Story 7.5) |
| 7 | `resource == pods`, `subResource == eviction` | Node drain |
| 8 | `resource == serviceaccounts`, `subResource == token` | Short-lived token for the read-only kubeconfig (R14). Creates no stored object. |

| Setting | Value |
|---|---|
| `failurePolicy` | `Fail` |
| `validationActions` | `[Deny]` |
| `messageExpression` | `"Manual change rejected: merge to main in github.com/nacfson/nacfson_pipeline, or use the break-glass procedure (operations §12)."` |

## 4. Identities

| Identity | How it is used | Can write? |
|---|---|---|
| `governance/operator-readonly` | Workstation kubeconfig with a ≤ 24 h token (`scripts/operator-kubeconfig.sh`) | No (RBAC `view`; no Secrets) |
| Group `platform:break-glass` | Only through impersonation by the node's K3s admin credential: `sudo k3s kubectl --as=break-glass:<operator> --as-group=platform:break-glass …` | Yes (`cluster-admin`, allowed by rule 5) |
| K3s admin (`system:admin`, group `system:masters`) without impersonation | Node only | No: denied in the matched scope |

## 5. Invariants checked by CI (render-validate)

1. Both bindings exist with `validationActions: [Deny]`, and both policies have `failurePolicy: Fail`.
2. The `namespaceSelector` list equals `{default, flux-system}` ∪ every `Namespace` declared under `deploy/`. A new project namespace fails CI until it is added.
3. The list contains none of `kube-system`, `kube-public`, `kube-node-lease`.
4. `platform-break-glass` binds exactly one subject: `Group platform:break-glass`.
5. `operator-readonly-view` binds only ClusterRole `view`.

## 6. Recovery

| Situation | Action |
|---|---|
| A system controller is denied | Break-glass: edit the allow rule in Git, and if needed patch the policy on the node under the break-glass identity until the PR merges |
| Policy blocks the break-glass path itself | Root on the node: restart K3s with `--kube-apiserver-arg=disable-admission-plugins=ValidatingAdmissionPolicy`, delete the bindings, fix Git, then restart K3s normally. Flux re-applies the fixed policy. |
