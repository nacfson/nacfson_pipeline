import os
import re
import tempfile
import unittest

TESTS_DIR = os.path.abspath(os.path.join(os.path.dirname(__file__), ".."))
GUARD_ROOT = os.environ.get("OPENBAO_STATUS_GUARD_ROOT", TESTS_DIR)
THIS_FILE = os.path.abspath(__file__)
FIXTURE_ROOT = os.path.join(TESTS_DIR, "fixtures")

KEY_PATTERN = r"""\\?["']%s\\?["']\s*:"""
STATUS_KEYS = tuple(re.compile(KEY_PATTERN % key) for key in ("sealed", "initialized"))

FIX = (
    "Load a fixture from tests/fixtures/openbao/2.0.0/status/ instead of writing status JSON by hand, "
    "and refresh the fixtures with scripts/rehearse-openbao-seal-migration.sh --write-fixtures. "
    "Hand-written status output is what let the #163 and #165 cutover bugs reach production."
)


def find_violations(root, skip=()):
    found = []
    for directory, dirnames, filenames in os.walk(root):
        dirnames[:] = [d for d in dirnames if d != "__pycache__"]
        for name in filenames:
            path = os.path.join(directory, name)
            if os.path.abspath(path) in skip or os.path.abspath(path).startswith(FIXTURE_ROOT + os.sep):
                continue
            try:
                with open(path, encoding="utf-8") as fh:
                    text = fh.read()
            except (UnicodeDecodeError, OSError):
                continue
            if all(pattern.search(text) for pattern in STATUS_KEYS):
                found.append(os.path.relpath(path, root))
    return sorted(found)


class NoHandwrittenOpenBaoStatusTest(unittest.TestCase):
    def test_tests_replay_captured_fixtures_instead_of_handwritten_status(self):
        found = find_violations(GUARD_ROOT, skip=(THIS_FILE,))
        self.assertEqual(
            found, [],
            "Hand-written OpenBao status JSON (both \"sealed\" and \"initialized\" keys) in:\n  "
            + "\n  ".join(found) + "\n" + FIX,
        )

    def test_guard_flags_status_json_in_python_shell_and_escaped_forms(self):
        samples = {
            "inline.py": "STATUS = '{\"type\":\"shamir\",\"initialized\":true,\"sealed\":false}'\n",
            "dict.py": "STATUS = {'initialized': True, 'sealed': False}\n",
            "stub.sh": "echo '{\"initialized\": true, \"sealed\": true}'\n",
            "escaped.py": "STATUS = \"{\\\"initialized\\\": true, \\\"sealed\\\": false}\"\n",
        }
        with tempfile.TemporaryDirectory() as root:
            for name, body in samples.items():
                with open(os.path.join(root, name), "w", encoding="utf-8") as fh:
                    fh.write(body)
            self.assertEqual(find_violations(root), sorted(samples))

    def test_guard_allows_fixture_loading_and_unrelated_json(self):
        with tempfile.TemporaryDirectory() as root:
            with open(os.path.join(root, "ok.py"), "w", encoding="utf-8") as fh:
                fh.write("FIXTURE = 'tests/fixtures/openbao/2.0.0/status/sealed-shamir.json'\nDATA = {'sealed_total': 1}\n")
            self.assertEqual(find_violations(root), [])


if __name__ == "__main__":
    unittest.main()
