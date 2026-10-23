#!/usr/bin/env python3
"""
review-cycle.py — Core Verification, Risk Classification, Receipt & Gate Engine.

Implements the deterministic verification and receipt engine for nacfson_pipeline
using Python 3.12 standard library only.
"""

from __future__ import annotations

import argparse
from datetime import datetime, timezone
import fnmatch
import hashlib
import json
import os
from pathlib import Path, PurePosixPath
import re
import subprocess
import sys
import tempfile

REPO_ROOT = Path(__file__).resolve().parent.parent
POLICY_PATH = REPO_ROOT / "config" / "review-policy.json"
BREAKER_PATH = REPO_ROOT / "config" / "review-circuit-breaker.json"
DEFAULT_RECEIPT_PATH = REPO_ROOT / ".git" / "review-cycle" / "receipt.json"
DEFAULT_STATE_PATH = REPO_ROOT / ".git" / "review-cycle" / "state.json"

TIER_ORDER = {"Low": 0, "Medium": 1, "High": 2}
ORDER_TO_TIER = {0: "Low", 1: "Medium", 2: "High"}
APPROVALS_BY_TIER = {"Low": 0, "Medium": 1, "High": 2}
DEPTH_BY_TIER = {"Low": "Quick", "Medium": "Standard", "High": "Deep"}
VALID_RESOLUTIONS = {"fixed", "false_positive", "intentional", "wont_fix"}
SUPPRESSING_RESOLUTIONS = {"false_positive", "intentional", "wont_fix"}
CODE_REVIEW_PIPELINE = "code-review"
CODE_REVIEW_SKILL = ".agents/skills/code-review/SKILL.md"


def code_review_required(detail: str) -> str:
    return (
        f"{detail} Run the code-review skill ({CODE_REVIEW_SKILL}) before git commit. "
        "Do not self-record a receipt."
    )



def get_receipt_path() -> Path:
    override = os.environ.get("REVIEW_RECEIPT_PATH", "").strip()
    return Path(override) if override else DEFAULT_RECEIPT_PATH


def get_state_path() -> Path:
    override = os.environ.get("REVIEW_STATE_PATH", "").strip()
    return Path(override) if override else DEFAULT_STATE_PATH


def load_policy(path: Path = POLICY_PATH) -> dict:
    return json.loads(path.read_text(encoding="utf-8"))


def load_breaker(path: Path = BREAKER_PATH) -> dict:
    return json.loads(path.read_text(encoding="utf-8"))


def _normalize_rel_path(rel_path: str) -> str:
    clean = rel_path.strip().replace("\\", "/")
    while clean.startswith("./"):
        clean = clean[2:]
    return clean


def _matches_pattern(rel_path: str, pattern: str) -> bool:
    norm_path = _normalize_rel_path(rel_path)
    norm_pat = _normalize_rel_path(pattern)
    if not norm_path or not norm_pat:
        return False
    if norm_pat.endswith("/**"):
        prefix = norm_pat[:-3]
        return norm_path == prefix or norm_path.startswith(prefix + "/")
    if norm_pat.startswith("**/"):
        suffix = norm_pat[3:]
        if norm_path == suffix or norm_path.endswith("/" + suffix):
            return True
        return fnmatch.fnmatch(norm_path, norm_pat)
    if "/" not in norm_pat:
        return fnmatch.fnmatch(norm_path, norm_pat) or fnmatch.fnmatch(
            PurePosixPath(norm_path).name, norm_pat
        )
    return norm_path == norm_pat or fnmatch.fnmatch(norm_path, norm_pat)


def _matches_any(rel_path: str, patterns: list[str]) -> bool:
    return any(_matches_pattern(rel_path, p) for p in patterns)


def compute_diff_sha256(diff_text: str) -> str:
    if not diff_text:
        return "EMPTY"
    normalized = diff_text.replace("\r\n", "\n").replace("\r", "\n")
    kept_lines: list[str] = []
    for line in normalized.split("\n"):
        if line.startswith("index "):
            continue
        kept_lines.append(line)
    cleaned = "\n".join(kept_lines).strip()
    if not cleaned:
        return "EMPTY"
    return hashlib.sha256(cleaned.encode("utf-8")).hexdigest()


