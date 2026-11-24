#!/usr/bin/env python3

from __future__ import annotations

import argparse
import os
import re
import sys
from collections.abc import Mapping

BLOCKED_BINARIES = frozenset(
    {
        "kubectl",
        "k3s",
        "oc",
        "k9s",
        "flux",
        "helm",
        "kubectx",
        "kubens",
        "stern",
        "crictl",
        "cilium",
        "hubble",
        "argocd",
        "velero",
        "ssh",
        "scp",
        "sftp",
        "gcloud",
        "gke-gcloud-auth-plugin",
        "gsutil",
        "bq",
        "cloud-sql-proxy",
        "oci",
        "aws",
        "eksctl",
        "az",
        "doctl",
        "linode-cli",
        "ansible",
        "ansible-playbook",
        "kcat",
        "kafkacat",
        "wg",
        "wg-quick",
        "tailscale",
        "sudo",
        "su",
        "doas",
        "pkexec",
        "runuser",
        "run0",
        "dzdo",
        "pbrun",
        "pfexec",
    }
)

TERRAFORM_MUTATING_SUBCOMMANDS = frozenset(
    {
        "apply",
        "destroy",
        "import",
        "taint",
        "untaint",
        "force-unlock",
    }
)

TERRAFORM_STATE_MUTATING = frozenset(
    {
        "rm",
        "mv",
        "push",
        "replace-provider",
    }
)

TERRAFORM_VALUE_FLAGS = frozenset(
    {
        "-chdir",
        "-var",
        "-var-file",
        "-state",
        "-state-out",
        "-backup",
    }
)

SAFE_READONLY_PATH_TOOLS = frozenset(
    {
        "git",
        "gh",
        "grep",
        "egrep",
        "fgrep",
        "rg",
        "ag",
        "ack",
    }
)

SHELL_INTERPRETERS = frozenset({"bash", "sh", "dash", "zsh", "ksh", "ash"})
SHELL_VALUE_FLAGS = frozenset({"-o", "+o", "-O", "+O", "--rcfile", "--init-file"})
SHELL_PREFIX_TOKENS = frozenset({"{", "}", "!", "coproc", "then", "do", "else", "elif", "if", "while", "until"})
COPROC_BODY_STARTERS = frozenset({"{", "(", "if", "while", "until", "for", "case", "select"})

PARALLEL_VALUE_FLAGS = frozenset(
    {
        "-j",
        "--jobs",
        "-P",
        "--max-procs",
        "-I",
        "-a",
        "--arg-file",
        "-d",
        "--delimiter",
        "-E",
        "-n",
        "-N",
        "--max-args",
        "-C",
        "--colsep",
        "-S",
        "--sshlogin",
        "--slf",
        "--sshloginfile",
        "--wd",
        "--workdir",
        "--id",
        "--semaphorename",
        "--timeout",
        "--retries",
        "--delay",
        "--joblog",
        "--results",
        "--res",
        "--tagstring",
        "--env",
        "--tmpdir",
        "--tempdir",
        "--nice",
        "--halt",
        "--arg-sep",
        "--arg-file-sep",
        "--block",
        "--block-size",
    }
)

WRAPPER_VALUE_FLAGS: dict[str, frozenset[str]] = {
    "env": frozenset({"-u", "--unset", "-C", "--chdir", "-a", "--argv0"}),
    "timeout": frozenset({"-k", "--kill-after", "-s", "--signal"}),
    "nice": frozenset({"-n", "--adjustment"}),
    "ionice": frozenset({"-c", "--class", "-n", "--classdata", "-p", "-P", "-u"}),
    "stdbuf": frozenset({"-i", "--input", "-o", "--output", "-e", "--error"}),
    "xargs": frozenset(
        {
            "-I",
            "-L",
            "-n",
            "-P",
            "-s",
            "-a",
            "-d",
            "-E",
            "-J",
            "--arg-file",
            "--delimiter",
            "--max-args",
            "--max-procs",
            "--max-chars",
            "--process-slot-var",
        }
    ),
    "strace": frozenset(
        {
            "-a",
            "--columns",
            "-b",
            "--detach-on",
            "-e",
            "--trace",
            "-E",
            "--env",
            "-I",
            "--interruptible",
            "-o",
            "--output",
            "-O",
            "--overhead",
            "-p",
            "--attach",
            "-P",
            "--trace-path",
            "-s",
            "--string-limit",
            "-S",
            "--sort-by",
            "-u",
            "--user",
            "-U",
            "--summary-columns",
            "-X",
            "--const-print-style",
            "--argv0",
            "--signal",
            "--status",
            "--syscall-limit",
        }
    ),
    "exec": frozenset({"-a"}),
    "watch": frozenset({"-n", "--interval", "-q", "--equexit"}),
    "flock": frozenset({"-w", "--timeout", "-E", "--conflict-exit-code"}),
    "chrt": frozenset({"-T", "--sched-runtime", "-P", "--sched-period", "-D", "--sched-deadline"}),
    "taskset": frozenset(),
    "parallel": PARALLEL_VALUE_FLAGS,
    "sem": PARALLEL_VALUE_FLAGS,
    "setpriv": frozenset(
        {
            "--ambient-caps",
            "--inh-caps",
            "--bounding-set",
            "--ruid",
            "--euid",
            "--rgid",
            "--egid",
            "--reuid",
            "--regid",
            "--groups",
            "--securebits",
            "--pdeathsig",
            "--ptracer",
            "--selinux-label",
            "--apparmor-profile",
            "--landlock-access",
            "--landlock-rule",
            "--seccomp-filter",
        }
    ),
    "nsenter": frozenset({"-t", "--target", "-N", "--net-socket", "-W", "--wdns"}),
    "unshare": frozenset(
        {
            "-l",
            "--load-interp",
            "--propagation",
            "-R",
            "--root",
            "-w",
            "--wd",
            "-S",
            "--setuid",
            "-G",
            "--setgid",
            "--map-user",
            "--map-group",
            "--map-users",
            "--map-groups",
            "--setgroups",
            "--monotonic",
            "--boottime",
        }
    ),
    "prlimit": frozenset({"-p", "--pid", "-o", "--output"}),
    "chroot": frozenset({"--userspec", "--groups"}),
    "setarch": frozenset(),
}

GREP_LONG_VALUE_FLAGS = frozenset(
    {
        "--max-count",
        "--include",
        "--exclude",
        "--exclude-dir",
        "--context",
        "--after-context",
        "--before-context",
        "--devices",
        "--directories",
        "--label",
        "--binary-files",
    }
)
GREP_SHORT_VALUE_FLAGS = ("m", "A", "B", "C", "d", "D")

RG_LONG_VALUE_FLAGS = frozenset(
    {
        "--glob",
        "--iglob",
        "--type",
        "--type-not",
        "--max-count",
        "--include",
        "--exclude",
        "--exclude-dir",
        "--context",
        "--after-context",
        "--before-context",
        "--threads",
        "--max-depth",
        "--encoding",
    }
)
RG_SHORT_VALUE_FLAGS = ("m", "A", "B", "C", "g", "t", "T", "j", "M", "E")

