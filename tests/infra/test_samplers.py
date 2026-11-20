import json
import os
import subprocess
import tempfile
import unittest
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parents[2]
SAMPLER = REPO_ROOT / "infra" / "oci" / "ansible" / "roles" / "samplers" / "files" / "ops-sample.sh"
SCRAPE = REPO_ROOT / "tests" / "fixtures" / "jmx-exporter" / "1.7.0" / "kafka-4.3.2-single-node.prom"
CONTRACT_METRICS = {
    "kafka.brokers.active", "kafka.brokers.fenced", "kafka.controller.active", "kafka.partitions.offline",
    "kafka.partitions.under_replicated", "kafka.partitions.under_min_isr", "kafka.produce.latency_p99",
    "kafka.bytes_in", "kafka.bytes_out", "kafka.heap.used", "kafka.gc.time",
    "os.mem.available", "os.disk.free_ratio", "os.cpu.steal",
}


def run_sampler(tmp: Path) -> list[dict]:
    env = {**os.environ, "OPS_VM": "a1-1", "OPS_SAMPLES": str(tmp / "samples"), "OPS_STATE": str(tmp / "state"),
           "OPS_EXPORTER": f"file://{SCRAPE}", "OPS_DATA_DIR": str(tmp)}
    subprocess.run(["sh", str(SAMPLER)], env=env, check=True, capture_output=True, text=True)
    path = tmp / "samples" / "metrics.jsonl"
    return [json.loads(line) for line in path.read_text(encoding="utf-8").splitlines()]


class SamplerTests(unittest.TestCase):
    def test_every_contract_metric_from_a_real_scrape(self):
        with tempfile.TemporaryDirectory() as tmp:
            records = run_sampler(Path(tmp))
        names = {r["metric"] for r in records}
        self.assertEqual(names, CONTRACT_METRICS)
        for r in records:
            self.assertEqual((r["schema"], r["source"]), (1, "vm/a1-1"))
            self.assertIsInstance(r["value"], (int, float))

    def test_values_come_from_the_scrape(self):
        with tempfile.TemporaryDirectory() as tmp:
            by_name = {r["metric"]: r for r in run_sampler(Path(tmp))}
        self.assertEqual(by_name["kafka.brokers.active"]["value"], 1)
        heap = next(float(line.split()[1]) for line in SCRAPE.read_text(encoding="utf-8").splitlines() if line.startswith("jvm_heap_used_bytes "))
        self.assertEqual(by_name["kafka.heap.used"]["value"], heap)
        self.assertEqual(by_name["kafka.heap.used"]["unit"], "bytes")
        self.assertGreater(by_name["os.mem.available"]["value"], 0)
        self.assertTrue(0 <= by_name["os.disk.free_ratio"]["value"] <= 1)

    def test_gc_time_is_a_rate_between_samples(self):
        with tempfile.TemporaryDirectory() as tmp:
            run_sampler(Path(tmp))
            second = run_sampler(Path(tmp))
        gc = [r for r in second if r["metric"] == "kafka.gc.time"]
        self.assertEqual(gc[-1]["value"], 0)


if __name__ == "__main__":
    unittest.main()