def get_changed_files(
    scope: str = "staged",
    changed_files_override: list[str] | None = None,
) -> list[str]:
    if changed_files_override is not None:
        return [_normalize_rel_path(f) for f in changed_files_override if f.strip()]
    if "REVIEW_FORCE_CHANGED" in os.environ:
        raw = os.environ["REVIEW_FORCE_CHANGED"].strip()
        if not raw:
            return []
        return [_normalize_rel_path(f) for f in raw.split(",") if f.strip()]

    try:
        if scope == "staged":
            out = subprocess.check_output(
                ["git", "diff", "--cached", "--name-only"],
                cwd=REPO_ROOT,
                text=True,
                stderr=subprocess.DEVNULL,
            )
            return [_normalize_rel_path(line) for line in out.splitlines() if line.strip()]
        if scope == "worktree":
            out = subprocess.check_output(
                ["git", "status", "--porcelain"],
                cwd=REPO_ROOT,
                text=True,
                stderr=subprocess.DEVNULL,
            )
            files: list[str] = []
            for line in out.splitlines():
                if len(line) > 3:
                    path_part = line[3:].strip()
                    if " -> " in path_part:
                        path_part = path_part.split(" -> ", 1)[1]
                    files.append(_normalize_rel_path(path_part))
            return files
        if scope == "branch":
            out = subprocess.check_output(
                ["git", "diff", "main...HEAD", "--name-only"],
                cwd=REPO_ROOT,
                text=True,
                stderr=subprocess.DEVNULL,
            )
            return [_normalize_rel_path(line) for line in out.splitlines() if line.strip()]
    except Exception:
        return []
    return []


def get_diff_text(
    scope: str = "staged",
    diff_text_override: str | None = None,
) -> str:
    if diff_text_override is not None:
        return diff_text_override
    if "REVIEW_FORCE_DIFF" in os.environ:
        return os.environ["REVIEW_FORCE_DIFF"]

    try:
        if scope == "staged":
            return subprocess.check_output(
                ["git", "diff", "--cached"],
                cwd=REPO_ROOT,
                text=True,
                stderr=subprocess.DEVNULL,
            )
        if scope == "worktree":
            return subprocess.check_output(
                ["git", "diff", "HEAD"],
                cwd=REPO_ROOT,
                text=True,
                stderr=subprocess.DEVNULL,
            )
        if scope == "branch":
            return subprocess.check_output(
                ["git", "diff", "main...HEAD"],
                cwd=REPO_ROOT,
                text=True,
                stderr=subprocess.DEVNULL,
            )
    except Exception:
        return ""
    return ""


def classify_risk(
    changed_files: list[str],
    policy: dict,
    breaker: dict,
    author: str | None = None,
    classifier2_override: str | None = None,
) -> dict:
    skip_paths = policy.get("skip_paths", [])
    high_paths = policy.get("high_paths", [])
    low_paths = policy.get("low_paths", [])
    trusted_authors = set(policy.get("trusted_authors", []))

    non_skipped = [f for f in changed_files if not _matches_any(f, skip_paths)]

    if not non_skipped:
        path_tier = "Low"
    elif any(_matches_any(f, high_paths) for f in non_skipped):
        path_tier = "High"
    elif all(_matches_any(f, low_paths) for f in non_skipped):
        path_tier = "Low"
    else:
        path_tier = "Medium"

    effective_idx = TIER_ORDER[path_tier]
    cb_state = str(breaker.get("state", "CLOSED")).upper()

    if cb_state == "OPEN" and effective_idx == TIER_ORDER["Low"]:
        effective_idx = TIER_ORDER["Medium"]

    if author and author not in trusted_authors:
        effective_idx = max(effective_idx, min(TIER_ORDER["High"], TIER_ORDER[path_tier] + 1))

    if classifier2_override in TIER_ORDER:
        effective_idx = max(effective_idx, TIER_ORDER[classifier2_override])

    effective_tier = ORDER_TO_TIER[effective_idx]

    return {
        "path_tier": path_tier,
        "classifier2_override": classifier2_override,
        "effective_tier": effective_tier,
        "required_human_approvals": APPROVALS_BY_TIER[effective_tier],
        "review_depth": DEPTH_BY_TIER[effective_tier],
        "circuit_breaker": cb_state,
    }


def _load_or_init_state(state_path: Path) -> dict:
    if state_path.exists():
        try:
            data = json.loads(state_path.read_text(encoding="utf-8"))
            if isinstance(data, dict):
                data.setdefault("round", 0)
                data.setdefault("last_diff_sha256", None)
                data.setdefault("last_gate_status", None)
                data.setdefault("resolved_findings", [])
                data.setdefault(
                    "noise_stats",
                    {"fixed": 0, "false_positive": 0, "intentional": 0, "wont_fix": 0},
                )
                return data
        except Exception:
            pass
    return {
        "round": 0,
        "last_diff_sha256": None,
        "last_gate_status": None,
        "resolved_findings": [],
        "noise_stats": {"fixed": 0, "false_positive": 0, "intentional": 0, "wont_fix": 0},
    }


