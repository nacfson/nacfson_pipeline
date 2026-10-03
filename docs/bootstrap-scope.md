# Minimal VM bootstrap

Ansible prepares one existing Linux VM to run K3s. Application deployment stays
in the existing Kubernetes manifests and Kustomize configuration.

This document defines the scope. The automation is implemented as an Ansible playbook under `bootstrap/`.

## Required inputs

| Input | Purpose |
| --- | --- |
| VM address | SSH connection target |
| SSH user | Existing account with administrator privileges through sudo |
| K3s version | Explicit version to install; no implicit latest-version upgrade |

Use the operator's existing SSH configuration or agent for authentication. Keep
private keys outside the repository. Do not add a second credential configuration
system to the playbook.

## Prerequisites

- An existing VM with a supported Linux image, systemd, Python, and working SSH/sudo access.
- Cloud network rules and host networking already permit the required traffic.
- Installation downloads and bootstrap container images are reachable without
  backend credentials supplied to the node or a dependency on the unstarted vault.
- The default K3s pod/service network ranges do not overlap the existing network.
- Existing disk space and node capacity are sufficient for K3s; application
  capacity is checked separately before deployment.

The initial implementation should select and validate one OS release and CPU
architecture. It must not silently claim support for other combinations.

## Required actions

1. Validate prerequisites and report missing requirements before installation.
2. Install the selected K3s version and enable its service.
3. Verify the service, Kubernetes API, node Ready condition, and CoreDNS readiness.

Use K3s defaults unless a documented project requirement makes an override
necessary. Do not expose the API broadly or copy administrative kubeconfig into Git.

Rerunning bootstrap must preserve existing cluster state and data. If an existing
installation has a different version or conflicting configuration, stop and report
the difference; upgrades, reinitialization, and disk formatting are separate actions.

## Outside this bootstrap

VM creation, cloud network changes, disk provisioning, application deployment,
workload resource allocation, vault installation, backend credential provisioning,
backup/restore, monitoring, and upgrades remain outside this playbook.

Successful bootstrap means the Kubernetes host is ready. It does not establish
application readiness, durable data recovery, or compliance with the vault design.

## Conditional integration

Add host-level registry routing only when the approved private-image integration
requires it. Its endpoint and caller-authentication design must be defined first;
do not provision GHCR credentials on the node. Kubernetes creates its own
infrastructure credentials; this bootstrap does not imply a node contains no
cryptographic secrets whatsoever.
