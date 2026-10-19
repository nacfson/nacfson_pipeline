#!/usr/bin/env python3
"""Resolve project image links, validate the images, and update the image pins.

This helper is used by the ``update-project-images`` skill.  It edits only the
main containers of Deployments under ``deploy/projects/<slug>/workloads`` and
the tenant image-pin inventory in ``docs/platform-architecture.html``.

Image validation is registry-only by default: manifests and the small config
blobs are inspected, but layers are never downloaded.  ``--pull-check`` is an
optional, non-blocking local pull.
"""

from __future__ import annotations

import argparse
import html
import json
import re
import subprocess
import sys
from dataclasses import dataclass, field
from pathlib import Path
from urllib.parse import parse_qs, urlparse

try:
    import yaml
except ImportError:  # pragma: no cover - PyYAML is a repository dependency
    yaml = None

DIGEST_RE = re.compile(r"sha256:[0-9a-f]{64}")
PROJECT_RE = re.compile(r"^[a-z0-9][a-z0-9-]*$")
ROLE_RE = re.compile(r"^[a-z0-9][a-z0-9-]*$")
SECRET_ENV_RE = re.compile(
    r"(?:PASSWORD|PASSWD|SECRET|TOKEN|PRIVATE_KEY|API_KEY|CREDENTIAL)",
    re.IGNORECASE,
)
PINS_START = "<!-- PROJECT_IMAGE_PINS:START -->"
PINS_END = "<!-- PROJECT_IMAGE_PINS:END -->"


class UpdateError(RuntimeError):
    """A user-actionable failure; the CLI exits with status 2."""


@dataclass
class LinkRef:
    kind: str
    original: str
    image: str | None = None
    digest: str | None = None
    owner: str | None = None
    repo: str | None = None
    run_id: str | None = None
    sha: str | None = None


@dataclass
class Target:
    project: str
    role: str
    deployment: str
    container: str
    path: Path
    old_image: str
    old_repo: str
    old_digest: str
    container_port: int | None


@dataclass
class ResolvedLink:
    repo: str
    digest: str
    tag: str | None
    expected_revision: str | None
    source: str
    warnings: list[str] = field(default_factory=list)
    source_repo: str | None = None


@dataclass
class ValidationReport:
    platforms: list[str] = field(default_factory=list)
    checks: dict[str, bool] = field(default_factory=dict)
    warnings: list[str] = field(default_factory=list)
    skipped: list[str] = field(default_factory=list)
    configs: dict[str, dict] = field(default_factory=dict)
    evidence: dict[str, dict] = field(default_factory=dict)


@dataclass
class ImageUpdate:
    target: Target
    source: str
    tag: str | None
    digest: str
    new_image: str
    validation: ValidationReport | None = None
    resolution_warnings: list[str] = field(default_factory=list)


def run_command(cmd: list[str], check: bool = False) -> subprocess.CompletedProcess:
    result = subprocess.run(cmd, capture_output=True, text=True)
    if check and result.returncode != 0:
        raise UpdateError(
            f"command failed ({result.returncode}): {' '.join(cmd)}\n"
            f"{result.stderr.strip()}"
        )
    return result


def extract_digest(text: str) -> str:
    match = DIGEST_RE.search(text or "")
    if not match:
        raise UpdateError(f"no sha256 digest found in output: {text.strip()[:200]}")
    return match.group(0)


def normalize_project(project: str) -> str:
    slug = project.strip().lower()
    if slug.startswith("proj-"):
        slug = slug[len("proj-") :]
    if not PROJECT_RE.match(slug):
        raise UpdateError(f"invalid project name: {project!r}")
    return slug


