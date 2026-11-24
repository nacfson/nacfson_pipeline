import importlib.util
import json
import os
import re
import subprocess
import sys
import unittest

REPO = os.path.abspath(os.path.join(os.path.dirname(__file__), "..", ".."))
LIB = os.path.join(REPO, "scripts", "lib")
STATUS_FIXTURES = os.path.join(REPO, "tests", "fixtures", "openbao", "2.0.0", "status")


def _load(name):
    spec = importlib.util.spec_from_file_location(name, os.path.join(LIB, f"{name}.py"))
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


status = _load("openbao_status")
compare = _load("openbao_fixture_compare")


def _fixture_text(name):
    with open(os.path.join(STATUS_FIXTURES, f"{name}.json"), encoding="utf-8") as fh:
        return fh.read()


def _named_state(stem):
    named = [s for s in status.STATES if re.search(rf"(^|-){re.escape(s)}(-|$)", stem)]
    return named


class ClassifierFixtureTest(unittest.TestCase):
    def test_every_fixture_classifies_to_the_state_its_filename_names(self):
        files = sorted(f for f in os.listdir(STATUS_FIXTURES) if f.endswith(".json"))
        self.assertTrue(files)
        for name in files:
            with self.subTest(fixture=name):
                named = _named_state(name[: -len(".json")])
                self.assertEqual(len(named), 1, f"{name} must name exactly one state, found {named}")
                self.assertEqual(status.classify(_fixture_text(name[: -len(".json")])), named[0])

    def test_fixture_directory_holds_only_the_rehearsal_captures_and_the_gke_capture(self):
        rehearsed = {f"{state}.json" for state in status.STATES if state != status.UNREACHABLE}
        self.assertEqual(set(os.listdir(STATUS_FIXTURES)), rehearsed | {"gke-unsealed-auto-gcpckms.json"})

    def test_classifier_command_reads_stdin_and_prints_the_state(self):
        result = subprocess.run(
            [sys.executable, os.path.join(LIB, "openbao_status.py")],
            input=_fixture_text("sealed-migration"), capture_output=True, text=True, timeout=30,
        )
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stdout.strip(), "sealed-migration")

    def test_empty_and_unparseable_output_is_unreachable_without_a_traceback(self):
        for text in ("", "\n", "   \n", "Error: connection refused", '{"initialized": tru', "[]", "null", '"sealed"'):
            with self.subTest(text=text):
                result = subprocess.run(
                    [sys.executable, os.path.join(LIB, "openbao_status.py")],
                    input=text, capture_output=True, text=True, timeout=30,
                )
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertEqual(result.stdout.strip(), "unreachable")
                self.assertEqual(result.stderr, "")


class RehearsalVersionTest(unittest.TestCase):
    def test_rehearsal_binary_and_fixtures_match_the_deployed_image(self):
        with open(os.path.join(REPO, "deploy", "platform", "vault", "openbao-statefulset.yaml"), encoding="utf-8") as fh:
            deployed = re.search(r"image: openbao/openbao:(\S+)", fh.read()).group(1)
        with open(os.path.join(REPO, "scripts", "vault", "rehearse-openbao-seal-migration.sh"), encoding="utf-8") as fh:
            rehearsed = re.search(r'^BAO_VERSION="([^"]+)"', fh.read(), re.M).group(1)
        self.assertEqual(rehearsed, deployed)
        self.assertEqual(os.path.basename(os.path.dirname(STATUS_FIXTURES)), deployed)
        for name in os.listdir(STATUS_FIXTURES):
            with self.subTest(fixture=name):
                self.assertEqual(json.loads(_fixture_text(name[: -len(".json")]))["version"], deployed)


class GcpckmsAnchorTest(unittest.TestCase):
    def test_transit_auto_unseal_fixture_agrees_with_the_real_gcpckms_capture(self):
        transit = json.loads(_fixture_text("unsealed-auto"))
        gcpckms = json.loads(_fixture_text("gke-unsealed-auto-gcpckms"))
        self.assertEqual(compare.differences(transit, gcpckms), [])
        self.assertEqual(compare.semantic(transit), compare.semantic(gcpckms))

    def test_comparison_ignores_volatile_values_but_not_semantic_ones(self):
        base = json.loads(_fixture_text("unsealed-auto"))
        volatile = dict(base, cluster_id="other", nonce="x", raft_applied_index=1, leader_address="http://elsewhere")
        self.assertEqual(compare.differences(volatile, base), [])
        self.assertTrue(compare.differences(dict(base, type="gcpckms"), base))
        self.assertTrue(compare.differences(dict(base, sealed=True), base))
        dropped = {k: v for k, v in base.items() if k != "recovery_seal"}
        self.assertTrue(compare.differences(dropped, base))


if __name__ == "__main__":
    unittest.main()