AG_LONG_VALUE_FLAGS = frozenset(
    {
        "--after",
        "--before",
        "--context",
        "--file-search-regex",
        "--max-count",
        "--path-to-ignore",
        "--ignore",
        "--ignore-dir",
        "--depth",
        "--workers",
    }
)
AG_SHORT_VALUE_FLAGS = ("A", "B", "C", "G", "m", "p")

SEARCH_SECRET_FILE_FLAGS = frozenset(
    {
        "--file",
        "--exclude-from",
        "--ignore-file",
        "--ignore-file-case-insensitive",
    }
)

PLAIN_WRAPPERS = frozenset(
    {
        "command",
        "nohup",
        "time",
        "setsid",
        "builtin",
        "unbuffer",
        "linux32",
        "linux64",
        "eatmydata",
        "chronic",
        "ifne",
    }
)

PARALLEL_SEPARATORS = frozenset({":::", "::::", ":::+", "::::+"})
CHRT_PRIO_RE = re.compile(r"^[+-]?[0-9]+$")
TASKSET_MASK_RE = re.compile(
    r"^(?:(?:0[xX])?[0-9a-fA-F]+|[0-9]+(?:-[0-9]+(?::[0-9]+)?)?(?:,[0-9]+(?:-[0-9]+(?::[0-9]+)?)?)*)$"
)
FIND_EXEC_FLAGS = frozenset({"-exec", "-execdir", "-ok", "-okdir"})
FIND_ONE_ARG_PREDICATES = frozenset(
    {
        "-name",
        "-iname",
        "-path",
        "-ipath",
        "-wholename",
        "-iwholename",
        "-regex",
        "-iregex",
        "-lname",
        "-ilname",
        "-printf",
    }
)
SCRIPT_VALUE_FLAGS = frozenset(
    {
        "-I",
        "--log-in",
        "-O",
        "--log-out",
        "-B",
        "--log-io",
        "-T",
        "--log-timing",
        "-m",
        "--logging-format",
        "-E",
        "--echo",
        "-o",
        "--output-limit",
    }
)
SCRIPT_SHORT_VALUE_CHARS = frozenset({"I", "O", "B", "T", "m", "E", "o"})

ENV_ASSIGN_RE = re.compile(r"^[A-Za-z_][A-Za-z0-9_]*=.*$", re.DOTALL)

ANSI_C_SIMPLE = {
    "\\": "\\",
    "'": "'",
    '"': '"',
    "n": "\n",
    "r": "\r",
    "t": "\t",
    "a": "\a",
    "b": "\b",
    "e": "\x1b",
    "E": "\x1b",
    "f": "\f",
    "v": "\v",
    "?": "?",
}


def _has_mcp_path(text: str) -> bool:
    if "/etc/nacfson-mcp" in text:
        return True
    if "nacfson-mcp" not in text:
        return False
    for m in re.finditer(r"/[^\s'\"`<>|;&:=,]+", text):
        cleaned = re.sub(r"^/+", "/", m.group(0))
        norm = os.path.normpath(cleaned)
        if norm == "/etc/nacfson-mcp" or norm.startswith("/etc/nacfson-mcp/"):
            return True
    if "=" in text:
        _, rhs = text.split("=", 1)
        if rhs.startswith("/"):
            cleaned = re.sub(r"^/+", "/", rhs)
            norm = os.path.normpath(cleaned)
            if norm == "/etc/nacfson-mcp" or norm.startswith("/etc/nacfson-mcp/"):
                return True
    return False


def _decode_ansi_c_single(text: str, start: int) -> tuple[str, int]:
    out: list[str] = []
    i = start
    n = len(text)
    while i < n:
        ch = text[i]
        if ch == "'":
            return "".join(out), i + 1
        if ch != "\\":
            out.append(ch)
            i += 1
            continue
        i += 1
        if i >= n:
            break
        esc = text[i]
        if esc in ANSI_C_SIMPLE:
            out.append(ANSI_C_SIMPLE[esc])
            i += 1
        elif esc == "x":
            j = i + 1
            while j < n and j < i + 3 and text[j] in "0123456789abcdefABCDEF":
                j += 1
            if j > i + 1:
                out.append(chr(int(text[i + 1 : j], 16)))
                i = j
            else:
                out.append("\\x")
                i += 1
        elif esc in ("u", "U"):
            limit = 5 if esc == "u" else 9
            j = i + 1
            while j < n and j < i + limit and text[j] in "0123456789abcdefABCDEF":
                j += 1
            if j > i + 1:
                cp = int(text[i + 1 : j], 16)
                out.append(chr(cp) if 0 <= cp <= 0x10FFFF else "")
                i = j
            else:
                out.append("\\" + esc)
                i += 1
        elif esc in "01234567":
            j = i + 1
            while j < n and j < i + 3 and text[j] in "01234567":
                j += 1
            out.append(chr(int(text[i:j], 8) & 0xFF))
            i = j
        elif esc == "c" and i + 1 < n:
            out.append(chr(ord(text[i + 1]) & 0x1F))
            i += 2
        else:
            out.append("\\" + esc)
            i += 1
    return "".join(out), i


def _scan_ansi_c_end(text: str, start: int) -> int:
    i = start
    n = len(text)
    while i < n:
        ch = text[i]
        if ch == "\\":
            i += 2
            continue
        if ch == "'":
            return i + 1
        i += 1
    return n


def _is_comment_start(text: str, pos: int) -> bool:
    return pos == 0 or text[pos - 1] in (" ", "\t", "\n", "\r", ";", "&", "|", "(", ")")


def _parse_heredoc_spec(text: str, pos: int) -> tuple[str, bool, bool, int] | None:
    strip_tabs = text.startswith("<<-", pos)
    j = pos + (3 if strip_tabs else 2)
    n = len(text)
    while j < n and text[j] in (" ", "\t"):
        j += 1
    out: list[str] = []
    quoted = False
    in_single = False
    in_double = False
    escaped = False
    stop_chars = (" ", "\t", "\n", "\r", ";", "&", "|", "(", ")", "<", ">")
    while j < n:
        ch = text[j]
        if escaped:
            out.append(ch)
            escaped = False
            j += 1
            continue
        if in_single:
            if ch == "'":
                in_single = False
            else:
                out.append(ch)
            j += 1
            continue
        if ch == "\\":
            quoted = True
            if in_double and j + 1 < n and text[j + 1] not in ('"', "\\", "$", "`", "\n"):
                out.append(ch)
            escaped = True
            j += 1
            continue
        if ch == "$" and not in_double and j + 1 < n and text[j + 1] == "'":
            quoted = True
            decoded, next_j = _decode_ansi_c_single(text, j + 2)
            out.append(decoded)
            j = next_j
            continue
        if ch == '"':
            quoted = True
            in_double = not in_double
            j += 1
            continue
        if ch == "'" and not in_double:
            quoted = True
            in_single = True
            j += 1
            continue
        if not in_double and ch in stop_chars:
            break
        out.append(ch)
        j += 1
    delimiter = "".join(out)
    if not delimiter:
        return None
    return delimiter, strip_tabs, quoted, j