def _is_suppressed_by_state(finding: dict, state: dict) -> bool:
    f_file = _normalize_rel_path(str(finding.get("file", "")))
    f_line = finding.get("line_start")
    f_title = str(finding.get("title", ""))
    for entry in state.get("resolved_findings", []):
        if entry.get("resolution") not in SUPPRESSING_RESOLUTIONS:
            continue
        if (
            _normalize_rel_path(str(entry.get("file", ""))) == f_file
            and entry.get("line_start") == f_line
            and str(entry.get("title", "")) == f_title
        ):
            return True
    return False


def _compute_gate_status(
    round_num: int,
    max_rounds: int,
    verification_passed: bool,
    important_open: int,
    security_open: int,
) -> str:
    if round_num > max_rounds or (
        round_num == max_rounds and (important_open > 0 or security_open > 0)
    ):
        return "ESCALATE_TO_HUMAN"
    if verification_passed and important_open == 0 and security_open == 0 and round_num <= max_rounds:
        return "PASS"
    return "BLOCKED"


def validate_and_record_receipt(
    payload: dict,
    receipt_path: Path,
    state_path: Path,
    diff_sha: str,
    changed_files: list[str],
    policy: dict,
    breaker: dict,
) -> dict:
    state = _load_or_init_state(state_path)
    last_sha = state.get("last_diff_sha256")
    if state["round"] == 0 or not last_sha:
        state["round"] = 1
        state["last_diff_sha256"] = diff_sha
    elif last_sha != diff_sha:
        if state.get("last_gate_status") == "PASS":
            state["round"] = 1
        else:
            state["round"] = int(state["round"]) + 1
        state["last_diff_sha256"] = diff_sha

    round_num = int(state["round"])
    re_review_mode = "diff_only" if round_num > 1 else "full"

    skip_paths = policy.get("skip_paths", [])
    max_nits = int(policy.get("max_nits", 2))
    max_rounds = int(policy.get("max_rounds", 3))

    kept_findings: list[dict] = []
    nit_count = 0

    for raw in payload.get("findings", []) or []:
        if not isinstance(raw, dict):
            continue
        file_val = raw.get("file")
        if not isinstance(file_val, str) or not file_val.strip():
            continue
        norm_file = _normalize_rel_path(file_val)
        if _matches_any(norm_file, skip_paths):
            continue

        category = str(raw.get("category", "")).strip().lower()
        if category == "style":
            continue

        line_start = raw.get("line_start")
        line_end = raw.get("line_end")
        if (
            isinstance(line_start, bool)
            or isinstance(line_end, bool)
            or not isinstance(line_start, int)
            or not isinstance(line_end, int)
            or line_start < 1
            or line_end < line_start
        ):
            continue

        evidence = raw.get("evidence")
        if not isinstance(evidence, str) or not evidence.strip():
            continue

        if _is_suppressed_by_state(raw, state):
            continue

        severity = str(raw.get("severity", "Nit"))
        if severity == "Nit":
            if nit_count >= max_nits:
                continue
            nit_count += 1

        item = dict(raw)
        item["file"] = norm_file
        item.setdefault("status", "open")
        kept_findings.append(item)

    important_open = sum(
        1
        for f in kept_findings
        if f.get("severity") == "Important" and f.get("status", "open") == "open"
    )
    security_open = sum(
        1
        for f in kept_findings
        if f.get("severity") == "Security" and f.get("status", "open") == "open"
    )
    total_nits = sum(1 for f in kept_findings if f.get("severity") == "Nit")
    pre_existing_count = sum(1 for f in kept_findings if f.get("severity") == "Pre-existing")

    verification = payload.get("verification") or {}
    verification_passed = bool(verification.get("passed", False))
    commands_run = list(verification.get("commands_run", []))

    gate_status = _compute_gate_status(
        round_num=round_num,
        max_rounds=max_rounds,
        verification_passed=verification_passed,
        important_open=important_open,
        security_open=security_open,
    )

    risk_info = classify_risk(
        changed_files=changed_files,
        policy=policy,
        breaker=breaker,
        author=payload.get("author"),
        classifier2_override=payload.get("classifier2_override"),
    )

    receipt = {
        "schema_version": "1.0",
        "timestamp": datetime.now(timezone.utc).isoformat(),
        "agent_harness": payload.get("agent_harness", "unknown"),
        "scope": payload.get("scope", "staged"),
        "diff_sha256": diff_sha,
        "changed_files": list(changed_files),
        "round": round_num,
        "re_review_mode": re_review_mode,
        "risk": risk_info,
        "verification": {
            "passed": verification_passed,
            "commands_run": commands_run,
        },
        "findings": kept_findings,
        "summary": {
            "important_open": important_open,
            "security_open": security_open,
            "nit_count": total_nits,
            "pre_existing_count": pre_existing_count,
        },
        "gate_status": gate_status,
        "pipeline": payload.get("pipeline"),
    }

    state["last_gate_status"] = gate_status
    state_path.parent.mkdir(parents=True, exist_ok=True)
    state_path.write_text(json.dumps(state, indent=2) + "\n", encoding="utf-8")

    receipt_path.parent.mkdir(parents=True, exist_ok=True)
    receipt_path.write_text(json.dumps(receipt, indent=2) + "\n", encoding="utf-8")

    return receipt


