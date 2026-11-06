"""Test log contract schema invariants across contracts, Lua collector, and Dockerfile."""

import os
import re
import unittest

BASE_DIR = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
CONTRACT_FILE = os.path.join(BASE_DIR, "contracts", "CONTRACT_LOG_SCHEMA.md")
LUA_FILE = os.path.join(
    BASE_DIR, "deploy", "platform", "observability", "fluent-bit", "log_contract.lua"
)
DOCKERFILE_LOGCHECK = os.path.join(
    BASE_DIR, "services", "error-pipeline", "Dockerfile.logcheck"
)


def _tables_under(doc: str, heading: str) -> list[list[list[str]]]:
    lines = doc.splitlines()
    i = 0
    while i < len(lines) and lines[i].strip() != heading:
        i += 1
    if i >= len(lines):
        raise AssertionError(f"heading {heading!r} not found in {CONTRACT_FILE}")
    i += 1
    tables: list[list[list[str]]] = []
    while i < len(lines):
        line = lines[i]
        if line.startswith("# ") or line.startswith("## "):
            break
        if line.startswith("|"):
            current_rows: list[list[str]] = []
            while i < len(lines) and lines[i].startswith("|"):
                cells = [c.strip() for c in lines[i].strip().strip("|").split("|")]
                if cells[0].strip("-: "):
                    current_rows.append(cells)
                i += 1
            if len(current_rows) >= 2:
                tables.append(current_rows)
            continue
        i += 1
    return tables


def _table_under(doc: str, heading: str, required_column: str = "") -> list[list[str]]:
    tables = _tables_under(doc, heading)
    if not tables:
        raise AssertionError(f"no table found under heading {heading!r}")
    if not required_column:
        return tables[0]
    for tbl in tables:
        if required_column in tbl[0]:
            return tbl
    raise AssertionError(f"no table under {heading!r} contains column {required_column!r}")


def _column_index(header: list[str], name: str) -> int:
    for idx, h in enumerate(header):
        if h == name:
            return idx
    raise AssertionError(f"column {name!r} not found in {header}")


class TestLogContractSchema(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        with open(CONTRACT_FILE, encoding="utf-8") as fh:
            cls.contract = fh.read()

    def test_section_2_required_keys(self):
        rows = _table_under(self.contract, "## 2. Line structure (project-owned, every line)")
        key_col = _column_index(rows[0], "JSON key")
        req_col = _column_index(rows[0], "Required")

        required = []
        for r in rows[1:]:
            if r[req_col] == "yes":
                matches = re.findall(r"`([^`]+)`", r[key_col])
                required.extend(matches)

        expected = ["time", "level", "msg", "service.name", "service.version"]
        self.assertEqual(sorted(required), sorted(expected))

    def test_section_2_level_values(self):
        rows = _table_under(self.contract, "## 2. Line structure (project-owned, every line)")
        key_col = _column_index(rows[0], "JSON key")
        val_col = _column_index(rows[0], "Values")

        level_cell = ""
        for r in rows[1:]:
            if "`level`" in r[key_col]:
                level_cell = r[val_col]
                break
        self.assertTrue(level_cell, "level row not found in section 2 table")
        for lvl in ["debug", "info", "warn", "error", "fatal"]:
            self.assertIn(f"`{lvl}`", level_cell)

    def test_section_3_error_fields(self):
        rows = _table_under(self.contract, "## 3. Error fields (when `level` is `error` or `fatal`)")
        key_col = _column_index(rows[0], "JSON key")
        req_col = _column_index(rows[0], "Required")

        fields: dict[str, str] = {}
        for r in rows[1:]:
            matches = re.findall(r"`([^`]+)`", r[key_col])
            for m in matches:
                fields[m] = r[req_col]

        self.assertIn("exception.message", fields)
        self.assertIn("exception.type", fields)
        self.assertIn("exception.stacktrace", fields)
        self.assertIn("error.fingerprint", fields)

        self.assertIn("yes", fields["exception.message"])
        self.assertIn("yes", fields["exception.type"])
        self.assertIn("fatal", fields["exception.stacktrace"])
        self.assertEqual("no", fields["error.fingerprint"])

    def test_section_4_runtime_crash_signatures(self):
        rows = _table_under(self.contract, "### Runtime crash signatures")
        runtime_col = _column_index(rows[0], "Runtime")
        runtimes = [r[runtime_col] for r in rows[1:]]
        self.assertEqual(sorted(runtimes), ["Go", "JVM", "Python"])

    def test_section_4_third_party_servers(self):
        rows = _table_under(self.contract, "### Third-party server formats")
        server_col = _column_index(rows[0], "Server")
        servers = [r[server_col] for r in rows[1:]]
        self.assertIn("nginx", servers)

    def test_section_6_migration_rows(self):
        rows = _table_under(self.contract, "## 6. Migration from version 2", required_column="Project")
        proj_col = _column_index(rows[0], "Project")
        status_col = _column_index(rows[0], "Status")

        projects = [r[proj_col] for r in rows[1:]]
        self.assertEqual(len(projects), 5)
        for r in rows[1:]:
            self.assertEqual(r[status_col], "pending")

    def test_lua_contract_tables_if_present(self):
        if not os.path.exists(LUA_FILE):
            self.skipTest(f"{LUA_FILE} not created yet (scheduled for PR 3 T019)")

        with open(LUA_FILE, encoding="utf-8") as fh:
            content = fh.read()

        req_match = re.search(r"REQUIRED\s*=\s*\{([^}]+)\}", content)
        self.assertTrue(req_match, "REQUIRED table missing in log_contract.lua")
        req_keys = re.findall(r'"([^"]+)"', req_match.group(1))
        expected_req = ["time", "level", "msg", "service.name", "service.version"]
        self.assertEqual(sorted(req_keys), sorted(expected_req))

        res_match = re.search(r"RESERVED\s*=\s*\{([^}]+)\}", content)
        self.assertTrue(res_match, "RESERVED table missing in log_contract.lua")
        res_keys = re.findall(r'"([^"]+)"', res_match.group(1))
        for expected_res in ["observed_time", "log.iostream"]:
            self.assertIn(expected_res, res_keys)

    def test_dockerfile_logcheck_pinned_base_images(self):
        if not os.path.exists(DOCKERFILE_LOGCHECK):
            self.skipTest(f"{DOCKERFILE_LOGCHECK} not created yet (scheduled for PR 1 T034)")

        with open(DOCKERFILE_LOGCHECK, encoding="utf-8") as fh:
            lines = fh.readlines()

        from_lines = [l.strip() for l in lines if l.strip().startswith("FROM")]
        self.assertTrue(from_lines, "Dockerfile.logcheck has no FROM line")

        for line in from_lines:
            # Must contain exact x.y.z tag or sha256 digest
            # E.g. golang:1.22.12-alpine or gcr.io/distroless/static@sha256:...
            has_sha = "@sha256:" in line
            has_patch_tag = bool(re.search(r":\d+\.\d+\.\d+(-[a-zA-Z0-9._-]+)?", line))
            self.assertTrue(
                has_sha or has_patch_tag,
                f"FROM line does not use exact patch tag or @sha256 digest: {line}",
            )


if __name__ == "__main__":
    unittest.main()