def parse_link(link: str) -> LinkRef:
    original = link.strip()
    if not original:
        raise UpdateError("empty image link")

    if original.startswith("docker://"):
        original = original[len("docker://") :]

    if DIGEST_RE.fullmatch(original):
        return LinkRef(kind="digest", original=link, digest=original)

    if original.startswith("ghcr.io/"):
        return LinkRef(kind="image", original=link, image=original)

    parsed = urlparse(original)
    if parsed.scheme in ("http", "https") and parsed.netloc.lower() == "ghcr.io":
        return LinkRef(kind="image", original=link, image=original.split("://", 1)[1])

    if parsed.scheme in ("http", "https") and parsed.netloc.lower() == "github.com":
        parts = [p for p in parsed.path.split("/") if p]
        query = parse_qs(parsed.query)

        if len(parts) >= 5 and parts[2] == "actions" and parts[3] == "runs":
            return LinkRef(
                kind="actions_run",
                original=link,
                owner=parts[0].lower(),
                repo=parts[1],
                run_id=parts[4],
            )

        if len(parts) >= 4 and parts[2] == "commit":
            return LinkRef(
                kind="commit",
                original=link,
                owner=parts[0].lower(),
                repo=parts[1],
                sha=parts[3],
            )

        package = None
        owner = None
        if len(parts) >= 5 and parts[0] in ("users", "orgs"):
            if parts[2] == "packages" and parts[3] == "container":
                owner = parts[1].lower()
                package = parts[4].lower()
        elif len(parts) >= 5 and parts[2] == "pkgs" and parts[3] == "container":
            owner = parts[0].lower()
            package = parts[4].lower()

        if package:
            tags = query.get("tag", [])
            if not tags:
                raise UpdateError(
                    "package link has no ?tag=; open the package version page "
                    "and copy the tag or digest link instead"
                )
            return LinkRef(
                kind="image",
                original=link,
                image=f"ghcr.io/{owner}/{package}:{tags[0]}",
            )

    raise UpdateError(
        "unsupported link. Use a GHCR image reference, a GHCR package version "
        "link with ?tag=, a GitHub Actions run link, a GitHub commit link, or "
        "a bare sha256 digest."
    )


def _image_parts(image: str) -> tuple[str, str]:
    """Return (repo, digest). A tag-pinned image returns an empty digest."""
    if "@" in image:
        repo, digest = image.rsplit("@", 1)
        if not DIGEST_RE.fullmatch(digest):
            raise UpdateError(f"invalid image digest: {image}")
        return repo, digest
    if ":" in image.rsplit("/", 1)[-1]:
        return image.rsplit(":", 1)[0], ""
    return image, ""


def _iter_deployments(workload_dir: Path):
    """Yield (path, deployment, container, image, containerPort) for main containers."""
    if yaml is None:
        raise UpdateError("PyYAML is required to read project manifests")
    paths = sorted(
        list(workload_dir.glob("*.yaml")) + list(workload_dir.glob("*.yml"))
    )
    for path in paths:
        try:
            docs = [d for d in yaml.safe_load_all(path.read_text(encoding="utf-8")) if d]
        except yaml.YAMLError as exc:
            raise UpdateError(f"cannot parse {path}: {exc}") from exc
        for doc in docs:
            if not isinstance(doc, dict) or doc.get("kind") != "Deployment":
                continue
            metadata = doc.get("metadata") or {}
            deployment = str(metadata.get("name") or "")
            template = ((doc.get("spec") or {}).get("template") or {}).get("spec") or {}
            for container in template.get("containers") or []:
                image = str(container.get("image") or "")
                if not image:
                    continue
                port = None
                for item in container.get("ports") or []:
                    if isinstance(item, dict) and item.get("containerPort") is not None:
                        try:
                            port = int(item["containerPort"])
                        except (TypeError, ValueError) as exc:
                            raise UpdateError(
                                f"invalid containerPort in {path}: "
                                f"{item['containerPort']!r}"
                            ) from exc
                        break
                yield path, deployment, str(container.get("name") or ""), image, port


def discover_target(root: Path, project: str, role: str) -> Target:
    slug = normalize_project(project)
    role = role.strip().lower()
    if not ROLE_RE.match(role):
        raise UpdateError(f"invalid role: {role!r}")

    project_dir = root / "deploy" / "projects" / slug
    workload_dir = project_dir / "workloads"
    if not workload_dir.is_dir():
        available = sorted(
            p.parent.name for p in (root / "deploy" / "projects").glob("*/workloads")
        )
        raise UpdateError(
            f"project {slug!r} has no workloads directory; available projects: "
            f"{', '.join(available) or 'none'}"
        )

    matches: list[tuple[int, Target]] = []
    seen_roles: set[str] = set()
    for path, deployment, name, image, port in _iter_deployments(workload_dir):
        seen_roles.add(name)
        repo, digest = _image_parts(image)
        repo_name = repo.rsplit("/", 1)[-1].lower()
        score = 0
        if name.lower() == role:
            score = 100
        elif deployment.lower() == role:
            score = 90
        elif deployment.lower().endswith("-" + role):
            score = 80
        elif repo_name.endswith("-" + role):
            score = 70
        elif role in deployment.lower():
            score = 60
        if score:
            matches.append(
                (
                    score,
                    Target(
                        project=slug,
                        role=role,
                        deployment=deployment,
                        container=name,
                        path=path,
                        old_image=image,
                        old_repo=repo,
                        old_digest=digest,
                        container_port=port,
                    ),
                )
            )

    if not matches:
        raise UpdateError(
            f"no container matched role {role!r} in project {slug!r}; "
            f"available containers: {', '.join(sorted(seen_roles)) or 'none'}"
        )

    best = max(score for score, _ in matches)
    winners = [target for score, target in matches if score == best]
    if len(winners) > 1:
        names = ", ".join(f"{t.deployment}/{t.container}" for t in winners)
        raise UpdateError(
            f"role {role!r} is ambiguous in project {slug!r}: {names}. "
            "Use the container name as the role."
        )
    return winners[0]