def resolve_finding(
    receipt_path: Path,
    state_path: Path,
    finding_id: str,
    resolution: str,
    reason: str,
) -> dict:
    if resolution not in VALID_RESOLUTIONS:
        raise ValueError(f"Invalid resolution '{resolution}'. Must be one of {sorted(VALID_RESOLUTIONS)}.")
    if not isinstance(reason, str) or not reason.strip():
        raise ValueError("Resolution reason must be non-empty.")
    if not receipt_path.exists():
        raise FileNotFoundError(f"Receipt file not found at {receipt_path}")

    receipt = json.loads(receipt_path.read_text(encoding="utf-8"))
    state = _load_or_init_state(state_path)

    target = None
    for f in receipt.get("findings", []):
        if f.get("id") == finding_id:
            target = f
            break

    if target is None:
        raise KeyError(f"Finding '{finding_id}' not found in receipt.")

    target["status"] = "resolved"
    target["resolution"] = resolution
    target["resolution_reason"] = reason.strip()

    state.setdefault("resolved_findings", []).append(
        {
            "id": finding_id,
            "file": target.get("file"),
            "line_start": target.get("line_start"),
            "line_end": target.get("line_end"),
            "title": target.get("title", ""),
            "resolution": resolution,
            "reason": reason.strip(),
        }
    )
    noise_stats = state.setdefault(
        "noise_stats",
        {"fixed": 0, "false_positive": 0, "intentional": 0, "wont_fix": 0},
    )
    noise_stats[resolution] = int(noise_stats.get(resolution, 0)) + 1

    findings = receipt.get("findings", [])
    important_open = sum(
        1 for f in findings if f.get("severity") == "Important" and f.get("status", "open") == "open"
    )
    security_open = sum(
        1 for f in findings if f.get("severity") == "Security" and f.get("status", "open") == "open"
    )
    nit_count = sum(1 for f in findings if f.get("severity") == "Nit")
    pre_existing_count = sum(1 for f in findings if f.get("severity") == "Pre-existing")

    receipt["summary"] = {
        "important_open": important_open,
        "security_open": security_open,
        "nit_count": nit_count,
        "pre_existing_count": pre_existing_count,
    }

    max_rounds = 3
    if POLICY_PATH.exists():
        try:
            max_rounds = int(load_policy().get("max_rounds", 3))
        except Exception:
            max_rounds = 3

    round_num = int(receipt.get("round", 1))
    verification_passed = bool(receipt.get("verification", {}).get("passed", False))
    receipt["gate_status"] = _compute_gate_status(
        round_num=round_num,
        max_rounds=max_rounds,
        verification_passed=verification_passed,
        important_open=important_open,
        security_open=security_open,
    )

    state["last_gate_status"] = receipt["gate_status"]
    state_path.parent.mkdir(parents=True, exist_ok=True)
    state_path.write_text(json.dumps(state, indent=2) + "\n", encoding="utf-8")
    receipt_path.write_text(json.dumps(receipt, indent=2) + "\n", encoding="utf-8")

    return receipt


def check_gate(
    scope: str,
    receipt_path: Path,
    diff_text_override: str | None = None,
    changed_files_override: list[str] | None = None,
    require_pipeline: str | None = None,
) -> tuple[bool, list[str]]:
    changed_files = get_changed_files(scope, changed_files_override)
    diff_text = get_diff_text(scope, diff_text_override)
    current_sha = compute_diff_sha256(diff_text)

    if not changed_files or current_sha == "EMPTY":
        return True, []

    errors: list[str] = []
    if not receipt_path.exists():
        detail = (
            f"Missing review receipt at {receipt_path} for scope '{scope}' "
            f"(diff_sha256={current_sha})."
        )
        if require_pipeline == CODE_REVIEW_PIPELINE:
            errors.append(code_review_required(detail))
        else:
            errors.append(detail)
        return False, errors

    try:
        receipt = json.loads(receipt_path.read_text(encoding="utf-8"))
    except Exception as exc:
        return False, [f"Invalid JSON in review receipt at {receipt_path}: {exc}"]

    receipt_sha = receipt.get("diff_sha256")
    if receipt_sha != current_sha:
        errors.append(
            f"Review receipt diff_sha256 mismatch: receipt has '{receipt_sha}', current {scope} diff is '{current_sha}'."
        )

    gate_status = receipt.get("gate_status")
    if gate_status != "PASS":
        summary = receipt.get("summary", {})
        errors.append(
            f"Review gate_status is '{gate_status}' "
            f"(important_open={summary.get('important_open', 0)}, "
            f"security_open={summary.get('security_open', 0)})."
        )
    if require_pipeline and receipt.get("pipeline") != require_pipeline:
        detail = (
            f"Review receipt at {receipt_path} pipeline is {receipt.get('pipeline')!r}, "
            f"not {require_pipeline!r}."
        )
        if require_pipeline == CODE_REVIEW_PIPELINE:
            errors.append(code_review_required(detail))
        else:
            errors.append(detail)


    return len(errors) == 0, errors