def _consume_heredoc_body(text: str, pos: int, delimiter: str, strip_tabs: bool) -> tuple[str, int]:
    cur = pos
    n = len(text)
    while cur < n:
        nl = text.find("\n", cur)
        line = text[cur : n if nl == -1 else nl]
        cmp = line.rstrip("\r")
        if strip_tabs:
            cmp = cmp.lstrip("\t")
        if cmp == delimiter:
            next_pos = n if nl == -1 else nl + 1
            return text[pos:cur], next_pos
        if nl == -1:
            break
        cur = nl + 1
    return text[pos:n], n


def _extract_unquoted_heredoc_subshells(body: str) -> list[str]:
    out: list[str] = []
    i = 0
    n = len(body)
    while i < n:
        ch = body[i]
        if ch == "\\":
            i += 2
            continue
        if ch == "$" and i + 1 < n and body[i + 1] == "(":
            if i + 2 < n and body[i + 2] == "(":
                i += 1
                continue
            start = i + 2
            end = _find_matching_paren(body, start)
            out.append(body[start:end])
            i = end + 1 if end < n else n
            continue
        if ch == "`":
            start = i + 1
            end = _find_matching_backtick(body, start)
            out.append(body[start:end])
            i = end + 1 if end < n else n
            continue
        i += 1
    return out


def _extract_subshells(text: str) -> list[str]:
    out: list[str] = []
    i = 0
    n = len(text)
    in_single = False
    in_double = False
    escaped = False
    pending_heredocs: list[tuple[str, bool, bool]] = []

    while i < n:
        ch = text[i]
        if escaped:
            escaped = False
            i += 1
            continue
        if in_single:
            if ch == "'":
                in_single = False
            i += 1
            continue
        if ch == "\\":
            escaped = True
            i += 1
            continue
        if ch == '"':
            in_double = not in_double
            i += 1
            continue
        if ch == "'" and not in_double:
            in_single = True
            i += 1
            continue
        if ch == "$" and not in_double and i + 1 < n and text[i + 1] == "'":
            i = _scan_ansi_c_end(text, i + 2)
            continue
        if not in_double:
            if ch == "#" and _is_comment_start(text, i):
                nl = text.find("\n", i)
                i = n if nl == -1 else nl
                continue
            if ch == "<" and text.startswith("<<", i) and not text.startswith("<<<", i):
                spec = _parse_heredoc_spec(text, i)
                if spec is not None:
                    delim, strip_tabs, quoted, next_i = spec
                    pending_heredocs.append((delim, strip_tabs, quoted))
                    i = next_i
                    continue
            if ch == "\n" and pending_heredocs:
                i += 1
                for delim, strip_tabs, quoted in pending_heredocs:
                    body, i = _consume_heredoc_body(text, i, delim, strip_tabs)
                    if not quoted:
                        out.extend(_extract_unquoted_heredoc_subshells(body))
                pending_heredocs.clear()
                continue
        if (ch == "$" or (ch in ("<", ">") and not in_double)) and i + 1 < n and text[i + 1] == "(":
            if ch == "$" and i + 2 < n and text[i + 2] == "(":
                i += 1
                continue
            start = i + 2
            end = _find_matching_paren(text, start)
            out.append(text[start:end])
            i = end + 1 if end < n else n
            continue
        if ch == "`":
            start = i + 1
            end = _find_matching_backtick(text, start)
            out.append(text[start:end])
            i = end + 1 if end < n else n
            continue
        i += 1
    return out


def _find_matching_paren(text: str, start: int) -> int:
    depth = 1
    i = start
    n = len(text)
    in_single = False
    in_double = False
    escaped = False
    pending_heredocs: list[tuple[str, bool]] = []
    while i < n:
        ch = text[i]
        if escaped:
            escaped = False
            i += 1
            continue
        if in_single:
            if ch == "'":
                in_single = False
            i += 1
            continue
        if ch == "\\":
            escaped = True
            i += 1
            continue
        if ch == '"':
            in_double = not in_double
            i += 1
            continue
        if in_double:
            if ch == "$" and i + 1 < n and text[i + 1] == "(":
                end = _find_matching_paren(text, i + 2)
                i = end + 1 if end < n else n
                continue
            if ch == "`":
                end = _find_matching_backtick(text, i + 1)
                i = end + 1 if end < n else n
                continue
            i += 1
            continue
        if ch == "'":
            in_single = True
            i += 1
            continue
        if ch == "$" and i + 1 < n and text[i + 1] == "'":
            i = _scan_ansi_c_end(text, i + 2)
            continue
        if ch == "#" and _is_comment_start(text, i):
            nl = text.find("\n", i)
            i = n if nl == -1 else nl
            continue
        if ch == "<" and text.startswith("<<", i) and not text.startswith("<<<", i):
            spec = _parse_heredoc_spec(text, i)
            if spec is not None:
                delim, strip_tabs, _, next_i = spec
                pending_heredocs.append((delim, strip_tabs))
                i = next_i
                continue
        if ch == "\n" and pending_heredocs:
            i += 1
            for delim, strip_tabs in pending_heredocs:
                _, i = _consume_heredoc_body(text, i, delim, strip_tabs)
            pending_heredocs.clear()
            continue
        if ch == "(":
            depth += 1
        elif ch == ")":
            depth -= 1
            if depth == 0:
                return i
        i += 1
    return n


def _find_matching_backtick(text: str, start: int) -> int:
    i = start
    n = len(text)
    escaped = False
    while i < n:
        ch = text[i]
        if escaped:
            escaped = False
            i += 1
            continue
        if ch == "\\":
            escaped = True
            i += 1
            continue
        if ch == "`":
            return i
        i += 1
    return n