def resolve_tag_digest(repo: str, tag: str) -> str | None:
    result = run_command(
        ["skopeo", "inspect", "--format", "{{.Digest}}", f"docker://{repo}:{tag}"]
    )
    if result.returncode != 0:
        return None
    try:
        return extract_digest(result.stdout)
    except UpdateError:
        return None


def resolve_digest_ref(ref: str) -> str:
    result = run_command(
        ["skopeo", "inspect", "--format", "{{.Digest}}", f"docker://{ref}"]
    )
    if result.returncode != 0:
        raise UpdateError(
            f"cannot resolve image {ref}: {result.stderr.strip() or 'registry error'}"
        )
    found = extract_digest(result.stdout)
    if "@" in ref:
        requested = ref.rsplit("@", 1)[1]
        if found != requested:
            raise UpdateError(
                f"digest mismatch for {ref}: registry returned {found}"
            )
    return found


def _tag_candidates(sha: str) -> list[str]:
    short = sha[:7]
    return [short, f"sha-{short}", sha, f"sha-{sha}"]


def _run_view(owner: str, repo: str, run_id: str) -> dict:
    result = run_command(
        [
            "gh",
            "run",
            "view",
            run_id,
            "--repo",
            f"{owner}/{repo}",
            "--json",
            "headSha,headBranch,conclusion,workflowName,jobs",
        ]
    )
    if result.returncode != 0:
        raise UpdateError(
            f"cannot read GitHub Actions run {run_id}: "
            f"{result.stderr.strip() or 'gh error'}"
        )
    try:
        return json.loads(result.stdout)
    except json.JSONDecodeError as exc:
        raise UpdateError(f"cannot parse Actions run metadata: {exc}") from exc


