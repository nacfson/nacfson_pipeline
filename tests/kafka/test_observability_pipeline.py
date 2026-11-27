import json
import ssl
import subprocess
import time
import unittest
import urllib.error
import urllib.parse
import urllib.request
import uuid
from datetime import datetime, timedelta, timezone
from pathlib import Path

from tests.kafka import harness
from tests.kafka.test_story1 import consume, produce

OBS_TLS = harness.REHEARSAL / ".run" / "obs-tls" / "test-client"
FAKENOTIFY_URL = "http://127.0.0.1:18080"
DETECTOR_URL = "http://127.0.0.1:18081"
LOKI_URL = "https://127.0.0.1:13100"


def alpha_tag() -> str:
    raw = uuid.uuid4().hex[:12]
    return "".join(chr(ord("a") + (int(ch, 16) % 26)) for ch in raw)


def http_get(url: str, headers: dict[str, str] | None = None, ssl_ctx: ssl.SSLContext | None = None) -> tuple[int, str]:
    req = urllib.request.Request(url, headers=headers or {}, method="GET")
    with urllib.request.urlopen(req, timeout=10, context=ssl_ctx) as resp:
        return resp.status, resp.read().decode("utf-8")


def loki_ssl_context(with_client_cert: bool = True) -> ssl.SSLContext:
    ctx = ssl.create_default_context(ssl.Purpose.SERVER_AUTH, cafile=str(OBS_TLS / "ca.crt"))
    if with_client_cert:
        ctx.load_cert_chain(certfile=str(OBS_TLS / "client.crt"), keyfile=str(OBS_TLS / "client.key"))
    return ctx


def requests_log() -> list[dict]:
    _, body = http_get(f"{FAKENOTIFY_URL}/requests")
    return json.loads(body)


def channel_requests_since(channel: str, start: int) -> list[dict]:
    return [r for r in requests_log()[start:] if r.get("channel") == channel]


def consumer_group_ready(group: str) -> bool:
    out = harness.target("rehearsal").admin("kafka-consumer-groups.sh", "--describe", "--group", group, check=False)
    if out.returncode != 0:
        return False
    lines = [line for line in out.stdout.splitlines() if line.strip() and not line.startswith("GROUP")]
    return any("kafka-rehearsal" in line or "/" in line for line in lines)


def loki_query_range(tenant: str, expr: str, start_ns: int, end_ns: int) -> dict:
    params = urllib.parse.urlencode({
        "query": expr,
        "start": str(start_ns),
        "end": str(end_ns),
        "limit": "50",
        "direction": "backward",
    })
    _, body = http_get(
        f"{LOKI_URL}/loki/api/v1/query_range?{params}",
        headers={"X-Scope-OrgID": tenant},
        ssl_ctx=loki_ssl_context(with_client_cert=True),
    )
    return json.loads(body)


def check_http_ok(url: str, ssl_ctx: ssl.SSLContext | None = None) -> bool:
    try:
        status, _ = http_get(url, ssl_ctx=ssl_ctx)
        return status == 200
    except Exception:
        return False