def _split_segments(text: str) -> list[tuple[str, list[tuple[str, bool]], bool]]:
    segments: list[tuple[str, list[tuple[str, bool]], bool]] = []
    buf: list[str] = []
    cur_heredocs: list[tuple[str, bool]] = []
    pending_heredocs: list[tuple[str, bool, bool, list[tuple[str, bool]]]] = []
    i = 0
    n = len(text)
    in_single = False
    in_double = False
    escaped = False
    in_backtick = False

    def flush_segment(is_pipe: bool) -> None:
        nonlocal cur_heredocs
        seg = "".join(buf).strip()
        if seg or cur_heredocs:
            segments.append((seg, cur_heredocs, is_pipe))
            cur_heredocs = []
        buf.clear()

    while i < n:
        ch = text[i]
        if escaped:
            buf.append(ch)
            escaped = False
            i += 1
            continue
        if in_single:
            buf.append(ch)
            if ch == "'":
                in_single = False
            i += 1
            continue
        if ch == "\\":
            buf.append(ch)
            escaped = True
            i += 1
            continue
        if ch == '"':
            buf.append(ch)
            in_double = not in_double
            i += 1
            continue
        if ch == "'" and not in_double and not in_backtick:
            buf.append(ch)
            in_single = True
            i += 1
            continue
        if ch == "$" and not in_double and not in_backtick and i + 1 < n and text[i + 1] == "'":
            end_i = _scan_ansi_c_end(text, i + 2)
            buf.append(text[i:end_i])
            i = end_i
            continue
        if ch == "`" and not in_single:
            buf.append(ch)
            in_backtick = not in_backtick
            i += 1
            continue
        if (ch == "$" or (ch in ("<", ">") and not in_double)) and not in_backtick and i + 1 < n and text[i + 1] == "(":
            end = _find_matching_paren(text, i + 2)
            end_inc = end + 1 if end < n else n
            buf.append(text[i:end_inc])
            i = end_inc
            continue
        if not in_double and not in_backtick:
            if ch == "#" and _is_comment_start(text, i):
                nl = text.find("\n", i)
                i = n if nl == -1 else nl
                continue
            if ch == "<" and text.startswith("<<", i) and not text.startswith("<<<", i):
                spec = _parse_heredoc_spec(text, i)
                if spec is not None:
                    delim, strip_tabs, quoted, next_i = spec
                    buf.append(text[i:next_i])
                    pending_heredocs.append((delim, strip_tabs, quoted, cur_heredocs))
                    i = next_i
                    continue
            if ch == "\n":
                flush_segment(False)
                i += 1
                if pending_heredocs:
                    for delim, strip_tabs, quoted, target_list in pending_heredocs:
                        body, i = _consume_heredoc_body(text, i, delim, strip_tabs)
                        target_list.append((body, quoted))
                    pending_heredocs.clear()
                continue
            if ch in (";", "(", ")"):
                flush_segment(False)
                i += 1
                continue
            if ch == "&":
                if i > 0 and text[i - 1] in (">", "<"):
                    buf.append(ch)
                    i += 1
                    continue
                if i + 1 < n and text[i + 1] == ">":
                    buf.append("&>")
                    i += 2
                    continue
            if ch == "|" and i > 0 and text[i - 1] == ">":
                buf.append(ch)
                i += 1
                continue
            if ch in ("&", "|"):
                is_pipe = ch == "|" and not (i + 1 < n and text[i + 1] == "|")
                if i + 1 < n and text[i + 1] in (ch, "&"):
                    i += 2
                else:
                    i += 1
                flush_segment(is_pipe)
                continue
        buf.append(ch)
        i += 1

    flush_segment(False)
    return segments


def _tokenize_segment(segment: str) -> tuple[list[str], list[str], list[str]]:
    words: list[str] = []
    redirs: list[str] = []
    herestrings: list[str] = []
    cur: list[str] = []
    in_word = False
    pending_redir = False
    pending_herestring = False
    in_single = False
    in_double = False
    escaped = False
    i = 0
    n = len(segment)

    def flush_word() -> None:
        nonlocal in_word, pending_redir, pending_herestring
        if not in_word:
            return
        tok = "".join(cur)
        cur.clear()
        in_word = False
        if pending_herestring:
            herestrings.append(tok)
            pending_herestring = False
        elif pending_redir:
            redirs.append(tok)
            pending_redir = False
        else:
            words.append(tok)

    while i < n:
        ch = segment[i]
        if escaped:
            cur.append(ch)
            escaped = False
            in_word = True
            i += 1
            continue
        if in_single:
            if ch == "'":
                in_single = False
            else:
                cur.append(ch)
            in_word = True
            i += 1
            continue
        if ch == "\\":
            if in_double and i + 1 < n and segment[i + 1] not in ('"', "\\", "$", "`", "\n"):
                cur.append(ch)
            escaped = True
            in_word = True
            i += 1
            continue
        if ch == "$" and not in_double and i + 1 < n:
            if segment[i + 1] == '"':
                in_double = True
                in_word = True
                i += 2
                continue
            if segment[i + 1] == "'":
                decoded, next_i = _decode_ansi_c_single(segment, i + 2)
                cur.append(decoded)
                in_word = True
                i = next_i
                continue
        if ch == '"':
            in_double = not in_double
            in_word = True
            i += 1
            continue
        if ch == "'" and not in_double:
            in_single = True
            in_word = True
            i += 1
            continue
        if not in_double:
            if ch.isspace():
                flush_word()
                i += 1
                continue
            if ch == "#" and not in_word:
                break
            if ch == "{" and not in_word:
                close = segment.find("}", i + 1)
                if close > i + 1 and close + 1 < n and segment[close + 1] in (">", "<"):
                    if re.match(r"^[A-Za-z_][A-Za-z0-9_]*(\[[^\]]+\])?$", segment[i + 1 : close]):
                        i = close + 1
                        continue
            if ch == "&" and i + 1 < n and segment[i + 1] == ">":
                flush_word()
                pending_redir = True
                i += 3 if (i + 2 < n and segment[i + 2] == ">") else 2
                continue
            if segment.startswith("<<<", i):
                flush_word()
                pending_herestring = True
                i += 3
                continue
            if segment.startswith("<<-", i):
                flush_word()
                pending_redir = True
                i += 3
                continue
            if ch in (">", "<") and not (i + 1 < n and segment[i + 1] == "("):
                flush_word()
                pending_redir = True
                if i + 1 < n and segment[i + 1] in (">", "<", "&", "|"):
                    i += 2
                else:
                    i += 1
                continue
            if ch.isdigit() and not in_word:
                k = i + 1
                while k < n and segment[k].isdigit():
                    k += 1
                if k < n and segment[k] in (">", "<"):
                    i = k
                    continue
        cur.append(ch)
        in_word = True
        i += 1

    flush_word()
    return words, redirs, herestrings


def _deny(reason: str) -> tuple[bool, str]:
    return (
        False,
        f"[CLUSTER-CLI GUARD] {reason} Use the read-only `nacfson-debug` MCP server tools first (`cluster_overview`, `list_workloads`, `describe_pod`, `get_events`, `get_flux_status`, `get_pod_logs`). If the daemon is stopped, ask the operator to start a temporary session via `sudo ./scripts/mcp/mcp-session.sh start <gke|local-k3s> --ttl 1h` (`.agents/skills/debug-mcp-session/SKILL.md`). Do not fall back to direct cluster/cloud CLIs or attempt to bypass this guard.",
    )