def resolve_link(
    target: Target, link: LinkRef, allow_unmatched_job: bool = False
) -> ResolvedLink:
    warnings: list[str] = []

    if link.kind == "digest":
        digest = resolve_digest_ref(f"{target.old_repo}@{link.digest}")
        return ResolvedLink(
            repo=target.old_repo,
            digest=digest,
            tag=None,
            expected_revision=None,
            source=link.original,
            warnings=warnings,
        )

    if link.kind == "actions_run":
        data = _run_view(link.owner or "", link.repo or "", link.run_id or "")
        if data.get("conclusion") != "success":
            raise UpdateError(
                f"Actions run {link.run_id} did not succeed "
                f"(conclusion: {data.get('conclusion')})"
            )
        if data.get("headBranch") != "main":
            raise UpdateError(
                f"Actions run {link.run_id} was built from "
                f"{data.get('headBranch')!r}, not main"
            )
        sha = str(data.get("headSha") or "")
        if not re.fullmatch(r"[0-9a-f]{40}", sha):
            raise UpdateError(f"Actions run {link.run_id} has no usable headSha")
        jobs = data.get("jobs") or []
        role_jobs = [
            job
            for job in jobs
            if target.role in str(job.get("name", "")).lower()
        ]
        if role_jobs and not any(job.get("conclusion") == "success" for job in role_jobs):
            raise UpdateError(
                f"Actions run {link.run_id} has no successful {target.role} job"
            )
        if not role_jobs:
            message = (
                f"no Actions job name mentioned {target.role!r}; "
                "the digest was resolved from the commit tag"
            )
            if allow_unmatched_job:
                warnings.append(message)
            else:
                raise UpdateError(
                    message
                    + ". Confirm the run published this image, then pass "
                    "--allow-unmatched-job."
                )
        for tag in _tag_candidates(sha):
            digest = resolve_tag_digest(target.old_repo, tag)
            if digest:
                return ResolvedLink(
                    repo=target.old_repo,
                    digest=digest,
                    tag=tag,
                    expected_revision=sha,
                    source=link.original,
                    warnings=warnings,
                    source_repo=f"{link.owner}/{link.repo}",
                )
        raise UpdateError(
            f"no image tag matched the commit {sha[:7]} for {target.old_repo}; "
            "paste the GHCR digest link instead"
        )

    if link.kind == "commit":
        sha = link.sha or ""
        if not re.fullmatch(r"[0-9a-f]{40}", sha):
            raise UpdateError(f"commit link has no full 40-character sha: {sha!r}")
        for tag in _tag_candidates(sha):
            digest = resolve_tag_digest(target.old_repo, tag)
            if digest:
                return ResolvedLink(
                    repo=target.old_repo,
                    digest=digest,
                    tag=tag,
                    expected_revision=sha,
                    source=link.original,
                    warnings=warnings,
                    source_repo=f"{link.owner}/{link.repo}",
                )
        raise UpdateError(
            f"no image tag matched commit {sha[:7]} for {target.old_repo}; "
            "paste the GHCR digest link instead"
        )

    if link.kind == "image":
        ref = link.image or ""
        if "@" in ref:
            repo, digest = ref.rsplit("@", 1)
            tag = None
        elif ":" in ref.rsplit("/", 1)[-1]:
            repo, tag = ref.rsplit(":", 1)
            digest = ""
        else:
            repo, tag, digest = ref, None, ""
        if repo != target.old_repo:
            raise UpdateError(
                f"image repository mismatch: link points to {repo}, but the "
                f"{target.project}/{target.role} container currently uses "
                f"{target.old_repo}"
            )
        if digest:
            digest = resolve_digest_ref(ref)
        elif tag:
            digest = resolve_tag_digest(repo, tag) or ""
            if not digest:
                raise UpdateError(f"tag {tag!r} not found for {repo}")
        else:
            raise UpdateError(f"image reference has no tag or digest: {ref}")
        expected = tag if tag and re.fullmatch(r"[0-9a-f]{40}", tag) else None
        return ResolvedLink(
            repo=repo,
            digest=digest,
            tag=tag,
            expected_revision=expected,
            source=link.original,
            warnings=warnings,
        )

    raise UpdateError(f"unsupported link kind: {link.kind}")


def _warning(report: ValidationReport, message: str, strict: bool) -> None:
    report.warnings.append(message)
    if strict:
        raise UpdateError(f"strict validation failed: {message}")


