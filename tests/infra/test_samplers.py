import json
import os
import subprocess
import tempfile
import unittest
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parents[2]
SAMPLER = REPO_ROOT / "infra" / "oci" / "ansible" / "roles" / "samplers" / "files" / "ops-sample.sh"
CERT_SAMPLER = REPO_ROOT / "infra" / "oci" / "ansible" / "roles" / "samplers" / "files" / "ops-certs.sh"
SCRAPE = REPO_ROOT / "tests" / "fixtures" / "jmx-exporter" / "1.7.0" / "kafka-4.3.2-single-node.prom"
CONTRACT_METRICS = {
    "kafka.brokers.active", "kafka.brokers.fenced", "kafka.controller.active", "kafka.partitions.offline",
    "kafka.partitions.under_replicated", "kafka.partitions.under_min_isr", "kafka.produce.latency_p99",
    "kafka.bytes_in", "kafka.bytes_out", "kafka.heap.used", "kafka.gc.time",
    "os.mem.available", "os.disk.free_ratio", "os.cpu.steal",
}


def run_sampler(tmp: Path, extra_env: dict[str, str] | None = None) -> list[dict]:
    env = {
        **os.environ,
        "OPS_VM": "a1-1",
        "OPS_SAMPLES": str(tmp / "samples"),
        "OPS_STATE": str(tmp / "state"),
        "OPS_EXPORTER": f"file://{SCRAPE}",
        "OPS_DATA_DIR": str(tmp),
        "OPS_LOKI_DATA_DIR": str(tmp / "no-loki"),
    }
    if extra_env:
        env.update(extra_env)
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

    def test_loki_disk_sampling_requires_mountpoint_unless_bypassed(self):
        with tempfile.TemporaryDirectory() as tmp_str:
            tmp = Path(tmp_str)
            loki_dir = tmp / "loki"
            loki_dir.mkdir()
            records = run_sampler(tmp, {"OPS_VM": "a1-2", "OPS_LOKI_DATA_DIR": str(loki_dir)})
            self.assertNotIn("loki.disk.free_ratio", {r["metric"] for r in records})

            with tempfile.TemporaryDirectory() as bypass_str:
                bypass_tmp = Path(bypass_str)
                bypass_dir = bypass_tmp / "loki"
                bypass_dir.mkdir()
                bypassed = run_sampler(
                    bypass_tmp,
                    {"OPS_VM": "a1-2", "OPS_LOKI_DATA_DIR": str(bypass_dir), "OPS_LOKI_CHECK_MOUNT": "0"},
                )
                self.assertEqual(len([r for r in bypassed if r["metric"] == "loki.disk.free_ratio"]), 1)

            with tempfile.TemporaryDirectory() as empty_str:
                empty_tmp = Path(empty_str)
                empty_dir = empty_tmp / "loki"
                empty_dir.mkdir()
                fake_bin = empty_tmp / "fake-bin"
                fake_bin.mkdir()
                fake_df = fake_bin / "df"
                fake_df.write_text(
                    "#!/bin/sh\n"
                    "echo 'Filesystem     1024-blocks  Used Available Capacity Mounted on'\n"
                    "echo '                  16382932     0  16382932       0% /'\n",
                    encoding="utf-8",
                )
                fake_df.chmod(0o755)
                empty_fs_records = run_sampler(
                    empty_tmp,
                    {
                        "OPS_VM": "a1-2",
                        "OPS_LOKI_DATA_DIR": str(empty_dir),
                        "OPS_LOKI_CHECK_MOUNT": "0",
                        "PATH": f"{fake_bin}:{os.environ.get('PATH', '')}",
                    },
                )
                empty_loki = [r for r in empty_fs_records if r["metric"] == "loki.disk.free_ratio"]
                self.assertEqual(len(empty_loki), 1)
                self.assertEqual(empty_loki[0]["value"], 1.0)

            (tmp / "state").mkdir(exist_ok=True)
            (tmp / "state" / "loki-disk-low").write_text("1\n", encoding="utf-8")
            mounted_records = run_sampler(
                tmp,
                {"OPS_VM": "a1-2", "OPS_LOKI_DATA_DIR": "/", "OPS_LOKI_CHECK_MOUNT": "1"},
            )
            loki_metrics = [r for r in mounted_records if r["metric"] == "loki.disk.free_ratio"]
            self.assertEqual(len(loki_metrics), 1)
            self.assertTrue(0 <= loki_metrics[0]["value"] <= 1)
            events = [
                json.loads(line)
                for line in (tmp / "samples" / "events.jsonl").read_text(encoding="utf-8").splitlines()
            ]
            self.assertTrue(
                any(e["kind"] == "loki.disk.ok" and e["subject"] == "loki/disk" and e["state"] == "ok" for e in events),
                events,
            )

    def test_cert_sampler_scans_kafka_and_loki_dirs(self):
        with tempfile.TemporaryDirectory() as tmp_str:
            tmp = Path(tmp_str)
            kafka_tls = tmp / "kafka-tls"
            loki_tls = tmp / "loki-tls"
            kafka_tls.mkdir()
            loki_tls.mkdir()
            for directory, name, days in ((kafka_tls, "broker-2", 60), (loki_tls, "loki-a1-2", 5)):
                subprocess.run(
                    [
                        "openssl", "req", "-x509", "-newkey", "ec", "-pkeyopt", "ec_paramgen_curve:P-256",
                        "-nodes", "-days", str(days),
                        "-keyout", str(directory / f"{name}.key"),
                        "-out", str(directory / f"{name}.crt"),
                        "-subj", f"/CN={name}",
                    ],
                    check=True,
                    capture_output=True,
                )
            (loki_tls / "ca.crt").write_text((kafka_tls / "broker-2.crt").read_text(encoding="utf-8"), encoding="utf-8")

            env = {
                **os.environ,
                "OPS_VM": "a1-2",
                "OPS_SAMPLES": str(tmp / "samples"),
                "OPS_STATE": str(tmp / "state"),
                "OPS_TLS_DIR": str(kafka_tls),
                "OPS_LOKI_TLS_DIR": str(loki_tls),
            }
            subprocess.run(["sh", str(CERT_SAMPLER)], env=env, check=True, capture_output=True, text=True)
            events = [
                json.loads(line)
                for line in (tmp / "samples" / "events.jsonl").read_text(encoding="utf-8").splitlines()
            ]
            self.assertEqual(len(events), 1)
            self.assertEqual(events[0]["kind"], "cert.expiring")
            self.assertEqual(events[0]["subject"], "cert/a1-2/loki-a1-2")
            metrics = [
                json.loads(line)
                for line in (tmp / "samples" / "metrics.jsonl").read_text(encoding="utf-8").splitlines()
            ]
            self.assertEqual(metrics[0]["metric"], "cert.days_left.min")
            self.assertLessEqual(metrics[0]["value"], 5)

            subprocess.run(
                ["sh", str(CERT_SAMPLER)],
                env={**env, "OPS_LOKI_TLS_DIR": str(tmp / "missing-loki-tls")},
                check=True,
                capture_output=True,
                text=True,
            )


if __name__ == "__main__":
    unittest.main()