def _extract_flock_c(
    tokens: list[str],
    idx: int,
    allow_bundles: bool,
    short_val_chars: set[str],
) -> tuple[str, list[str], int] | None:
    arg = tokens[idx]
    if arg in ("-c", "--command"):
        cmd_str = tokens[idx + 1] if idx + 1 < len(tokens) else ""
        next_idx = idx + 2 if idx + 1 < len(tokens) else idx + 1
        return cmd_str, [arg], next_idx
    if arg.startswith("--command="):
        return arg.split("=", 1)[1], [], idx + 1
    if allow_bundles and arg.startswith("-") and not arg.startswith("--") and len(arg) > 1:
        for k, ch in enumerate(arg[1:]):
            if ch in short_val_chars:
                return None
            if ch == "c":
                rest = arg[k + 2 :]
                if rest:
                    return rest, [], idx + 1
                cmd_str = tokens[idx + 1] if idx + 1 < len(tokens) else ""
                next_idx = idx + 2 if idx + 1 < len(tokens) else idx + 1
                return cmd_str, [arg], next_idx
    if not allow_bundles and arg.startswith("-c") and len(arg) > 2:
        return arg[2:], [], idx + 1
    return None


def _unwrap_command(tokens: list[str]) -> tuple[list[str], list[str], list[str], bool]:
    tokens = list(tokens)
    idx = 0
    inline_scripts: list[str] = []
    stripped_tokens: list[str] = []
    splice_count = 0
    was_wrapped = False

    while idx < len(tokens):
        tok = tokens[idx]
        if tok == "coproc":
            stripped_tokens.append(tok)
            idx += 1
            if idx + 1 < len(tokens) and tokens[idx + 1] in COPROC_BODY_STARTERS:
                stripped_tokens.append(tokens[idx])
                idx += 1
            continue
        if tok in SHELL_PREFIX_TOKENS:
            stripped_tokens.append(tok)
            idx += 1
            continue
        if ENV_ASSIGN_RE.match(tok):
            stripped_tokens.append(tok)
            idx += 1
            continue

        base = os.path.basename(tok)
        if base in PLAIN_WRAPPERS:
            was_wrapped = True
            stripped_tokens.append(tok)
            idx += 1
            while idx < len(tokens) and tokens[idx].startswith("-") and tokens[idx] != "--":
                stripped_tokens.append(tokens[idx])
                idx += 1
            if idx < len(tokens) and tokens[idx] == "--":
                stripped_tokens.append(tokens[idx])
                idx += 1
            continue

        if base in WRAPPER_VALUE_FLAGS:
            was_wrapped = True
            stripped_tokens.append(tok)
            val_flags = WRAPPER_VALUE_FLAGS[base]
            short_val_chars = {f[1] for f in val_flags if len(f) == 2 and f.startswith("-")}
            watch_exec = False
            flock_saw_c = False
            flock_end_opts = False
            saw_dashdash = False
            idx += 1
            while idx < len(tokens):
                arg = tokens[idx]
                if arg == "--":
                    saw_dashdash = True
                    if base == "flock":
                        flock_end_opts = True
                    stripped_tokens.append(arg)
                    idx += 1
                    break
                if base == "env":
                    if arg == "-":
                        stripped_tokens.append(arg)
                        idx += 1
                        continue
                    if ENV_ASSIGN_RE.match(arg):
                        stripped_tokens.append(arg)
                        idx += 1
                        continue
                    split_val: str | None = None
                    next_idx = idx
                    if arg in ("-S", "--split-string"):
                        if idx + 1 < len(tokens):
                            split_val = tokens[idx + 1]
                            next_idx = idx + 2
                        else:
                            next_idx = idx + 1
                    elif arg.startswith("--split-string="):
                        split_val = arg.split("=", 1)[1]
                        next_idx = idx + 1
                    elif arg.startswith("-") and not arg.startswith("--") and "S" in arg[1:]:
                        pos = arg.find("S", 1)
                        if not any(ch in short_val_chars for ch in arg[1:pos]):
                            if pos == len(arg) - 1:
                                if idx + 1 < len(tokens):
                                    split_val = tokens[idx + 1]
                                    next_idx = idx + 2
                                else:
                                    next_idx = idx + 1
                            else:
                                split_val = arg[pos + 1 :].lstrip("=")
                                next_idx = idx + 1
                    if split_val is not None:
                        inline_scripts.append(split_val)
                        if splice_count < 10:
                            splice_count += 1
                            split_words, _, _ = _tokenize_segment(split_val)
                            tokens = tokens[:idx] + split_words + tokens[next_idx:]
                            continue
                        idx = next_idx
                        continue
                if base == "flock":
                    flock_c = _extract_flock_c(tokens, idx, True, short_val_chars)
                    if flock_c is not None:
                        cmd_str, skipped, idx = flock_c
                        flock_saw_c = True
                        inline_scripts.append(cmd_str)
                        stripped_tokens.extend(skipped)
                        continue
                if base == "watch" and arg == "--exec":
                    watch_exec = True
                if arg in val_flags:
                    stripped_tokens.extend(tokens[idx : idx + 2])
                    idx += 2
                    continue
                if any(arg.startswith(f"{vf}=") for vf in val_flags if vf.startswith("--")):
                    stripped_tokens.append(arg)
                    idx += 1
                    continue
                if arg.startswith("--"):
                    stripped_tokens.append(arg)
                    idx += 1
                    continue
                if arg.startswith("-") and len(arg) > 1:
                    consumed_next = False
                    for k, ch in enumerate(arg[1:]):
                        if ch in short_val_chars:
                            if k + 2 == len(arg):
                                consumed_next = True
                            break
                        if base == "watch" and ch == "x":
                            watch_exec = True
                    step = 2 if consumed_next else 1
                    stripped_tokens.extend(tokens[idx : idx + step])
                    idx += step
                    continue
                if base == "timeout" and re.match(
                    r"^(?:inf|infinity|[+-]?(?:[0-9]+(?:\.[0-9]*)?|\.[0-9]+)(?:[eE][+-]?[0-9]+)?[smhd]?)$",
                    arg,
                    re.IGNORECASE,
                ):
                    stripped_tokens.append(arg)
                    idx += 1
                    continue
                break

            if base == "watch":
                if watch_exec:
                    continue
                if idx < len(tokens):
                    inline_scripts.append(" ".join(tokens[idx:]))
                    idx = len(tokens)
                break
            if base == "flock":
                if idx < len(tokens):
                    stripped_tokens.append(tokens[idx])
                    idx += 1
                if not flock_saw_c and not flock_end_opts and idx < len(tokens):
                    flock_c = _extract_flock_c(tokens, idx, False, short_val_chars)
                    if flock_c is not None:
                        cmd_str, skipped, idx = flock_c
                        inline_scripts.append(cmd_str)
                        stripped_tokens.extend(skipped)
                continue
            if base == "chrt":
                if idx < len(tokens) and CHRT_PRIO_RE.match(tokens[idx]):
                    stripped_tokens.append(tokens[idx])
                    idx += 1
                continue
            if base == "taskset":
                if idx < len(tokens) and TASKSET_MASK_RE.match(tokens[idx]):
                    stripped_tokens.append(tokens[idx])
                    idx += 1
                continue
            if base in ("chroot", "setarch"):
                if idx < len(tokens):
                    stripped_tokens.append(tokens[idx])
                    idx += 1
                continue
            if base in ("parallel", "sem"):
                rest = tokens[idx:]
                sep_idx = next((p for p, t in enumerate(rest) if t in PARALLEL_SEPARATORS), None)
                if sep_idx is not None:
                    cmd_part = rest[:sep_idx]
                    tail = rest[sep_idx:]
                    input_part = [t for t in tail if t not in PARALLEL_SEPARATORS]
                    stripped_tokens.extend(tail)
                else:
                    cmd_part = rest
                    input_part = []
                if not cmd_part:
                    inline_scripts.extend(input_part)
                    idx = len(tokens)
                    break
                inline_scripts.append(" ".join(cmd_part))
                first_base = os.path.basename(cmd_part[0])
                if input_part and (
                    "{" in cmd_part[0]
                    or first_base in SHELL_INTERPRETERS
                    or first_base == "eval"
                    or first_base in PLAIN_WRAPPERS
                    or first_base in WRAPPER_VALUE_FLAGS
                ):
                    inline_scripts.extend(input_part)
                if (
                    saw_dashdash
                    and not input_part
                    and len(cmd_part) > 1
                    and first_base not in SAFE_READONLY_PATH_TOOLS
                ):
                    inline_scripts.extend(t for t in cmd_part if t != "--")
                tokens = tokens[:idx] + cmd_part
                continue
            continue
        break
    return tokens[idx:], inline_scripts, stripped_tokens, was_wrapped