def validate_image(
    repo: str,
    digest: str,
    container_port: int | None,
    expected_revision: str | None,
    strict: bool,
    pull_check: bool,
    expected_source_repo: str | None = None,
) -> ValidationReport:
    report = ValidationReport()
    ref = f"{repo}@{digest}"

    raw = run_command(["skopeo", "inspect", "--raw", f"docker://{ref}"])
    if raw.returncode != 0:
        raise UpdateError(
            f"cannot read manifest for {ref}: {raw.stderr.strip() or 'registry error'}"
        )
    try:
        manifest = json.loads(raw.stdout)
    except json.JSONDecodeError as exc:
        raise UpdateError(f"cannot parse manifest for {ref}: {exc}") from exc

    manifests = manifest.get("manifests")
    if not isinstance(manifests, list):
        raise UpdateError(
            f"{ref} is a single-architecture manifest; the platform requires "
            "a multi-arch index with linux/amd64 and linux/arm64"
        )

    entries = [
        item
        for item in manifests
        if isinstance(item, dict)
        and (item.get("platform") or {}).get("os") == "linux"
        and (item.get("platform") or {}).get("architecture") in ("amd64", "arm64")
    ]
    platforms = sorted(
        {
            f"linux/{(item.get('platform') or {}).get('architecture')}"
            for item in entries
        }
    )
    report.platforms = platforms
    for needed in ("linux/amd64", "linux/arm64"):
        if needed not in platforms:
            raise UpdateError(
                f"{ref} does not provide {needed}; found: "
                f"{', '.join(platforms) or 'none'}"
            )

    for item in entries:
        platform_digest = str(item.get("digest") or "")
        arch = str((item.get("platform") or {}).get("architecture"))
        if not DIGEST_RE.fullmatch(platform_digest):
            raise UpdateError(f"{ref} has an invalid {arch} manifest digest")
        platform_ref = f"{repo}@{platform_digest}"
        platform_raw = run_command(
            ["skopeo", "inspect", "--raw", f"docker://{platform_ref}"]
        )
        if platform_raw.returncode != 0:
            raise UpdateError(
                f"cannot read {arch} manifest {platform_ref}: "
                f"{platform_raw.stderr.strip() or 'registry error'}"
            )

        config_result = run_command(
            ["skopeo", "inspect", "--config", f"docker://{platform_ref}"]
        )
        if config_result.returncode != 0:
            report.skipped.append(f"config blob unavailable for linux/{arch}")
            _warning(
                report,
                f"config blob unavailable for linux/{arch}; "
                "manifest validation only",
                strict,
            )
            continue
        try:
            config = json.loads(config_result.stdout)
        except json.JSONDecodeError as exc:
            report.skipped.append(f"config JSON unreadable for linux/{arch}")
            _warning(
                report,
                f"config JSON unreadable for linux/{arch}: {exc}",
                strict,
            )
            continue
        report.configs[arch] = config
        report.checks[f"platform:linux/{arch}"] = True

        if config.get("os") != "linux" or config.get("architecture") != arch:
            raise UpdateError(
                f"{platform_ref} config reports "
                f"{config.get('os')}/{config.get('architecture')}, expected linux/{arch}"
            )

        rootfs = config.get("rootfs") or {}
        if not rootfs.get("diff_ids"):
            raise UpdateError(f"{platform_ref} has an empty rootfs")

        container_config = config.get("config") or {}
        if not (container_config.get("Entrypoint") or container_config.get("Cmd")):
            raise UpdateError(f"{platform_ref} declares neither Entrypoint nor Cmd")

        exposed = container_config.get("ExposedPorts")
        keys = {str(key) for key in exposed} if exposed else set()
        if not exposed:
            _warning(
                report,
                f"linux/{arch} does not declare ExposedPorts; cannot verify "
                f"container port {container_port}",
                strict,
            )
        elif container_port is not None:
            if (
                f"{container_port}/tcp" not in keys
                and f"{container_port}/udp" not in keys
                and str(container_port) not in keys
            ):
                _warning(
                    report,
                    f"linux/{arch} ExposedPorts declares {sorted(keys)}, but the "
                    f"Deployment uses containerPort {container_port}",
                    strict,
                )

        user = str(container_config.get("User") or "")
        if not user or user in ("0", "root"):
            _warning(
                report,
                f"linux/{arch} image user is {user or 'unset'}; the pod "
                "securityContext still enforces non-root",
                strict,
            )

        for entry in container_config.get("Env") or []:
            key = str(entry).split("=", 1)[0]
            if SECRET_ENV_RE.search(key):
                _warning(
                    report,
                    f"linux/{arch} image config contains secret-like env "
                    f"{key} (value redacted)",
                    strict,
                )

        labels = container_config.get("Labels") or {}
        source = str(labels.get("org.opencontainers.image.source") or "")
        if source:
            if "github.com" not in source:
                _warning(
                    report,
                    f"linux/{arch} image source label is not a GitHub URL: {source}",
                    strict,
                )
            elif expected_source_repo:
                parts = urlparse(source).path.strip("/").split("/")
                actual = (
                    f"{parts[0]}/{parts[1].removesuffix('.git')}".lower()
                    if len(parts) >= 2
                    else ""
                )
                if actual != expected_source_repo.lower():
                    raise UpdateError(
                        f"linux/{arch} image source {actual!r} does not match "
                        f"the linked repository {expected_source_repo!r}"
                    )
        else:
            _warning(
                report,
                f"linux/{arch} image has no org.opencontainers.image.source label",
                strict,
            )

        revision = str(labels.get("org.opencontainers.image.revision") or "")
        if expected_revision and revision:
            if not (
                expected_revision.startswith(revision)
                or revision.startswith(expected_revision)
            ):
                raise UpdateError(
                    f"{platform_ref} revision label {revision!r} does not match "
                    f"the source commit {expected_revision!r}"
                )
        elif expected_revision and not revision:
            _warning(
                report,
                f"linux/{arch} image has no org.opencontainers.image.revision label",
                strict,
            )

        report.evidence[f"linux/{arch}"] = {
            "user": user or "(unset)",
            "source": source or "(missing)",
            "revision": revision or "(missing)",
            "entrypoint": container_config.get("Entrypoint") or [],
            "cmd": container_config.get("Cmd") or [],
            "exposed_ports": sorted(keys),
        }

    if pull_check:
        pulled = run_command(
            ["docker", "pull", "--platform", "linux/amd64", f"{repo}@{digest}"]
        )
        if pulled.returncode != 0:
            _warning(
                report,
                "optional pull check failed; registry-only validation stands",
                strict,
            )

    return report


