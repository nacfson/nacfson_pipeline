# Intent: network-policy check on a local VM, also small enough for CI

GitHub issue: https://github.com/nacfson/nacfson_pipeline/issues/69

Recorded 2026-10-20 from the conversation that started with the `proj-health` 404. This file is what was described. It is not an implementation plan. Sentences marked **Agent guess** were not confirmed.

## Trigger

`proj-health` shows 404 pages. The ask was to check whether that project is configured in this pipeline before changing anything.

What the repo showed, as a fact, not as a requested fix:

- `clusters/local-k3s/projects/proj-health.yaml` exists and is listed in that cluster's `kustomization.yaml`.
- `clusters/vps-k3s/projects/` lists only `proj-pn.yaml`. There is no `proj-health.yaml` there.
- The VPS already has the health boundary, the `health.nacfson.cloud` certificate patch, gateway return hosts, and the Keycloak client redirect.
- The health workloads and `IngressRoute` are applied only by the project Kustomization. On the VPS that Kustomization is missing, so Traefik has a certificate and no matching route.

The next sentence was: before implementing that, make a verification pipeline. This issue is that pipeline. It is not the VPS admission change.

## What was asked for, in the order it was said

1. A verifying step on the local PC. Spawn a k3s cluster and verify it works exactly.
2. That may require a lot of constraints: Vault, credentials, and a network that is not the same as the VPS.
3. One point of a correct run: the network policy and the starting probe are working.
4. Not only `proj-health`. A specific CI pipeline before deploying with GitOps.
5. That wide required-check picture was rejected. Exact sentence: "No it's not my intent. We don't have to utilize entire wide screen size to use our websites." The screen-size sentence was not explained after that. Do not invent a meaning for it.
6. What is wanted instead: a specific TDD pipeline, tested with a real VM on the local PC. It is not a restricted rule on every commit and pull request. It is a specific pipeline to use when wanted.
7. Asked what "already failing before you change anything" means. After that was explained as "the check must fail once while the bug is still there," the answer was: the network policy.
8. Other things are not worth it. Expected difficulty: it is hard to configure the same cluster environment on a local VM.
9. Even if the check is not using a local Kubernetes cluster, it has to construct the same network environment as the VPS / k3s / cluster network policies.
10. Brainstorm of the shape was closed. Remaining ambiguity that was answered: the check is small enough to adopt into CI as well.
11. The OpenBao network policy was not known at the time. Other ambiguities were called done.
12. Cilium rules have to be followed when the check is done first.
13. It could be made by OpenCode skills too. It should be coupled to scripts too. The skill is only guiding. People could read it too.
14. Do not restrict using it by one script. It has to be coupled by skills too.
15. The question "which skill do you expect to be reading?" was not understood. That choice was not made. Do not treat a skill name, or "one new skill," as a decision from this conversation.

## Decisions that were actually made

- The behavior worth failing first is the network policy. Not a full copy of the cluster.
- The local machine is a real VM on the PC.
- That VM does not have to run Kubernetes. It still has to enforce the same allow and deny rules as the cluster network policies on `vps-k3s`.
- Vault, credentials, and the rest of a identical cluster are not worth reproducing. Those differences were named as the reason.
- The pipeline is something to start when wanted. It is not a rule that blocks every commit or every pull request.
- Because the check is short, also adopt that same pipeline into CI.
- Cilium rules are in the first version of the check, not a later add-on. That includes rules that were not known during the conversation, once they are Cilium rules in this repo.
- Use is not locked to one script.
- Skills are required coupling, not an optional note. A skill guides. People can read it. Scripts do the checking and stay tied to the skill. More than one script is allowed if the check needs them.

## What was rejected

- A required gate on every commit and every pull request, as the definition of this pipeline.
- Standing up the entire VPS-sized cluster on the local VM so the websites can be used.
- Treating "works exactly" as Vault parity, credential parity, or identical networking beyond the network policies.
- Restricting use of the pipeline to one script.
- Being asked to pick which existing skill page to open. That question was not answerable and is not a requirement.

## Starting probe

Named once, with the network policy, as a point that should be working. Later, the only failure called worth seeing first was the network policy, because the rest of the cluster is too hard to reproduce. The starting probe is not a decided requirement of this pipeline. It is also not an explicit cancellation. Do not build a startup-probe harness unless a later decision adds it back.

## Sequence against the health 404

Original sequence: do not implement the missing VPS `proj-health` admission until this verification pipeline exists.

Later, the pipeline was narrowed to network-policy equivalence. A network-policy check does not by itself make `health.nacfson.cloud` stop 404ing. The 404 fix was called a separate job in playback and was not corrected. Keep the 404 fix out of this issue. The original "before implementing it" sentence still means this pipeline was requested first.

## How a run is supposed to feel

