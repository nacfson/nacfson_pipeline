# Contract: Enforcement Gates

**Covers**: FR-014, FR-020 to FR-024. **Decisions**: [research.md R7–R10](../research.md).

## Gate 1: Preflight `check_log_contract` (this repository, every PR)

Implemented in `scripts/platform-preflight.py`. It runs inside `render-validate.sh <cluster>` and reports findings with the existing `{rule, object, detail}` shape.

| Rule ID | Fails when | Applies to |
|---|---|---|
| `log-declaration-missing` | a container in a `proj-*` workload has no `logs.nacfson.cloud/<container>` annotation, or its value isn't `v3/json`, `v3/nginx`, or `v2/legacy` | both clusters |
| `log-file-output` | a container env var name matches `(?i)^LOG_(FILE\|PATH\|DIR)$`, or an arg matches `(?i)--?log[-_]?(file\|path\|dir)`, or `LOG_FORMAT` / `--log-format` isn't `json` | both |
| `log-volume-undeclared` | a `volumeMount.mountPath` contains `/log` or `/logs` as a path segment and isn't listed in `logs.nacfson.cloud/<container>.data-paths` | both |
| `log-data-reason-missing` | `data-paths` is set without `data-reason` | both |
| `log-migration-row-missing` | a `deploy/projects/<slug>` container has no row in contract §6 | both |
| `log-migration-regressed` | a `done` row's container is declared `v2/legacy` | both |
| `log-legacy-list-drift` | `legacy-containers.txt` in the Fluent Bit ConfigMap doesn't equal the set of containers declared `v2/legacy` | both |
| `log-version-label` | the pod-template label `app.kubernetes.io/version` is missing, doesn't match `^v\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?$`, or is a plain `v0.0.0` (no commit) | both |

*Updated 2026-11-05 (operator decision D1):* the former `log-version-untagged` rule is removed. An untagged image (`v0.0.0-<commit>`) may run in both clusters.

A `pending` project's existing containers may be `v2/legacy`. Preflight reports them as a warning. A container added to a project after this feature lands must be `v3/*`.

## Gate 1b: Version-label check (this repository, every PR that changes a project workload)

*Added 2026-11-05 (operator decision D19: verify on the PR, never in the cluster).* FR-024a.

Preflight is offline and can't read images, so this is a separate CI step with registry access. It runs on the rendered `proj-*` workloads of both clusters.

| Rule ID | Fails when |
|---|---|
| `log-version-label-mismatch` | a container's pod-template label `app.kubernetes.io/version` differs from the `org.opencontainers.image.version` label of the image digest it pins (read with `skopeo inspect`) |
| `log-version-image-unreadable` | the pinned image can't be read from the registry, so the label can't be verified |

Example output:

```
✗ log-version-label-mismatch  proj-pn/backend
  label  app.kubernetes.io/version = v1.4.2
  image  ghcr.io/nacfson/pn-backend@sha256:ab12… → org.opencontainers.image.version = v1.5.0
```

A pod runs several containers but has one label. If one pod template has containers with different image versions, the check fails with `log-version-label-mismatch` on the containers that differ. Each hosted project today runs one image per pod template, so this doesn't happen.

## Gate 2: `logcheck` (project repositories, every image build)

The CLI comes from `services/error-pipeline/cmd/logcheck` (reconciliation R-1) and is published as `ghcr.io/nacfson/logcheck`. Project CI pins it by digest.

```
logcheck --format json|nginx [--expect-version vX.Y.Z] [--expect-service NAME] < captured-output.log
```

| Input | stdin: the captured stdout+stderr of the built image running the project's log smoke scenario |
|---|---|
| Output | stdout: one JSON violation per line (`{"line":N,"section":"§2","kind":"missing-key:service.version","text":"…redacted…"}`), then a summary line |
| Exit code | `0` = no violations; `1` = violations found; `2` = usage or IO error |

The project CI job must be named **`log-contract`** and run on **every** commit that builds an image, tagged or not. `--expect-version` is the plain tag for a tag build and `v0.0.0-<short commit>` for any other build.

Besides the line rules, `logcheck` reports `stacktrace-too-long` when `exception.stacktrace` exceeds 8 KiB (FR-004).

## Gate 3: Image-pin helper (`update-project-images`)

These checks are added to `.agents/skills/update-project-images/scripts/update_project_images.py`. They are blocking unless stated otherwise.

| Check | Fails when |
|---|---|
| Version label | the image config has no `org.opencontainers.image.version`, it doesn't match the version regex, or it is a plain `v0.0.0` |
| Untagged commit | the version is `v0.0.0-<commit>` and `<commit>` isn't a prefix of the image's `org.opencontainers.image.revision` |
| Conformance | for **every** image, the `log-contract` check run on the image's `org.opencontainers.image.revision` commit is missing or not `success` (`gh api repos/<o>/<r>/commits/<sha>/check-runs`) |
| Label write | (action, not a check) the helper sets the pod-template `app.kubernetes.io/version` to the image version in the same edit as the digest |

The PR body adds the version and the `log-contract` run URL to the existing evidence table.