def replace_image(path: Path, old_image: str, new_image: str) -> None:
    text = path.read_text(encoding="utf-8")
    lines = text.splitlines(keepends=True)
    matches = 0
    for index, line in enumerate(lines):
        stripped = line.strip()
        if not stripped.startswith("image:"):
            continue
        raw_value = stripped.split(":", 1)[1].strip()
        quote = raw_value[0] if raw_value[:1] in ("'", '"') else ""
        value = raw_value.strip("'\"")
        if value != old_image:
            continue
        matches += 1
        prefix = line[: len(line) - len(line.lstrip())]
        suffix = "\n" if line.endswith("\n") else ""
        lines[index] = f"{prefix}image: {quote}{new_image}{quote}{suffix}"
    if matches != 1:
        raise UpdateError(
            f"expected exactly one image line {old_image!r} in {path}, found {matches}"
        )
    path.write_text("".join(lines), encoding="utf-8")


def _scan_project_images(root: Path) -> list[dict[str, str]]:
    rows: list[dict[str, str]] = []
    for project_dir in sorted((root / "deploy" / "projects").glob("*")):
        workload_dir = project_dir / "workloads"
        if not workload_dir.is_dir():
            continue
        for _, deployment, container, image, _ in _iter_deployments(workload_dir):
            repo, digest = _image_parts(image)
            rows.append(
                {
                    "project": f"proj-{project_dir.name}",
                    "deployment": deployment,
                    "container": container,
                    "repo": repo,
                    "digest": digest or "(tag-pinned)",
                }
            )
    return rows


def _pins_table(
    root: Path, digest_overrides: dict[tuple[str, str], str] | None = None
) -> str:
    rows = _scan_project_images(root)
    for row in rows:
        key = (row["deployment"], row["container"])
        if digest_overrides and key in digest_overrides:
            row["digest"] = digest_overrides[key]
    lines = [
        '<div class="overflow-x-auto">',
        '  <table class="w-full text-left text-xs">',
        '    <thead><tr class="border-b border-slate-700 text-slate-400">',
        '      <th class="py-2 pr-3">Project</th>',
        '      <th class="py-2 pr-3">Workload</th>',
        '      <th class="py-2 pr-3">Container</th>',
        '      <th class="py-2 pr-3">Image</th>',
        '      <th class="py-2 pr-3">Digest</th>',
        "    </tr></thead>",
        "    <tbody>",
    ]
    for row in rows:
        lines.append(
            '      <tr class="border-b border-slate-800/60">'
            f'<td class="py-2 pr-3 font-mono text-cyan-300">{html.escape(row["project"])}</td>'
            f'<td class="py-2 pr-3 font-mono">{html.escape(row["deployment"])}</td>'
            f'<td class="py-2 pr-3 font-mono">{html.escape(row["container"])}</td>'
            f'<td class="py-2 pr-3 font-mono break-all">{html.escape(row["repo"])}</td>'
            f'<td class="py-2 pr-3 font-mono break-all">{html.escape(row["digest"])}</td>'
            "</tr>"
        )
    lines.extend(["    </tbody>", "  </table>", "</div>"])
    return "\n".join(lines)


def _replace_digest_refs(
    text: str, old_image: str, old_repo: str, old_digest: str, new_image: str
) -> str:
    if old_image and "@" not in old_image:
        text = re.sub(
            r"(?<![\w.-])" + re.escape(old_image) + r"(?![\w.-])",
            lambda _: new_image,
            text,
        )
    if not old_digest:
        return text
    new_repo, new_digest = new_image.rsplit("@", 1)
    text = text.replace(f"{old_repo}@{old_digest}", new_image)
    old_short = old_digest[7:15]
    new_short = new_digest[7:15]
    return re.sub(
        re.escape(old_repo)
        + r"@sha256:"
        + re.escape(old_short)
        + r"[0-9a-f]{0,56}(?:\.\.\.)?",
        f"{new_repo}@sha256:{new_short}...",
        text,
    )