def format_markdown_summary(receipt: dict) -> str:
    summary = receipt.get("summary", {})
    imp = int(summary.get("important_open", 0))
    sec = int(summary.get("security_open", 0))
    nit = int(summary.get("nit_count", 0))
    pre = int(summary.get("pre_existing_count", 0))
    risk = receipt.get("risk", {})

    lines = [
        "## Automated Review Receipt",
        f"{imp} Important, {sec} Security, {nit} Nit, {pre} Pre-existing",
        "",
        f"- **Gate Status**: `{receipt.get('gate_status', 'UNKNOWN')}`",
        f"- **Risk Tier**: `{risk.get('effective_tier', 'Medium')}` (depth: `{risk.get('review_depth', 'Standard')}`, required approvals: `{risk.get('required_human_approvals', 1)}`)",
        f"- **Round**: `{receipt.get('round', 1)}` (`{receipt.get('re_review_mode', 'full')}`)",
        f"- **Diff SHA-256**: `{receipt.get('diff_sha256', '')}`",
    ]
    if imp == 0 and sec == 0:
        lines.append("")
        lines.append("Nothing to fix.")

    findings = receipt.get("findings", [])
    if findings:
        lines.append("")
        lines.append("### Findings")
        for f in findings:
            lines.append(
                f"- **[{f.get('severity')}]** `{f.get('file')}:{f.get('line_start')}-{f.get('line_end')}` "
                f"({f.get('id', 'F-?')}): {f.get('title', '')} — `{f.get('status', 'open')}`"
            )
    return "\n".join(lines) + "\n"


def run_domain_verification(
    scope: str = "staged",
    changed_files_override: list[str] | None = None,
    diff_text_override: str | None = None,
) -> dict:
    changed = get_changed_files(scope, changed_files_override)
    diff_text = get_diff_text(scope, diff_text_override)
    diff_sha = compute_diff_sha256(diff_text)

    commands: list[list[str]] = []
    if any(f.startswith("gateway/") for f in changed):
        commands.append(["bash", "-c", "cd gateway && go test -v ./... && go vet ./..."])
    if any(f.startswith("services/vault-proxies/") for f in changed):
        commands.append(
            ["bash", "-c", "cd services/vault-proxies && go test -v ./... && go vet ./..."]
        )
    if any(
        f.startswith(("deploy/", "clusters/", "tests/preflight/"))
        or f == "scripts/platform-preflight.py"
        for f in changed
    ):
        commands.append(["python3", "-m", "unittest", "discover", "tests/preflight"])
    if any(
        f.startswith("tests/review_cycle/") or f == "scripts/review-cycle.py"
        for f in changed
    ):
        commands.append(
            ["python3", "-m", "unittest", "tests.review_cycle.test_review_engine"]
        )

    sync_flag = "--staged" if scope == "staged" else "--worktree"
    if any(
        f.startswith(
            (
                "deploy/",
                "clusters/",
                "gateway/",
                "services/",
                "bootstrap/",
                "contracts/",
                "specs/",
            )
        )
        or f
        in {
            ".specify/memory/constitution.md",
            "scripts/platform-preflight.py",
            "scripts/render-invariants.py",
            "ARCHITECTURE.md",
            "DEPENDENCIES.md",
            "docs/platform-architecture.html",
        }
        for f in changed
    ):
        commands.append(["python3", "scripts/check-arch-doc-sync.py", sync_flag])

    commands_run: list[str] = []
    failures: list[str] = []
    for cmd in commands:
        cmd_str = " ".join(cmd)
        commands_run.append(cmd_str)
        proc = subprocess.run(
            cmd,
            cwd=REPO_ROOT,
            text=True,
            capture_output=True,
        )
        if proc.returncode != 0:
            failures.append(f"{cmd_str}: {(proc.stderr or proc.stdout).strip()}")

    return {
        "passed": len(failures) == 0,
        "scope": scope,
        "changed_files": changed,
        "diff_sha256": diff_sha,
        "commands_run": commands_run,
        "failures": failures,
    }