def _check_mcp_path_search(args: list[str], cmd: str = "") -> tuple[bool, str]:
    pos: list[str] = []
    has_explicit_pattern = False
    files_mode = False
    dashdash = False
    if cmd in ("grep", "egrep", "fgrep", "git-grep"):
        long_val_flags = GREP_LONG_VALUE_FLAGS
        short_val_flags = GREP_SHORT_VALUE_FLAGS
    elif cmd in ("ag", "ack"):
        long_val_flags = AG_LONG_VALUE_FLAGS
        short_val_flags = AG_SHORT_VALUE_FLAGS
    else:
        long_val_flags = RG_LONG_VALUE_FLAGS
        short_val_flags = RG_SHORT_VALUE_FLAGS
    i = 0
    while i < len(args):
        arg = args[i]
        if dashdash:
            pos.append(arg)
            i += 1
            continue
        if arg == "--":
            dashdash = True
            i += 1
            continue
        if arg.startswith("--"):
            if arg in SEARCH_SECRET_FILE_FLAGS:
                if arg == "--file":
                    has_explicit_pattern = True
                if i + 1 < len(args) and _has_mcp_path(args[i + 1]):
                    return _deny("Reading /etc/nacfson-mcp via pattern file is blocked.")
                i += 2
                continue
            if any(arg.startswith(f"{flg}=") for flg in SEARCH_SECRET_FILE_FLAGS):
                if arg.startswith("--file="):
                    has_explicit_pattern = True
                val = arg.split("=", 1)[1]
                if _has_mcp_path(val):
                    return _deny("Reading /etc/nacfson-mcp via pattern file is blocked.")
                i += 1
                continue
            if arg == "--regexp":
                has_explicit_pattern = True
                i += 2
                continue
            if arg.startswith("--regexp="):
                has_explicit_pattern = True
                i += 1
                continue
            if arg == "--files":
                files_mode = True
                i += 1
                continue
            if cmd == "rg" and arg == "--color" and i + 1 < len(args) and args[i + 1] in ("always", "ansi", "auto", "never"):
                i += 2
                continue
            if arg in long_val_flags:
                i += 2
                continue
            i += 1
            continue
        if arg.startswith("-") and len(arg) > 1:
            consumed_next = False
            for k, ch in enumerate(arg[1:]):
                if ch == "f":
                    has_explicit_pattern = True
                    rest = arg[k + 2 :]
                    if rest:
                        if _has_mcp_path(rest):
                            return _deny("Reading /etc/nacfson-mcp via pattern file is blocked.")
                        break
                    if i + 1 < len(args) and _has_mcp_path(args[i + 1]):
                        return _deny("Reading /etc/nacfson-mcp via pattern file is blocked.")
                    consumed_next = True
                    break
                if ch == "e":
                    has_explicit_pattern = True
                    if k + 2 < len(arg):
                        break
                    consumed_next = True
                    break
                if ch in short_val_flags:
                    if k + 2 < len(arg):
                        break
                    consumed_next = True
                    break
            i += 2 if consumed_next else 1
            continue
        pos.append(arg)
        i += 1

    paths = pos if (has_explicit_pattern or files_mode) else pos[1:]
    for p in paths:
        if _has_mcp_path(p):
            return _deny("Searching inside /etc/nacfson-mcp from an agent shell is blocked.")
    return True, ""


def _check_tokens_except_flags(args: list[str], value_flags: frozenset[str], attached_prefixes: tuple[str, ...], cmd: str) -> tuple[bool, str]:
    i = 0
    while i < len(args):
        a = args[i]
        if a in value_flags:
            i += 2
            continue
        if any(a.startswith(f"{flg}=") for flg in value_flags if flg.startswith("--")):
            i += 1
            continue
        if attached_prefixes and a.startswith(attached_prefixes):
            i += 1
            continue
        if _has_mcp_path(a):
            return _deny(f"Passing /etc/nacfson-mcp as an argument to `{cmd}` is blocked.")
        i += 1
    return True, ""