def update_html(
    root: Path,
    updates: list[ImageUpdate],
    write: bool = True,
    warnings: list[str] | None = None,
    digest_overrides: dict[tuple[str, str], str] | None = None,
) -> bool:
    warnings = warnings if warnings is not None else []
    path = root / "docs" / "platform-architecture.html"
    original = path.read_text(encoding="utf-8")
    updated = original
    pattern = re.compile(
        re.escape(PINS_START) + r".*?" + re.escape(PINS_END), re.DOTALL
    )
    if not pattern.search(updated):
        raise UpdateError(
            f"{path} has no {PINS_START} / {PINS_END} block; merge the "
            "update-project-images setup PR before using this skill"
        )
    if digest_overrides is None:
        digest_overrides = {
            (update.target.deployment, update.target.container): update.digest
            for update in updates
        }
    table = _pins_table(root, digest_overrides)
    table = "\n".join("        " + line for line in table.splitlines())
    updated = pattern.sub(
        lambda _: f"{PINS_START}\n{table}\n        {PINS_END}", updated, count=1
    )

    for update in updates:
        target = update.target
        old_repo = target.old_repo
        new_short = update.digest[7:15]

        node_pattern = re.compile(
            r'(<g[^>]*id="node-' + re.escape(target.deployment) + r'"[^>]*>.*?</g>)',
            re.DOTALL,
        )
        node_replaced = False

        def replace_node(match: re.Match) -> str:
            nonlocal node_replaced
            block = match.group(1)
            new_block = _replace_digest_refs(
                block, target.old_image, old_repo, target.old_digest, update.new_image
            )
            new_block = re.sub(
                r"Pinned @sha256:[0-9a-f]{8}",
                f"Pinned @sha256:{new_short}",
                new_block,
            )
            if new_block != block:
                node_replaced = True
            return new_block

        updated = node_pattern.sub(replace_node, updated)
        if not node_replaced:
            warnings.append(
                f"no topology node reference changed for {target.deployment!r}; "
                "the pins table still records the new digest"
            )

        key = r"(?:'|\")?" + re.escape(target.deployment) + r"(?:'|\")?"
        detail_pattern = re.compile(
            r"(?ms)^(?P<indent>[ \t]*)"
            + key
            + r":\s*\{.*?^\1\},?",
        )
        detail_replaced = False

        def replace_detail(match: re.Match) -> str:
            nonlocal detail_replaced
            block = match.group(0)
            new_block = _replace_digest_refs(
                block, target.old_image, old_repo, target.old_digest, update.new_image
            )
            if new_block != block:
                detail_replaced = True
            return new_block

        updated = detail_pattern.sub(replace_detail, updated)
        if not detail_replaced:
            warnings.append(
                f"no JS detail reference changed for {target.deployment!r}; "
                "the pins table still records the new digest"
            )

        if f"{old_repo}@{target.old_digest}" in updated:
            warnings.append(
                f"old digest for {target.deployment} still appears elsewhere "
                "in the handbook; review the remaining references"
            )

    for update in updates:
        if update.digest not in updated:
            raise UpdateError(
                f"new digest for {update.target.deployment} missing from {path}"
            )

    changed = updated != original
    if changed and write:
        path.write_text(updated, encoding="utf-8")
    return changed


def _parse_image_args(image_args: list[str]) -> list[tuple[str, str]]:
    requests: list[tuple[str, str]] = []
    seen: set[str] = set()
    for item in image_args:
        if "=" not in item:
            raise UpdateError(
                f"invalid --image {item!r}; use ROLE=LINK, for example "
                "frontend=https://github.com/..."
            )
        role, link = item.split("=", 1)
        role = role.strip().lower()
        if role in seen:
            raise UpdateError(f"duplicate role in request: {role!r}")
        seen.add(role)
        requests.append((role, link.strip()))
    if not requests:
        raise UpdateError("no --image arguments supplied")
    return requests


def _summary_images(updates: list[ImageUpdate], root: Path) -> list[dict]:
    images = []
    for update in updates:
        target = update.target
        report = update.validation
        images.append(
            {
                "role": target.role,
                "deployment": target.deployment,
                "container": target.container,
                "path": str(target.path.relative_to(root)),
                "old_image": target.old_image,
                "new_image": update.new_image,
                "tag": update.tag,
                "digest": update.digest,
                "source": update.source,
                "noop": update.new_image == target.old_image,
                "platforms": report.platforms if report else [],
                "checks": report.checks if report else {},
                "evidence": report.evidence if report else {},
                "warnings": report.warnings if report else [],
                "resolution_warnings": update.resolution_warnings,
                "skipped": report.skipped if report else [],
            }
        )
    return images


