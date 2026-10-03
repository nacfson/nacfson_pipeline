# Data Model: Minimal VM Bootstrap

## Bootstrap request

| Field | Validation |
| --- | --- |
| Target address | Exactly one inventory host; nonempty address or existing SSH alias |
| SSH user | Explicit nonempty existing remote user |
| K3s version | Exact release tag; initial accepted value `v1.35.9+k3s1` |

These are the only feature-specific inputs. Private keys/passwords/backend credentials are not request fields. SSH authentication remains external to the feature.

## Observed target

Transient read-only facts: OS/version, architecture, privilege, service manager, Python, CPU/RAM, usable storage, swap/cgroups/kernel support, interfaces/routes, conflicting listeners, and existing cluster installation/configuration evidence. Facts must not include secret contents. The observations determine admission to the supported profile; they do not determine project resource allocations.

## Installation receipt

Root-owned JSON, mode `0600`, at `/var/lib/nacfson-bootstrap/installation.json`:

| Field | Meaning |
| --- | --- |
| `profile_revision` | Internal bootstrap compatibility revision, initially `1` |
| `k3s_version` | Exact installed release |
| `binary_sha256` | Verified digest of the installed public binary |
| `unit_sha256` | Digest of the installed non-secret systemd unit |

Write atomically after complete binary/unit installation. No timestamps, node identity keys, tokens, passwords, kubeconfig, or datastore hashes are needed. A receipt proves only ownership metadata, not runtime readiness or immunity from host compromise. Compare it with actual files/effective settings before using it.

## State transitions

| Observed state | Allowed action | Result |
| --- | --- | --- |
| Fresh and prerequisites satisfied | Install files, validate unit, write receipt, enable/start | Verify readiness |
| Fresh but invalid prerequisites | Report failure | No installation changes |
| Managed-compatible, running | Read-only readiness checks | Ready or failed |
| Managed-compatible, stopped/disabled | Enable/start without reinstall | Verify readiness |
| Files/data present without valid receipt | Report partial/unknown state | No automatic repair/reset |
| Receipt exists but version/files/effective configuration conflict | Report conflict | No upgrade or overwrite |
| Compatible install but readiness timeout | Report failed stage | Preserve installation and data |

The receipt is not rewritten on a healthy rerun. Interrupted downloads may leave staging files; staging alone is not cluster data and may be cleaned/retried after fresh-host classification. Partial executable/unit installation without a receipt requires explicit operator recovery. No automatic rollback or generic resume journal is included.

## Bootstrap result

Sanitized fields: `target`, `requested_version`, `observed_version` when known, `completed_stages`, four readiness outcomes, `status`, `failed_stage` when applicable, and `next_action`. Status is `ready`, `failed`, or `validation_only`. Output uses normal Ansible task/summary reporting; no separate result database or machine API is required.

Authentication/unreachable failures may use Ansible's native failure report when a target summary cannot be emitted. They must still identify the target and access stage and must return nonzero. A `ready` result authorizes only proceeding to separate deployment checks; it does not certify applications or Vault integration.