class ObservabilityPipelineTests(unittest.TestCase):
    suite_start_ns: int = 0
    structured_tag: str = ""

    @classmethod
    def setUpClass(cls):
        cls.suite_start_ns = int(time.time() * 1e9)
        cls.structured_tag = alpha_tag()
        harness.wait_for(lambda: check_http_ok(f"{FAKENOTIFY_URL}/healthz"), 120, interval=2, what="fakenotify healthz")
        harness.wait_for(lambda: check_http_ok(f"{DETECTOR_URL}/readyz"), 180, interval=2, what="error-detector readyz")
        harness.wait_for(
            lambda: check_http_ok(f"{LOKI_URL}/ready", ssl_ctx=loki_ssl_context(with_client_cert=True)),
            180,
            interval=2,
            what="loki ready",
        )
        harness.wait_for(
            lambda: subprocess.run(
                ["podman", "exec", "kafka-rehearsal-ops-relay", "/ops-relay", "ready"],
                capture_output=True,
                check=False,
            ).returncode == 0,
            240,
            interval=2,
            what="ops-relay ready",
        )
        for group in ("error-detector", "vector-loki", "ops-relay"):
            harness.wait_for(lambda g=group: consumer_group_ready(g), 120, interval=2, what=f"consumer group {group}")

    def test_1_acl_enforcement(self):
        node = harness.running_node_container()
        wrote_valid = produce("error-detector-a1-2", "logs.proj.valid", '{"msg":"denied"}')
        self.assertRegex(
            wrote_valid.stdout + wrote_valid.stderr,
            "TopicAuthorizationException|Not authorized|TOPIC_AUTHORIZATION_FAILED",
        )

        read_ops = subprocess.run(
            [
                *harness.kafka_exec(node),
                f"{harness.KAFKA_BIN}/kafka-console-consumer.sh",
                "--bootstrap-server",
                harness.target("rehearsal").bootstrap,
                "--command-config",
                f"{harness.CLIENTS_IN_CONTAINER}/error-detector-a1-2.properties",
                "--topic",
                "ops.platform.events",
                "--from-beginning",
                "--timeout-ms",
                "15000",
            ],
            capture_output=True,
            text=True,
            check=False,
            timeout=120,
        )
        self.assertRegex(read_ops.stdout + read_ops.stderr, "AuthorizationException|not authorized")

        wrote_alerts = produce("ops-relay", "alerts.errors", '{"title":"denied"}')
        self.assertRegex(
            wrote_alerts.stdout + wrote_alerts.stderr,
            "ClusterAuthorizationException|TopicAuthorizationException|Not authorized|TOPIC_AUTHORIZATION_FAILED",
        )

    def test_2_structured_error_and_dedup(self):
        tag = self.structured_tag
        start = len(requests_log())
        now_iso = datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")
        first = {
            "time": now_iso,
            "observed_time": now_iso,
            "level": "error",
            "msg": f"database failure {tag}",
            "service.name": "backend",
            "service.version": "1.0.0",
            "exception.type": "QueryError",
            "exception.message": f"failed query {tag} 11111111-2222-3333-4444-555555555555",
            "trace_id": "4bf92f3577b34da6a3ce929d0e0e4736",
            "span_id": "00f067aa0ba902b7",
            "k8s.namespace.name": "proj-pn",
            "k8s.container.name": "backend",
            "k8s.pod.name": "pn-backend-0",
        }
        res = produce("fluent-bit-gke", "logs.proj.valid", json.dumps(first, separators=(",", ":")))
        self.assertEqual(res.returncode, 0, res.stdout + res.stderr)

        first_hits = harness.wait_for(
            lambda: [
                r for r in channel_requests_since("errors", start)
                if tag in json.dumps(r.get("payload", {}))
            ] or None,
            60,
            interval=1,
            what="first structured error alert",
        )
        self.assertEqual(len(first_hits), 1, first_hits)
        first_payload = first_hits[0]["payload"]
        self.assertIn("New error in proj-pn/backend", first_payload.get("title", ""))
        field_names = {f["name"] for f in first_payload.get("fields", [])}
        self.assertNotIn("Grafana", field_names)
        first_seen_at = time.monotonic()

        after_first = len(requests_log())
        dups = []
        for idx, uid in enumerate(
            (
                "aaaaaaaa-bbbb-cccc-dddd-111111111111",
                "bbbbbbbb-cccc-dddd-eeee-222222222222",
                "cccccccc-dddd-eeee-ffff-333333333333",
            ),
            start=1,
        ):
            dup_ts = (datetime.now(timezone.utc) + timedelta(milliseconds=idx * 50)).strftime("%Y-%m-%dT%H:%M:%SZ")
            dups.append(
                json.dumps(
                    {
                        "time": dup_ts,
                        "observed_time": dup_ts,
                        "level": "error",
                        "msg": f"database failure {tag}",
                        "service.name": "backend",
                        "service.version": "1.0.0",
                        "exception.type": "QueryError",
                        "exception.message": f"failed query {tag} {uid}",
                        "k8s.namespace.name": "proj-pn",
                        "k8s.container.name": "backend",
                        "k8s.pod.name": "pn-backend-0",
                    },
                    separators=(",", ":"),
                )
            )
        dup_res = produce("fluent-bit-gke", "logs.proj.valid", "\n".join(dups))
        self.assertEqual(dup_res.returncode, 0, dup_res.stdout + dup_res.stderr)
        time.sleep(2)
        dup_hits = [
            r for r in channel_requests_since("errors", after_first)
            if tag in json.dumps(r.get("payload", {}))
        ]
        self.assertEqual(dup_hits, [], f"expected dedup within 5s TTL, got {dup_hits}")

        remaining = max(0.0, 6.0 - (time.monotonic() - first_seen_at))
        time.sleep(remaining)
        after_ttl_start = len(requests_log())
        recur_ts = datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")
        recur = dict(first, time=recur_ts, observed_time=recur_ts)
        recur_res = produce("fluent-bit-gke", "logs.proj.valid", json.dumps(recur, separators=(",", ":")))
        self.assertEqual(recur_res.returncode, 0, recur_res.stdout + recur_res.stderr)
        recur_hits = harness.wait_for(
            lambda: [
                r for r in channel_requests_since("errors", after_ttl_start)
                if tag in json.dumps(r.get("payload", {}))
            ] or None,
            60,
            interval=1,
            what="post-TTL recurring alert",
        )
        self.assertEqual(len(recur_hits), 1, recur_hits)
        self.assertIn("Recurring error in proj-pn/backend", recur_hits[0]["payload"].get("title", ""))

    def test_3_delayed_out_of_order_log(self):
        tag = alpha_tag()
        start = len(requests_log())
        now = datetime.now(timezone.utc)
        newer = {
            "time": now.strftime("%Y-%m-%dT%H:%M:%SZ"),
            "observed_time": now.strftime("%Y-%m-%dT%H:%M:%SZ"),
            "level": "info",
            "msg": f"healthy request {tag}",
            "service.name": "backend",
            "service.version": "1.0.0",
            "k8s.namespace.name": "proj-pn",
            "k8s.container.name": "backend",
            "k8s.pod.name": "pn-backend-1",
        }
        past = (now - timedelta(hours=3)).strftime("%Y-%m-%dT%H:%M:%SZ")
        delayed = {
            "time": past,
            "observed_time": past,
            "level": "error",
            "msg": f"late error {tag}",
            "service.name": "backend",
            "service.version": "1.0.0",
            "exception.type": "DelayedError",
            "exception.message": f"late arrival {tag}",
            "k8s.namespace.name": "proj-pn",
            "k8s.container.name": "backend",
            "k8s.pod.name": "pn-backend-1",
        }
        batch = "\n".join(json.dumps(x, separators=(",", ":")) for x in (newer, delayed))
        res = produce("fluent-bit-gke", "logs.proj.valid", batch)
        self.assertEqual(res.returncode, 0, res.stdout + res.stderr)

        hits = harness.wait_for(
            lambda: [
                r for r in channel_requests_since("errors", start)
                if tag in json.dumps(r.get("payload", {}))
            ] or None,
            60,
            interval=1,
            what="delayed out-of-order alert",
        )
        self.assertEqual(len(hits), 1, hits)
        self.assertIn(f"late arrival {tag}", hits[0]["payload"].get("body", ""))

        loki_delayed = harness.wait_for(
            lambda: (
                lambda d: d if any(
                    tag in line
                    for stream in d.get("data", {}).get("result", [])
                    for _, line in stream.get("values", [])
                ) else None
            )(
                loki_query_range(
                    "proj",
                    '{topic="logs.proj.valid",k8s_namespace_name="proj-pn",level="error"}',
                    int((time.time() - 4 * 3600) * 1e9),
                    int((time.time() + 120) * 1e9),
                )
            ),
            90,
            interval=2,
            what="3h out-of-order Loki entry",
        )
        self.assertEqual(loki_delayed["status"], "success")

    def test_4_raw_crash_and_quarantine_redaction(self):
        crash_tag = alpha_tag()
        quar_tag = alpha_tag()
        secret_bearer = "supersecrettoken12345"
        secret_sk = "sk-test-0000000000000000"
        start = len(requests_log())
        now_iso = datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")

        crash_line = {
            "msg": f"panic: runtime crash {crash_tag} Bearer {secret_bearer}",
            "k8s.namespace.name": "proj-pn",
            "k8s.container.name": "worker",
            "k8s.pod.name": "pn-worker-0",
            "observed_time": now_iso,
        }
        quar_line = {
            "msg": f"invalid raw line {quar_tag} token={secret_sk}",
            "k8s.namespace.name": "proj-health",
            "k8s.container.name": "api",
            "k8s.pod.name": "health-api-0",
            "observed_time": now_iso,
        }

        res1 = produce("fluent-bit-gke", "logs.proj.valid", json.dumps(crash_line, separators=(",", ":")))
        self.assertEqual(res1.returncode, 0, res1.stdout + res1.stderr)
        res2 = produce("fluent-bit-gke", "logs.proj.quarantine", json.dumps(quar_line, separators=(",", ":")))
        self.assertEqual(res2.returncode, 0, res2.stdout + res2.stderr)

        crash_hits = harness.wait_for(
            lambda: [
                r for r in channel_requests_since("errors", start)
                if crash_tag in json.dumps(r.get("payload", {}))
            ] or None,
            60,
            interval=1,
            what="raw panic crash alert",
        )
        quar_hits = harness.wait_for(
            lambda: [
                r for r in channel_requests_since("errors", start)
                if quar_tag in json.dumps(r.get("payload", {}))
            ] or None,
            60,
            interval=1,
            what="quarantine alert",
        )

        crash_raw = json.dumps(crash_hits[0]["payload"])
        quar_raw = json.dumps(quar_hits[0]["payload"])
        self.assertIn("[REDACTED", crash_raw)
        self.assertNotIn(secret_bearer, crash_raw)
        self.assertIn("Quarantined log in proj-health/api", quar_hits[0]["payload"].get("title", ""))
        self.assertIn("[REDACTED", quar_raw)
        self.assertNotIn(secret_sk, quar_raw)

        outbox_records = consume("alerts.errors", 100, timeout_ms=5000)
        outbox_blob = json.dumps(outbox_records)
        self.assertNotIn(secret_bearer, outbox_blob)
        self.assertNotIn(secret_sk, outbox_blob)

    def test_5_loki_mtls_and_queries(self):
        with self.assertRaises((ssl.SSLError, urllib.error.URLError, OSError)):
            http_get(f"{LOKI_URL}/ready", ssl_ctx=loki_ssl_context(with_client_cert=False))

        status, ready_body = http_get(f"{LOKI_URL}/ready", ssl_ctx=loki_ssl_context(with_client_cert=True))
        self.assertEqual(status, 200)
        self.assertIn("ready", ready_body)

        metric_payload = {
            "schema": 1,
            "source": "vm/a1-2",
            "metric": "loki.disk.free_ratio",
            "value": 0.875,
            "unit": "ratio",
        }
        m_res = produce("ops-agent-a1-2", "ops.platform.metrics", json.dumps(metric_payload, separators=(",", ":")))
        self.assertEqual(m_res.returncode, 0, m_res.stdout + m_res.stderr)

        now_iso = datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")
        bogus_tag = alpha_tag()
        fatal_tag = alpha_tag()
        extra_lines = "\n".join([
            json.dumps({
                "time": now_iso,
                "observed_time": now_iso,
                "level": f"custom_attacker_{bogus_tag}",
                "msg": f"bogus level {bogus_tag}",
                "service.name": "backend",
                "service.version": "1.0.0",
                "k8s.namespace.name": "proj-pn",
                "k8s.container.name": "backend",
                "k8s.pod.name": "pn-backend-0",
            }, separators=(",", ":")),
            json.dumps({
                "msg": f"panic: runtime fatal {fatal_tag}",
                "k8s.namespace.name": "proj-pn",
                "k8s.container.name": "worker",
                "k8s.pod.name": "pn-worker-1",
                "observed_time": now_iso,
            }, separators=(",", ":")),
        ])
        e_res = produce("fluent-bit-gke", "logs.proj.valid", extra_lines)
        self.assertEqual(e_res.returncode, 0, e_res.stdout + e_res.stderr)

        end_ns = int((time.time() + 120) * 1e9)
        start_ns = self.suite_start_ns - int(300 * 1e9)

        proj_res = harness.wait_for(
            lambda: (
                lambda d: d if any(
                    self.structured_tag in line
                    for stream in d.get("data", {}).get("result", [])
                    for _, line in stream.get("values", [])
                ) else None
            )(loki_query_range("proj", '{topic="logs.proj.valid",k8s_namespace_name="proj-pn"}', start_ns, end_ns)),
            90,
            interval=2,
            what="Loki proj query",
        )
        self.assertEqual(proj_res["status"], "success")

        bogus_res = harness.wait_for(
            lambda: (
                lambda d: d if any(
                    bogus_tag in line
                    for stream in d.get("data", {}).get("result", [])
                    for _, line in stream.get("values", [])
                ) else None
            )(loki_query_range("proj", '{topic="logs.proj.valid",k8s_namespace_name="proj-pn",level="unknown"}', start_ns, end_ns)),
            90,
            interval=2,
            what="Loki allowlisted unknown level query",
        )
        self.assertEqual(bogus_res["status"], "success")

        fatal_res = harness.wait_for(
            lambda: (
                lambda d: d if any(
                    fatal_tag in line
                    for stream in d.get("data", {}).get("result", [])
                    for _, line in stream.get("values", [])
                ) else None
            )(loki_query_range("proj", '{topic="logs.proj.valid",k8s_namespace_name="proj-pn",level="fatal"}', start_ns, end_ns)),
            90,
            interval=2,
            what="Loki panic fatal level query",
        )
        self.assertEqual(fatal_res["status"], "success")

        ops_res = harness.wait_for(
            lambda: (
                lambda d: d if any(
                    float(val) == 0.875
                    for series in d.get("data", {}).get("result", [])
                    for _, val in series.get("values", [])
                ) else None
            )(
                loki_query_range(
                    "ops",
                    'avg_over_time({topic="ops.platform.metrics",source="vm/a1-2",metric="loki.disk.free_ratio"} | logfmt | unwrap value [5m])',
                    start_ns,
                    int((time.time() + 120) * 1e9),
                )
            ),
            90,
            interval=2,
            what="Loki ops metrics unwrap query",
        )
        self.assertEqual(ops_res["status"], "success")

    def test_6_obs_unit_isolation(self):
        hb = {
            "schema": 1,
            "kind": "agent.heartbeat",
            "state": "ok",
            "subject": "vm/a1-2",
            "vm": "a1-2",
            "observed_by": "a1-2",
            "source": "heartbeat",
        }
        hb_res = produce("ops-agent-a1-2", "ops.platform.events", json.dumps(hb, separators=(",", ":")))
        self.assertEqual(hb_res.returncode, 0, hb_res.stdout + hb_res.stderr)
        time.sleep(1)
        start = len(requests_log())
        events = [
            {
                "schema": 1,
                "kind": "unit.failed",
                "state": "down",
                "subject": "unit/a1-2/loki.service",
                "vm": "a1-2",
                "observed_by": "a1-2",
                "source": "systemd",
                "detail": "loki exited",
            },
            {
                "schema": 1,
                "kind": "loki.disk.low",
                "state": "warn",
                "subject": "loki/disk",
                "vm": "a1-2",
                "observed_by": "a1-2",
                "source": "sample",
                "detail": "free space on /var/lib/loki is below 15%",
            },
            {
                "schema": 1,
                "kind": "partition.isr_shrink",
                "state": "down",
                "subject": "kafka/partition/logs.proj.valid-0",
                "vm": "a1-1",
                "observed_by": "a1-1",
                "source": "kafka-broker-log",
                "detail": "ISR shrunk",
            },
        ]
        batch = "\n".join(json.dumps(ev, separators=(",", ":")) for ev in events)
        res = produce("ops-agent-a1-2", "ops.platform.events", batch)
        self.assertEqual(res.returncode, 0, res.stdout + res.stderr)

        def got_all():
            msgs = [r.get("payload", {}).get("title", "") for r in channel_requests_since("kafka", start)]
            want = (
                "[obs] DOWN  obs/a1-2/loki.service",
                "[obs] WARN  obs/a1-2/loki/disk",
                "[kafka] DOWN  kafka/partition/logs.proj.valid-0",
            )
            if all(any(w in m for m in msgs) for w in want):
                return msgs
            return None

        titles = harness.wait_for(got_all, 90, interval=2, what="isolated [obs] and [kafka] alerts")
        self.assertFalse(any("DOWN  vm/a1-2" in t for t in titles), titles)


if __name__ == "__main__":
    unittest.main()