def run_update(
    root: Path,
    project: str,
    image_args: list[str],
    dry_run: bool,
    strict: bool,
    pull_check: bool,
    summary_file: Path | None = None,
    allow_unmatched_job: bool = False,
) -> dict:
    root = Path(root).resolve()
    requests = _parse_image_args(image_args)
    updates: list[ImageUpdate] = []
    seen_targets: dict[tuple[Path, str], str] = {}

    for role, link_text in requests:
        target = discover_target(root, project, role)
        key = (target.path, target.container)
        if key in seen_targets:
            raise UpdateError(
                f"roles {seen_targets[key]!r} and {role!r} both resolve to "
                f"{target.deployment}/{target.container}; use one role per container"
            )
        seen_targets[key] = role

        link = parse_link(link_text)
        resolved = resolve_link(target, link, allow_unmatched_job)
        if not target.old_digest:
            if ":" not in target.old_image.rsplit("/", 1)[-1]:
                raise UpdateError(
                    f"current image has no tag or digest: {target.old_image}"
                )
            target.old_digest = resolve_digest_ref(target.old_image)

        new_image = f"{target.old_repo}@{resolved.digest}"
        noop = target.old_image == new_image
        if noop:
            validation = None
        else:
            validation = validate_image(
                target.old_repo,
                resolved.digest,
                target.container_port,
                resolved.expected_revision,
                strict,
                pull_check,
                expected_source_repo=resolved.source_repo,
            )
        updates.append(
            ImageUpdate(
                target=target,
                source=resolved.source,
                tag=resolved.tag,
                digest=resolved.digest,
                new_image=new_image,
                validation=validation,
                resolution_warnings=resolved.warnings,
            )
        )

    changed = any(update.new_image != update.target.old_image for update in updates)
    html_updates = [
        update for update in updates if update.new_image != update.target.old_image
    ]
    html_changed = False
    html_warnings: list[str] = []
    if html_updates:
        # Compute first so a missing pins block stops before any YAML is edited.
        overrides = {
            (update.target.deployment, update.target.container): update.digest
            for update in html_updates
        }
        html_changed = update_html(
            root,
            html_updates,
            write=False,
            warnings=html_warnings,
            digest_overrides=overrides,
        )
        if not dry_run:
            for update in html_updates:
                replace_image(
                    update.target.path, update.target.old_image, update.new_image
                )
            update_html(root, html_updates, write=True, warnings=[])

    summary = {
        "project": normalize_project(project),
        "dry_run": dry_run,
        "changed": changed,
        "html_changed": html_changed,
        "html_warnings": html_warnings,
        "images": _summary_images(updates, root),
    }
    if summary_file:
        summary_file = Path(summary_file)
        summary_file.parent.mkdir(parents=True, exist_ok=True)
        summary_file.write_text(json.dumps(summary, indent=2) + "\n", encoding="utf-8")

    for image in summary["images"]:
        state = "NO-OP" if image["noop"] else "UPDATE"
        print(
            f"[{state}] {image['role']} {image['deployment']}: "
            f"{image['old_image'].rsplit(':', 1)[-1][:12]} -> "
            f"{image['digest'][:19]}"
        )
        for warning in image["resolution_warnings"]:
            print(f"  resolution warning: {warning}", file=sys.stderr)
        for warning in image["warnings"]:
            print(f"  warning: {warning}", file=sys.stderr)
        for skipped in image["skipped"]:
            print(f"  skipped: {skipped}", file=sys.stderr)
    for warning in summary["html_warnings"]:
        print(f"  handbook warning: {warning}", file=sys.stderr)
    if dry_run:
        print("dry run: no files were changed")
    elif not changed:
        print("all requested images are already pinned; no PR is needed")
    return summary


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(
        description="Update hosted project image digests and validate the images."
    )
    parser.add_argument("--root", default=".", help="repository root")
    parser.add_argument("--project", required=True, help="project slug or proj-<slug>")
    parser.add_argument(
        "--image",
        action="append",
        required=True,
        metavar="ROLE=LINK",
        help="container role and GHCR/Actions/digest link; repeat for more images",
    )
    parser.add_argument("--dry-run", action="store_true")
    parser.add_argument("--strict", action="store_true")
    parser.add_argument("--pull-check", action="store_true")
    parser.add_argument(
        "--allow-unmatched-job",
        action="store_true",
        help="allow an Actions run whose job names do not mention the role",
    )
    parser.add_argument("--summary-file", type=Path)
    args = parser.parse_args(argv)

    try:
        run_update(
            root=Path(args.root),
            project=args.project,
            image_args=args.image,
            dry_run=args.dry_run,
            strict=args.strict,
            pull_check=args.pull_check,
            summary_file=args.summary_file,
            allow_unmatched_job=args.allow_unmatched_job,
        )
    except UpdateError as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 2
    return 0


if __name__ == "__main__":
    sys.exit(main())