def _detect_static_findings(
    diff_text: str,
    changed_files: list[str],
    policy: dict,
) -> list[dict]:
    skip_paths = policy.get("skip_paths", [])
    non_skipped = [f for f in changed_files if not _matches_any(f, skip_paths)]
    target_file = non_skipped[0] if non_skipped else (changed_files[0] if changed_files else "unknown")

    findings: list[dict] = []
    added_lines = [
        line[1:]
        for line in diff_text.splitlines()
        if line.startswith("+") and not line.startswith("+++")
    ]
    joined_added = "\n".join(added_lines)

    secret_patterns = (
        r"kind:\s*Secret\b",
        r"stringData\s*:",
        r"(?:POSTGRES_PASSWORD|DB_PASSWORD|VAULT_TOKEN|OPENBAO_TOKEN|CLIENT_SECRET|API_KEY|SECRET_KEY|PRIVATE_KEY)\s*[:=]\s*['\"]?[A-Za-z0-9_\-/+=]{4,}",
        r"BEGIN (?:RSA |EC |OPENSSH )?PRIVATE KEY",
    )
    for pat in secret_patterns:
        m = re.search(pat, joined_added)
        if m:
            findings.append(
                {
                    "id": "F-STATIC-SECRET",
                    "category": "security",
                    "severity": "Security",
                    "file": target_file,
                    "line_start": 1,
                    "line_end": 1,
                    "title": "Plaintext secret or forbidden Secret manifest detected in diff",
                    "evidence": m.group(0),
                }
            )
            break

    if any(f.startswith(("deploy/projects/", "clusters/")) for f in non_skipped):
        if "limits:" not in joined_added and (
            "resources:" in joined_added
            or "kind: Deployment" in joined_added
            or "image:" in joined_added
        ):
            findings.append(
                {
                    "id": "F-STATIC-QOS",
                    "category": "bug",
                    "severity": "Important",
                    "file": target_file,
                    "line_start": 1,
                    "line_end": 1,
                    "title": "Missing Guaranteed QoS limits/requests in project workload",
                    "evidence": joined_added.splitlines()[0] if added_lines else "missing limits",
                }
            )
        if re.search(r"image:\s*[^\s]+:latest\b", joined_added):
            findings.append(
                {
                    "id": "F-STATIC-LATEST",
                    "category": "bug",
                    "severity": "Important",
                    "file": target_file,
                    "line_start": 1,
                    "line_end": 1,
                    "title": "Mutable :latest image reference is forbidden",
                    "evidence": "image: ...:latest",
                }
            )

    return findings


def run_eval_suite(eval_root: Path | None = None) -> dict:
    base = eval_root or (REPO_ROOT / "tests" / "review-evals")
    must_flag_dir = base / "must-flag"
    must_pass_dir = base / "must-pass"

    policy = load_policy()
    breaker = load_breaker()

    must_flag_files = sorted(must_flag_dir.glob("*.json")) if must_flag_dir.exists() else []
    must_pass_files = sorted(must_pass_dir.glob("*.json")) if must_pass_dir.exists() else []

    flagged = 0
    for fpath in must_flag_files:
        fixture = json.loads(fpath.read_text(encoding="utf-8"))
        changed_files = fixture.get("changed_files") or ["deploy/projects/pn/deployment.yaml"]
        diff_text = fixture.get("diff", "+fixture\n")
        candidates = fixture.get("candidate_findings")
        if candidates is None:
            candidates = fixture.get("findings")
        if not candidates:
            candidates = _detect_static_findings(diff_text, changed_files, policy)

        with tempfile.TemporaryDirectory() as tmp:
            r_path = Path(tmp) / "receipt.json"
            s_path = Path(tmp) / "state.json"
            receipt = validate_and_record_receipt(
                payload={
                    "agent_harness": "eval",
                    "verification": fixture.get(
                        "verification", {"passed": True, "commands_run": ["eval"]}
                    ),
                    "findings": candidates,
                },
                receipt_path=r_path,
                state_path=s_path,
                diff_sha=compute_diff_sha256(diff_text),
                changed_files=changed_files,
                policy=policy,
                breaker=breaker,
            )
            if (
                receipt["gate_status"] != "PASS"
                or (
                    receipt["summary"]["important_open"]
                    + receipt["summary"]["security_open"]
                )
                > 0
            ):
                flagged += 1

    false_positives = 0
    for fpath in must_pass_files:
        fixture = json.loads(fpath.read_text(encoding="utf-8"))
        changed_files = fixture.get("changed_files") or ["README.md"]
        diff_text = fixture.get("diff", "+clean\n")
        candidates = fixture.get("candidate_findings")
        if candidates is None:
            candidates = fixture.get("findings")
        if candidates is None:
            candidates = _detect_static_findings(diff_text, changed_files, policy)

        with tempfile.TemporaryDirectory() as tmp:
            r_path = Path(tmp) / "receipt.json"
            s_path = Path(tmp) / "state.json"
            receipt = validate_and_record_receipt(
                payload={
                    "agent_harness": "eval",
                    "verification": fixture.get(
                        "verification", {"passed": True, "commands_run": ["eval"]}
                    ),
                    "findings": candidates,
                },
                receipt_path=r_path,
                state_path=s_path,
                diff_sha=compute_diff_sha256(diff_text),
                changed_files=changed_files,
                policy=policy,
                breaker=breaker,
            )
            if (
                receipt["gate_status"] != "PASS"
                or (
                    receipt["summary"]["important_open"]
                    + receipt["summary"]["security_open"]
                )
                > 0
            ):
                false_positives += 1

    recall = (flagged / len(must_flag_files)) if must_flag_files else 1.0
    fp_rate = (false_positives / len(must_pass_files)) if must_pass_files else 0.0

    return {
        "passed": recall == 1.0 and fp_rate == 0.0,
        "recall": recall,
        "false_positive_rate": fp_rate,
        "must_flag_total": len(must_flag_files),
        "must_flag_caught": flagged,
        "must_pass_total": len(must_pass_files),
        "must_pass_false_positives": false_positives,
    }