def _check_mcp_path_access(cmd: str, args: list[str], words: list[str]) -> tuple[bool, str]:
    if not any(_has_mcp_path(tok) for tok in words):
        return True, ""
    if cmd in ("grep", "egrep", "fgrep", "rg", "ag", "ack"):
        return _check_mcp_path_search(args, cmd)
    if cmd == "git":
        sub_idx = 0
        while sub_idx < len(args):
            a = args[sub_idx]
            if a in ("-C", "--git-dir", "--work-tree", "-c"):
                if sub_idx + 1 < len(args) and _has_mcp_path(args[sub_idx + 1]):
                    return _deny("Accessing /etc/nacfson-mcp via git is blocked.")
                sub_idx += 2
                continue
            if a.startswith(("-C", "--git-dir=", "--work-tree=")):
                if _has_mcp_path(a):
                    return _deny("Accessing /etc/nacfson-mcp via git is blocked.")
                sub_idx += 1
                continue
            if a.startswith("-"):
                if _has_mcp_path(a):
                    return _deny("Accessing /etc/nacfson-mcp via git is blocked.")
                sub_idx += 1
                continue
            break
        if sub_idx >= len(args):
            return _deny("Passing /etc/nacfson-mcp as an argument to `git` is blocked.")
        subcmd = args[sub_idx]
        sub_args = args[sub_idx + 1 :]
        if subcmd == "grep":
            return _check_mcp_path_search(sub_args, "git-grep")
        if subcmd in ("commit", "tag", "notes"):
            i = 0
            while i < len(sub_args):
                a = sub_args[i]
                if a in ("-m", "--message") or (a.startswith("-") and not a.startswith("--") and a.endswith("m")):
                    i += 2
                    continue
                if a.startswith("--message=") or (a.startswith("-m") and len(a) > 2):
                    i += 1
                    continue
                if _has_mcp_path(a):
                    return _deny(f"Passing /etc/nacfson-mcp as an argument to `git {subcmd}` is blocked.")
                i += 1
            return True, ""
        if subcmd in ("log", "rev-list"):
            return _check_tokens_except_flags(
                sub_args,
                frozenset({"-S", "-G", "--grep"}),
                ("-S", "-G", "--grep="),
                f"git {subcmd}",
            )
        return _deny(f"Passing /etc/nacfson-mcp as an argument to `git {subcmd}` is blocked.")

    if cmd == "gh":
        subcmds: list[str] = []
        i = 0
        while i < len(args) and len(subcmds) < 2:
            a = args[i]
            if not a.startswith("-"):
                subcmds.append(a)
                i += 1
            else:
                if _has_mcp_path(a):
                    return _deny("Passing /etc/nacfson-mcp as an argument to `gh` is blocked.")
                i += 1
        if len(subcmds) == 2 and subcmds[0] in ("issue", "pr", "release") and subcmds[1] in ("create", "edit", "comment"):
            return _check_tokens_except_flags(
                args[i:],
                frozenset({"--title", "--body", "-t", "-b"}),
                ("-t=", "-b="),
                f"gh {subcmds[0]} {subcmds[1]}",
            )
        return _deny("Passing /etc/nacfson-mcp as an argument to `gh` is blocked.")

    return _deny("Accessing or modifying /etc/nacfson-mcp from an agent shell is blocked.")


def _check_terraform(cmd_name: str, args: list[str]) -> tuple[bool, str]:
    i = 0
    subcmds: list[str] = []
    while i < len(args):
        arg = args[i]
        if arg.startswith("-"):
            if arg in TERRAFORM_VALUE_FLAGS and i + 1 < len(args):
                i += 2
            else:
                i += 1
            continue
        subcmds.append(arg)
        i += 1
    if not subcmds:
        return True, ""
    first = subcmds[0]
    if first in TERRAFORM_MUTATING_SUBCOMMANDS:
        return _deny(f"Direct mutating `{cmd_name} {first}` is blocked in AI shells.")
    if first == "state" and len(subcmds) > 1 and subcmds[1] in TERRAFORM_STATE_MUTATING:
        return _deny(f"Direct mutating `{cmd_name} state {subcmds[1]}` is blocked in AI shells.")
    return True, ""


def _check_shell_or_eval(
    cmd: str,
    args: list[str],
    herestrings: list[str],
    env: Mapping[str, str],
    depth: int,
) -> tuple[bool, str]:
    if cmd in SHELL_INTERPRETERS:
        has_c = False
        j = 0
        while j < len(args):
            a = args[j]
            if a == "--":
                j += 1
                break
            if a in SHELL_VALUE_FLAGS:
                j += 2
                continue
            if a.startswith(("--rcfile=", "--init-file=")):
                j += 1
                continue
            if (a.startswith("-") or a.startswith("+")) and not a.startswith("--") and len(a) > 1:
                if a.startswith("-") and "c" in a[1:]:
                    has_c = True
                extra_vals = sum(1 for ch in a[1:] if ch in ("o", "O"))
                j += 1 + extra_vals
                continue
            if a.startswith("-") or a.startswith("+"):
                j += 1
                continue
            break
        if has_c:
            for operand in args[j:]:
                ok, reason = _check_text(operand, env, depth + 1)
                if not ok:
                    return ok, reason
        for hs in herestrings:
            ok, reason = _check_text(hs, env, depth + 1)
            if not ok:
                return ok, reason

    if cmd in ("source", "."):
        for hs in herestrings:
            ok, reason = _check_text(hs, env, depth + 1)
            if not ok:
                return ok, reason

    if cmd == "eval" and args:
        eval_args = args[1:] if args[0] == "--" else args
        if eval_args:
            return _check_text(" ".join(eval_args), env, depth + 1)

    return True, ""


def _check_find(args: list[str], env: Mapping[str, str], depth: int) -> tuple[bool, str]:
    i = 0
    while i < len(args):
        arg = args[i]
        if arg in FIND_ONE_ARG_PREDICATES:
            i += 2
            continue
        if arg in FIND_EXEC_FLAGS:
            i += 1
            exec_tokens: list[str] = []
            while i < len(args):
                tok = args[i]
                if tok == ";" or (tok == "+" and exec_tokens and exec_tokens[-1] == "{}"):
                    i += 1
                    break
                exec_tokens.append(tok)
                i += 1
            if exec_tokens:
                ok, reason = _check_tokens(exec_tokens, [], env, depth + 1)
                if not ok:
                    return ok, reason
            continue
        i += 1
    return True, ""


def _check_script(args: list[str], herestrings: list[str], env: Mapping[str, str], depth: int) -> tuple[bool, str]:
    i = 0
    dashdash = False
    saw_c = False
    positional: list[str] = []
    while i < len(args):
        arg = args[i]
        if dashdash:
            positional.append(arg)
            i += 1
            continue
        if arg == "--":
            dashdash = True
            i += 1
            continue
        if arg in ("-c", "--command"):
            saw_c = True
            if i + 1 < len(args):
                ok, reason = _check_text(args[i + 1], env, depth + 1)
                if not ok:
                    return ok, reason
            i += 2
            continue
        if arg.startswith("--command="):
            saw_c = True
            ok, reason = _check_text(arg.split("=", 1)[1], env, depth + 1)
            if not ok:
                return ok, reason
            i += 1
            continue
        if arg in SCRIPT_VALUE_FLAGS:
            i += 2
            continue
        if any(arg.startswith(f"{vf}=") for vf in SCRIPT_VALUE_FLAGS if vf.startswith("--")) or arg.startswith("--"):
            i += 1
            continue
        if arg.startswith("-") and len(arg) > 1:
            consumed_next = False
            for k, ch in enumerate(arg[1:]):
                if ch == "c":
                    saw_c = True
                    rest = arg[k + 2 :]
                    if rest:
                        ok, reason = _check_text(rest, env, depth + 1)
                        if not ok:
                            return ok, reason
                    elif i + 1 < len(args):
                        ok, reason = _check_text(args[i + 1], env, depth + 1)
                        if not ok:
                            return ok, reason
                        consumed_next = True
                    break
                if ch in SCRIPT_SHORT_VALUE_CHARS:
                    if k + 2 == len(arg):
                        consumed_next = True
                    break
            i += 2 if consumed_next else 1
            continue
        positional.append(arg)
        i += 1

    for hs in herestrings:
        ok, reason = _check_text(hs, env, depth + 1)
        if not ok:
            return ok, reason

    if not saw_c and len(positional) >= 2:
        ok, reason = _check_tokens(positional[1:], [], env, depth + 1)
        if not ok:
            return ok, reason

    return True, ""


