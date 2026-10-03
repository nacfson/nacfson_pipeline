# Research: Minimal VM Bootstrap

**Date**: 2026-10-03
**Scope**: Resolve the implementation choices for [spec.md](spec.md), without adding application deployment or Vault integration.

## 1. Host and controller profile

**Decision**: Support Ubuntu Server 24.04 LTS ARM64 on one existing VM. Use Python 3.12 on the controller and target, with `ansible-core==2.21.4` on the controller. Require systemd, sudo, Python, working host DNS, a default route, CA trust, and the normal Ubuntu kernel/network tools. Install no additional agent or container runtime separately.

**Rationale**: This matches the intended A1 host and keeps the support matrix small. The published Ansible matrix covers this Python version, and the selected core release is available on PyPI. ARM64 is supported by K3s.

**Alternatives considered**: Supporting every Linux image would expand host-specific work. A large third-party Ansible collection or role adds defaults and lifecycle behavior outside this feature. A bare script is viable but offers less structured validation and reporting.

Sources: [Ansible support matrix](https://docs.ansible.com/projects/ansible/latest/reference_appendices/release_and_maintenance.html), [Ansible core release](https://pypi.org/project/ansible-core/2.21.4/), [K3s requirements](https://docs.k3s.io/installation/requirements).

## 2. Explicit version and installation

**Decision**: Require an exact `k3s_version` input; initially accept `v1.35.9+k3s1`, published September 30, 2026. Adding another accepted release is a reviewed support-profile change, not an automatic upgrade. Download the ARM64 release binary and its published SHA-256 manifest over verified HTTPS, verify the binary, and install it atomically at `/usr/local/bin/k3s`. Use one repository-owned systemd service template to run `k3s server` with default server behavior. No runtime download or execution of an unpinned installer script.

**Rationale**: The binary already includes the server, container runtime, and command-line clients. Owning a small service definition makes the effective configuration and rerun comparison explicit; it avoids installer behavior that can overwrite a previous service configuration. This is a project design choice, not a claim that upstream requires this method.

**Alternatives considered**: The official installation script is convenient, but safely pinning, inspecting, and comparing its generated files adds another artifact to this restricted profile. A full installation role or OS image pipeline is unnecessary. Air-gap image bundles add distribution work; this initial profile uses public bootstrap images.

Sources: [K3s release notes](https://docs.k3s.io/release-notes/v1.35.X), [K3s server command](https://docs.k3s.io/cli/server), [installation configuration](https://docs.k3s.io/installation/configuration), [release binary placement](https://docs.k3s.io/installation/airgap).

## 3. Prerequisites, not extra configuration knobs

**Decision**: Require at least two visible CPUs, 1,900 MiB reported total RAM (to accommodate OS reporting on a nominal 2 GB VM), 10 GiB free on the existing filesystem backing `/var/lib/rancher`, disabled swap, available cgroups, and usable `overlay`/`br_netfilter` kernel support. The 10 GiB floor is our bootstrap policy, not an upstream workload sizing guarantee. Check non-default host interface/route networks against `10.42.0.0/16` and `10.43.0.0/16` before a fresh install; ignore the default route and recognized cluster-created routes on an existing managed host.

**Rationale**: Detect obvious inability to run the default profile before persistent installation changes. These checks do not establish application capacity or prove cloud firewall correctness.

**Alternatives considered**: Automatically resizing, installing missing host packages, changing firewalls, or choosing different CIDRs would expand the feature. Report the missing prerequisite instead. Existing host/cloud rules must permit cluster networking and restrict administrative access; bootstrap does not modify those policies. K3s's own runtime networking rules remain normal cluster behavior.

Source: [K3s requirements](https://docs.k3s.io/installation/requirements). Capacity thresholds beyond upstream CPU/RAM guidance are explicit local policy.

## 4. Existing installations and interrupted runs

**Decision**: Classify targets as fresh, managed-compatible, or conflicting/unknown before installation. Write one root-owned, non-secret installation receipt only after the binary and service are installed and validated, before enabling/starting the service. It contains a profile revision, selected version, and hashes of those two managed files. A matching receipt is necessary but not sufficient: verify hashes, expected unit path/arguments, and absence of unexpected configuration/drop-ins before any service mutation. Do not read or hash cluster tokens, private keys, or datastore contents into the receipt.

**Rationale**: A healthy rerun becomes read-only verification; a compatible completed installation with a stopped service can be started without reinstalling. A partial install without a valid receipt stops for operator recovery. Existing binary or unit presence alone is not proof of compatibility.

**Alternatives considered**: Automatic adoption, rollback, uninstall/reinstall, a multistage recovery journal, or broad repair would add complexity and risk data loss. The spec explicitly allows stopping on unverified partial state.

Source: [K3s configuration sources and precedence](https://docs.k3s.io/installation/configuration). The receipt/state classification is a project decision.

## 5. Three inputs, existing authentication

**Decision**: Use ordinary `ansible-playbook` invocation with a one-host inline inventory, `-u`, and `-e k3s_version=...`. Use the existing SSH agent/configuration and verified known-host entry; require noninteractive sudo for the initial profile. Keep host-key verification enabled, disable password prompting, and use no cloud SDK or account credentials. Do not fetch administrative kubeconfig to the controller.

**Rationale**: This delivers exactly the three agreed feature settings without inventory generation, key-copying, or a second credentials mechanism. Administrator checks can execute through the existing SSH session on the VM.

**Alternatives considered**: A custom CLI wrapper, interactive installer, generated inventory, or an application/Vault token input is unnecessary.

Source: [Ansible SSH connection](https://docs.ansible.com/projects/ansible/latest/collections/ansible/builtin/ssh_connection.html).

## 6. Bounded execution and truthful reporting

**Decision**: Set SSH connection timeout to 15 seconds; ordinary remote task limits to 60 seconds; download request idle timeout to 30 seconds and outer download limit to 600 seconds; service activation request limit to 60 seconds; each readiness stage deadline to 300 seconds with 5-second individual API request limits. Use task deadlines and bounded remote commands where needed, not SSH connection timeout alone. Starting the service is nonblocking; readiness is checked separately. No background installation is declared successful merely because it started.

**Rationale**: These are implementation constants, not required operator settings. Report the last completed stage and non-sensitive failure category. A failed bootstrap may leave K3s running or files installed; no automatic rollback is claimed.

**Alternatives considered**: Unbounded polling, `ignore_errors`, or treating connection failure as simulated success violates the spec. Do not dump raw service environments, kubeconfig, or arbitrary journal content in failure summaries.

Sources: [Ansible task timeout keyword](https://docs.ansible.com/projects/ansible/latest/reference_appendices/playbooks_keywords.html), [download timeouts/checksums](https://docs.ansible.com/projects/ansible/latest/collections/ansible/builtin/get_url_module.html).

## 7. Validation and scope boundaries

**Decision**: Use syntax checks plus real disposable VM acceptance exercises for fresh install, healthy rerun, version/configuration conflict, failed dependencies, partial installation, and failed readiness. Check mode is validation-only and must never print a readiness success for an uninstalled host. Keep application manifests and existing release scripts unchanged.

**Rationale**: A mock service or a rendered playbook cannot prove systemd, kernel, networking, and node readiness. The feature adds no workloads or secret migration. The unresolved Vault caller identity and placement do not block installing the base host.

**Alternatives considered**: A heavyweight Molecule/container simulation stack is unnecessary for this single profile; it would not replace VM validation anyway. Changes to the existing application release workflow would expand the agreed scope.

Source: [Ansible check mode limitations](https://docs.ansible.com/projects/ansible/latest/playbook_guide/playbooks_checkmode.html).

## Research completion

All planning choices above are resolved. Artifact availability and compatibility still require implementation-time verification and live acceptance; no VM was created or changed during planning.