TDD, as explained and then answered:

- Start the pipeline on the local VM when wanted.
- See the network-policy check fail while the local network does not yet enforce the cluster rules.
- Change the local network until it enforces those rules.
- Run the same check again. It passes.
- A check that only passes, and was never seen failing, does not count.

CI runs the same pipeline because it is small. CI adoption does not turn it back into a ban on pushing. No decision was made that CI must boot the local VM. No decision was made that CI only reads files. Those are open.

## Skills and scripts

Required shape, from the words used:

- OpenCode skills are part of how this is used, not a write-up added after the scripts exist.
- The skill guides. A person can read it without running an agent.
- Scripts are coupled to that guidance. The skill does not replace the scripts, and the scripts are not the only way to use the pipeline.
- Do not design the whole thing as one script with a skill that only points at that one command.
- Which skill file, and whether an existing skill must start it, was not decided.

**Agent guess, not confirmed:** one new skill under `.agents/skills/`, same split as `update-project-images` (a readable `SKILL.md` that tells a person or an agent which scripts to run). Do not edit every existing skill unless a later decision says so.

## Cilium rules that have to be followed first

These are repository facts, included because "follow the Cilium rules" was the decision. They were not listed by name in the conversation.

- `deploy/projects/pn/boundary/translation-egress.yaml` — `CiliumNetworkPolicy/pn-backend-translation-egress` in `proj-pn`. Egress from `pn-backend` to kube-dns on port 53, then to the names `api.wiktapi.dev` and `generativelanguage.googleapis.com` on TCP 443.
- `deploy/platform/vault/network-policy.yaml` — `CiliumNetworkPolicy/openbao-apiserver-egress` in `vault`. Egress from OpenBao to the Kubernetes API entity on TCP 443 and TCP 6443. This is the policy that was not known during the conversation.

A checker that only understands `networking.k8s.io/v1` `NetworkPolicy` does not follow those two rules. `toFQDNs`, DNS match patterns, and `toEntities: kube-apiserver` are Cilium fields.

## Other network policies in this repo

"Same network environment as the VPS / k3s / cluster network policies" was the scope sentence. These are the policy objects under `deploy/`. Generated Flux policies under `clusters/*/flux-system/gotk-components.yaml` were not discussed. **Agent guess:** leave the generated Flux files out unless a later decision adds them.

| File | Kind | Name |
| :--- | :--- | :--- |
| `deploy/projects/health/boundary/network-policy.yaml` | NetworkPolicy | `project-health-isolation` |
| `deploy/projects/pn/boundary/network-policy.yaml` | NetworkPolicy | `project-pn-isolation` |
| `deploy/projects/pn/boundary/translation-egress.yaml` | CiliumNetworkPolicy | `pn-backend-translation-egress` |
| `deploy/platform/governance/base-network-policy.yaml` | NetworkPolicy | `default-deny-all` |
| `deploy/platform/vault/network-policy.yaml` | NetworkPolicy | `vault-default-deny` |
| `deploy/platform/vault/network-policy.yaml` | NetworkPolicy | `openbao-ingress-policy` |
| `deploy/platform/vault/network-policy.yaml` | CiliumNetworkPolicy | `openbao-apiserver-egress` |
| `deploy/platform/vault/network-policy.yaml` | NetworkPolicy | `openbao-backup-policy` |
| `deploy/platform/vault/network-policy.yaml` | NetworkPolicy | `db-proxy-policy` |
| `deploy/platform/vault/network-policy.yaml` | NetworkPolicy | `registry-proxy-policy` |

Health isolation, as a fact from the file, is default-deny. It names Traefik and the `platform` namespace to ports 80 and 8080, frontend to backend on 8080, DNS, `vault-db-proxy` on 5432, and the auth gateway on 8080. Direct Postgres, Keycloak, and the public internet are not named. That list is an illustration of one policy. The decision was cluster network policies, not health only.

## Out of scope

- Copying Vault, unsealing OpenBao, or replaying credentials on the local VM.
- Proving `health.nacfson.cloud` stops returning 404. That is the missing VPS project Kustomization, not this check.
- A full local k3s cluster that matches the VPS.
- A rule that rejects every commit or every pull request.
- Choosing a website layout, including whatever "entire wide screen size" meant. That sentence was not explained.

## Open, because it was not decided

- Whether CI boots a VM or only runs the coupled scripts.
- The skill file name, and whether any skill that already exists must start this check.
- Whether the starting probe comes back into this pipeline.
- What "entire wide screen size" meant.
- Whether generated Flux `NetworkPolicy` objects in `gotk-components.yaml` are part of "cluster network policies."

## Non-goals an implementer might invent

- One script as the only interface.
- A skill that contains the only copy of the rules, with no scripts.
- A second production cluster.
- Solving the health 404 inside this issue.