def _check_special_runners(
    cmd: str,
    args: list[str],
    herestrings: list[str],
    env: Mapping[str, str],
    depth: int,
) -> tuple[bool, str]:
    if cmd == "find":
        return _check_find(args, env, depth)
    if cmd == "script":
        return _check_script(args, herestrings, env, depth)
    return True, ""


def _check_tokens(
    words: list[str],
    herestrings: list[str],
    env: Mapping[str, str],
    depth: int,
) -> tuple[bool, str]:
    if depth > 6:
        return _deny("Excessive shell nesting depth is blocked.")
    if not words:
        for hs in herestrings:
            ok, reason = _check_text(hs, env, depth + 1)
            if not ok:
                return ok, reason
        return True, ""

    cmd_tokens, inline_scripts, stripped_tokens, was_wrapped = _unwrap_command(words)

    if any(_has_mcp_path(tok) for tok in stripped_tokens):
        return _deny("Accessing or modifying /etc/nacfson-mcp from an agent shell is blocked.")

    for script in inline_scripts:
        ok, reason = _check_text(script, env, depth + 1)
        if not ok:
            return ok, reason

    if not cmd_tokens:
        for hs in herestrings:
            ok, reason = _check_text(hs, env, depth + 1)
            if not ok:
                return ok, reason
        return True, ""

    cmd_raw = cmd_tokens[0]
    cmd = os.path.basename(cmd_raw)
    args = cmd_tokens[1:]

    if cmd in BLOCKED_BINARIES:
        return _deny(f"Direct `{cmd}` terminal commands are blocked.")

    if cmd in ("terraform", "tofu"):
        ok, reason = _check_terraform(cmd, args)
        if not ok:
            return ok, reason

    if cmd == "nacfson-mcp":
        for a in args:
            if a == "--serve-unix" or a.startswith("--serve-unix=") or a == "--kubeconfig" or a.startswith("--kubeconfig="):
                return _deny("Starting `nacfson-mcp --serve-unix` or passing `--kubeconfig` from an agent shell is blocked.")

    ok, reason = _check_mcp_path_access(cmd, args, cmd_tokens)
    if not ok:
        return ok, reason

    ok, reason = _check_shell_or_eval(cmd, args, herestrings, env, depth)
    if not ok:
        return ok, reason

    ok, reason = _check_special_runners(cmd, args, herestrings, env, depth)
    if not ok:
        return ok, reason

    if was_wrapped and cmd not in SAFE_READONLY_PATH_TOOLS:
        for pos, tok in enumerate(args):
            sub_cmd = os.path.basename(tok)
            if sub_cmd in BLOCKED_BINARIES:
                return _deny(f"Direct `{sub_cmd}` terminal commands are blocked.")
            if sub_cmd in ("terraform", "tofu"):
                ok, reason = _check_terraform(sub_cmd, args[pos + 1 :])
                if not ok:
                    return ok, reason
            ok, reason = _check_shell_or_eval(sub_cmd, args[pos + 1 :], herestrings, env, depth)
            if not ok:
                return ok, reason
            ok, reason = _check_special_runners(sub_cmd, args[pos + 1 :], herestrings, env, depth)
            if not ok:
                return ok, reason

    return True, ""


def _segment_executes_stdin(segment: str) -> bool:
    words, _, _ = _tokenize_segment(segment)
    if not words:
        return False
    cmd_tokens, _, _, was_wrapped = _unwrap_command(words)
    if not cmd_tokens:
        return any(os.path.basename(w) in ("parallel", "sem") for w in words)
    cmd = os.path.basename(cmd_tokens[0])
    if cmd in SHELL_INTERPRETERS or cmd in ("source", ".", "script"):
        return True
    if was_wrapped and cmd not in SAFE_READONLY_PATH_TOOLS:
        for tok in cmd_tokens[1:]:
            sub_cmd = os.path.basename(tok)
            if sub_cmd in SHELL_INTERPRETERS or sub_cmd in ("source", ".", "script"):
                return True
    return False


def _check_segment(
    segment: str,
    env: Mapping[str, str],
    depth: int,
    heredoc_bodies: list[str] | None = None,
) -> tuple[bool, str]:
    if depth > 6:
        return _deny("Excessive shell nesting depth is blocked.")
    words, redirs, herestrings = _tokenize_segment(segment)
    for target in redirs:
        if _has_mcp_path(target):
            return _deny("Redirection against /etc/nacfson-mcp is forbidden.")
    stdin_bodies = herestrings + (heredoc_bodies or [])
    return _check_tokens(words, stdin_bodies, env, depth)


def _check_text(command: str, env: Mapping[str, str], depth: int) -> tuple[bool, str]:
    if depth > 6:
        return _deny("Excessive shell nesting depth is blocked.")
    segments = _split_segments(command)
    for idx, (seg, heredocs, piped_to_next) in enumerate(segments):
        for body, quoted in heredocs:
            if not quoted:
                for sub in _extract_unquoted_heredoc_subshells(body):
                    sub_ok, sub_reason = _check_text(sub, env, depth + 1)
                    if not sub_ok:
                        return sub_ok, sub_reason
        if heredocs and piped_to_next:
            k = idx + 1
            while k < len(segments):
                next_seg, _, next_piped = segments[k]
                if _segment_executes_stdin(next_seg):
                    for body, _ in heredocs:
                        ok, reason = _check_text(body, env, depth + 1)
                        if not ok:
                            return ok, reason
                if not next_piped:
                    break
                k += 1
        ok, reason = _check_segment(seg, env, depth, [body for body, _ in heredocs])
        if not ok:
            return ok, reason
        for sub in _extract_subshells(seg):
            sub_ok, sub_reason = _check_text(sub, env, depth + 1)
            if not sub_ok:
                return sub_ok, sub_reason
    return True, ""


def check_command(command: str, env: Mapping[str, str] | None = None) -> tuple[bool, str]:
    if not command or not command.strip():
        return True, ""
    active_env = os.environ if env is None else env
    if active_env.get("ALLOW_CLUSTER_CLI") == "1":
        return True, ""
    return _check_text(command, active_env, 0)


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description="PreToolUse cluster CLI guardrail.")
    parser.add_argument("--command", default="", help="Shell command string to inspect.")
    args = parser.parse_args(argv)
    ok, reason = check_command(args.command, os.environ)
    if ok:
        return 0
    print(reason, file=sys.stderr)
    return 2


if __name__ == "__main__":
    raise SystemExit(main())