def update_circuit_breaker(
    record_incident: str | None = None,
    reset: bool = False,
    breaker_path: Path = BREAKER_PATH,
) -> dict:
    breaker = load_breaker(breaker_path)
    if reset:
        breaker["state"] = "CLOSED"
        breaker["recent_incidents"] = 0
        breaker["incidents"] = []
    elif record_incident:
        incidents = list(breaker.get("incidents", []))
        incidents.append(record_incident)
        breaker["incidents"] = incidents
        breaker["recent_incidents"] = int(breaker.get("recent_incidents", 0)) + 1
        max_inc = int(breaker.get("max_recent_incidents", 2))
        if breaker["recent_incidents"] >= max_inc:
            breaker["state"] = "OPEN"

    breaker_path.write_text(json.dumps(breaker, indent=2) + "\n", encoding="utf-8")
    return breaker


def main() -> int:
    parser = argparse.ArgumentParser(
        description="Deterministic verification, risk classification, and review receipt engine."
    )
    subparsers = parser.add_subparsers(dest="subcommand", required=True)

    p_verify = subparsers.add_parser("verify", help="Run domain verification commands.")
    p_verify.add_argument(
        "--scope", choices=["staged", "worktree", "branch"], default="staged"
    )
    p_verify.add_argument("--json", action="store_true")

    p_risk = subparsers.add_parser("classify-risk", help="Classify change risk tier.")
    p_risk.add_argument(
        "--scope", choices=["staged", "worktree", "branch"], default="staged"
    )
    p_risk.add_argument("--author", default=None)
    p_risk.add_argument(
        "--classifier2-override", choices=["Low", "Medium", "High"], default=None
    )
    p_risk.add_argument("--json", action="store_true")

    p_rec = subparsers.add_parser(
        "record-receipt", help="Validate and record a review receipt."
    )
    p_rec.add_argument(
        "--scope", choices=["staged", "worktree", "branch"], default="staged"
    )
    p_rec.add_argument(
        "--input", default="-", help="Path to JSON payload or '-' for stdin."
    )
    p_rec.add_argument("--json", action="store_true")

    p_res = subparsers.add_parser("resolve", help="Resolve an open finding in receipt.")
    p_res.add_argument("--id", required=True, help="Finding ID to resolve.")
    p_res.add_argument(
        "--resolution",
        required=True,
        choices=["fixed", "false_positive", "intentional", "wont_fix"],
    )
    p_res.add_argument("--reason", required=True, help="Concrete resolution rationale.")
    p_res.add_argument("--json", action="store_true")

    p_gate = subparsers.add_parser(
        "check-gate", help="Verify review receipt matches diff and passes gate."
    )
    p_gate.add_argument(
        "--scope", choices=["staged", "worktree", "branch"], default="staged"
    )
    p_gate.add_argument("--json", action="store_true")
    p_gate.add_argument(
        "--require-pipeline",
        default="",
        help="Reject the receipt unless pipeline equals this value. Commit hooks pass code-review.",
    )

    p_sum = subparsers.add_parser("summary", help="Print review receipt summary.")
    p_sum.add_argument("--markdown", action="store_true")
    p_sum.add_argument("--json", action="store_true")

    p_eval = subparsers.add_parser("eval", help="Run review benchmark eval suite.")
    p_eval.add_argument("--json", action="store_true")

    p_cb = subparsers.add_parser(
        "circuit-breaker", help="Inspect or update review circuit breaker state."
    )
    p_cb.add_argument("--record-incident", default=None)
    p_cb.add_argument("--reset", action="store_true")
    p_cb.add_argument("--json", action="store_true")

    args = parser.parse_args()

    if args.subcommand == "verify":
        res = run_domain_verification(args.scope)
        if args.json:
            print(json.dumps(res, indent=2))
        else:
            status = "PASS" if res["passed"] else "FAIL"
            print(f"[REVIEW-VERIFY {status}] diff_sha256={res['diff_sha256']}")
            for err in res["failures"]:
                print(f"  - {err}", file=sys.stderr)
        return 0 if res["passed"] else 1

    if args.subcommand == "classify-risk":
        policy = load_policy()
        breaker = load_breaker()
        changed = get_changed_files(args.scope)
        res = classify_risk(
            changed,
            policy,
            breaker,
            author=args.author,
            classifier2_override=args.classifier2_override,
        )
        if args.json:
            print(json.dumps(res, indent=2))
        else:
            print(
                f"Risk Tier: {res['effective_tier']} "
                f"(path={res['path_tier']}, depth={res['review_depth']}, "
                f"approvals={res['required_human_approvals']}, cb={res['circuit_breaker']})"
            )
        return 0

    if args.subcommand == "record-receipt":
        if args.input == "-":
            raw = sys.stdin.read()
        else:
            raw = Path(args.input).read_text(encoding="utf-8")
        payload = json.loads(raw)
        payload.setdefault("scope", args.scope)
        policy = load_policy()
        breaker = load_breaker()
        changed = get_changed_files(args.scope)
        diff_sha = compute_diff_sha256(get_diff_text(args.scope))
        receipt = validate_and_record_receipt(
            payload=payload,
            receipt_path=get_receipt_path(),
            state_path=get_state_path(),
            diff_sha=diff_sha,
            changed_files=changed,
            policy=policy,
            breaker=breaker,
        )
        if args.json:
            print(json.dumps(receipt, indent=2))
        else:
            print(format_markdown_summary(receipt), end="")
        return 0

    if args.subcommand == "resolve":
        receipt = resolve_finding(
            receipt_path=get_receipt_path(),
            state_path=get_state_path(),
            finding_id=args.id,
            resolution=args.resolution,
            reason=args.reason,
        )
        if args.json:
            print(json.dumps(receipt, indent=2))
        else:
            print(format_markdown_summary(receipt), end="")
        return 0

    if args.subcommand == "check-gate":
        ok, errs = check_gate(
            args.scope,
            get_receipt_path(),
            require_pipeline=args.require_pipeline or None,
        )
        if args.json:
            print(json.dumps({"ok": ok, "errors": errs}, indent=2))
        else:
            if ok:
                print(f"[REVIEW-GATE PASS] ({args.scope})")
            else:
                print(f"[REVIEW-GATE BLOCKED] ({args.scope})", file=sys.stderr)
                for err in errs:
                    print(f"  - {err}", file=sys.stderr)
        return 0 if ok else 1

    if args.subcommand == "summary":
        r_path = get_receipt_path()
        if not r_path.exists():
            print(f"No review receipt found at {r_path}", file=sys.stderr)
            return 1
        receipt = json.loads(r_path.read_text(encoding="utf-8"))
        if args.json:
            print(json.dumps(receipt, indent=2))
        else:
            print(format_markdown_summary(receipt), end="")
        return 0

    if args.subcommand == "eval":
        res = run_eval_suite()
        if args.json:
            print(json.dumps(res, indent=2))
        else:
            status = "PASS" if res["passed"] else "FAIL"
            print(
                f"[REVIEW-EVAL {status}] recall={res['recall']:.2f} "
                f"false_positive_rate={res['false_positive_rate']:.2f}"
            )
        return 0 if res["passed"] else 1

    if args.subcommand == "circuit-breaker":
        res = update_circuit_breaker(
            record_incident=args.record_incident,
            reset=args.reset,
        )
        if args.json:
            print(json.dumps(res, indent=2))
        else:
            print(
                f"Circuit Breaker: {res['state']} "
                f"(recent_incidents={res['recent_incidents']}/{res['max_recent_incidents']})"
            )
        return 0

    return 1


if __name__ == "__main__":
    sys.exit(main())
