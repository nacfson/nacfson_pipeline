import json
import re
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parents[2]
LUA = REPO_ROOT / "infra" / "oci" / "ansible" / "roles" / "ops_agent" / "files" / "ops_events.lua"
KAFKA_LOGS = REPO_ROOT / "tests" / "fixtures" / "kafka-logs" / "4.3.2"
SYSTEMD = REPO_ROOT / "tests" / "fixtures" / "systemd" / "unit-lifecycle.systemd-257.jsonl"
EXPECTED = REPO_ROOT / "tests" / "fixtures" / "ops-events" / "expected"
FLUENT_BIT_IMAGE = "docker.io/fluent/fluent-bit:4.2.8@sha256:6806769c10acf11b41d14083b3101a24433b4ca113a1ab7a6b1a1b7cad86a7e8"
ENV = {
    "OPS_VM": "a1-1",
    "OPS_NODES": "1=a1-1,2=a1-2,3=micro-1,4=micro-2",
    "OPS_REPLICATION": "3",
    "OPS_UNITS": "ops-fixture-unit.service,ops-fixture-stop.service",
}
UNIT_STARTED = "39f53479d3a045ac8e11786248231fbf"
UNIT_LEFT = {"d9b373ed55a64feb8242e02dbe79a49c", "9d1aaa27d60140bd96365438aad20286"}
NODES = dict(pair.split("=") for pair in ENV["OPS_NODES"].split(","))


def contract_event(kind, state, subject, vm, source, detail):
    return {"schema": 1, "kind": kind, "state": state, "subject": subject, "vm": vm,
            "observed_by": ENV["OPS_VM"], "source": source, "detail": detail[:1024]}


def spec_kafka_events(lines):
    events = []
    for line in lines:
        m = re.search(r"Fencing broker (\d+) at epoch|The request from broker (\d+) to shut down has been granted"
                      r"|Replayed BrokerRegistrationChangeRecord modifying the registration for broker (\d+):.*fenced=1,", line)
        if m:
            b = m.group(1) or m.group(2) or m.group(3)
            events.append(contract_event("broker.fenced", "down", f"kafka/broker-{b}", NODES[b], "kafka-controller-log", line))
            continue
        m = re.search(r"The request from broker (\d+) to unfence has been granted"
                      r"|Replayed BrokerRegistrationChangeRecord modifying the registration for broker (\d+):.*fenced=-1,", line)
        if m:
            b = m.group(1) or m.group(2)
            events.append(contract_event("broker.unfenced", "up", f"kafka/broker-{b}", NODES[b], "kafka-controller-log", line))
            continue
        m = re.search(r"\[Partition (\S+)-(\d+) broker=\d+\] (?:Shrinking ISR from [\d,]+ to|ISR updated to) ([\d,]+)", line)
        if m and not m.group(1).startswith("__"):
            subject = f"kafka/partition/{m.group(1)}-{m.group(2)}"
            full = len(m.group(3).split(",")) >= int(ENV["OPS_REPLICATION"])
            kind, state = ("partition.isr_expand", "up") if full else ("partition.isr_shrink", "down")
            events.append(contract_event(kind, state, subject, ENV["OPS_VM"], "kafka-broker-log", line))
    return events


def spec_unit_events(records):
    events = []
    watched = set(ENV["OPS_UNITS"].split(","))
    for r in records:
        unit = r.get("UNIT") or r.get("USER_UNIT")
        if unit not in watched:
            continue
        subject = f"unit/{ENV['OPS_VM']}/{unit}"
        if r.get("MESSAGE_ID") == UNIT_STARTED:
            events.append(contract_event("unit.active", "up", subject, ENV["OPS_VM"], "systemd", r["MESSAGE"]))
        elif r.get("MESSAGE_ID") in UNIT_LEFT:
            events.append(contract_event("unit.failed", "down", subject, ENV["OPS_VM"], "systemd", r["MESSAGE"]))
    return events


def spec_for(fixture: Path):
    if fixture.suffix == ".jsonl":
        return spec_unit_events([json.loads(line) for line in fixture.read_text(encoding="utf-8").splitlines() if line.strip()])
    return spec_kafka_events(fixture.read_text(encoding="utf-8").splitlines())


def fixtures():
    return sorted(KAFKA_LOGS.glob("*.log")) + [SYSTEMD]


def expected_path(fixture: Path) -> Path:
    return EXPECTED / f"{fixture.name}.json"


def run_fluent_bit(fixture: Path, env_override: dict[str, str] | None = None) -> list[dict]:
    is_units = fixture.suffix == ".jsonl"
    conf = f"""[SERVICE]
    Flush 1
    Log_Level warn
    Parsers_File /fluent-bit/etc/parsers.conf

[INPUT]
    Name tail
    Path /fixture/{fixture.name}
    Read_from_Head On
    Exit_On_Eof On
    Tag {"unit" if is_units else "kafka"}
    {"Parser json" if is_units else "Key MESSAGE"}

[FILTER]
    Name lua
    Match kafka
    script /lua/ops_events.lua
    call kafka_line

[FILTER]
    Name lua
    Match unit
    script /lua/ops_events.lua
    call unit_record

[OUTPUT]
    Name stdout
    Match *
    Format json_lines
"""
    with tempfile.TemporaryDirectory() as tmp:
        (Path(tmp) / "fluent-bit.conf").write_text(conf, encoding="utf-8")
        (Path(tmp) / "parsers.conf").write_text("[PARSER]\n    Name json\n    Format json\n", encoding="utf-8")
        run_env = {**ENV, **(env_override or {})}
        env = [arg for k, v in run_env.items() for arg in ("-e", f"{k}={v}")]
        out = subprocess.run(
            ["podman", "run", "--rm", *env,
             "-v", f"{tmp}:/fluent-bit/etc:ro,Z", "-v", f"{LUA.parent}:/lua:ro,Z", "-v", f"{fixture.parent}:/fixture:ro,Z",
             FLUENT_BIT_IMAGE, "/fluent-bit/bin/fluent-bit", "-c", "/fluent-bit/etc/fluent-bit.conf"],
            capture_output=True, text=True, check=True, timeout=120)
    events = []
    for line in out.stdout.splitlines():
        if line.startswith("{"):
            record = json.loads(line)
            record.pop("date", None)
            events.append(record)
    return events


class ExpectedFilesFollowTheContract(unittest.TestCase):
    def test_expected_files_match_the_contract_table(self):
        for fixture in fixtures():
            with self.subTest(fixture=fixture.name):
                expected = json.loads(expected_path(fixture).read_text(encoding="utf-8"))
                self.assertEqual(expected, spec_for(fixture))


class LuaRulesOnCapturedLines(unittest.TestCase):
    def test_lua_emits_exactly_the_expected_events(self):
        for fixture in fixtures():
            with self.subTest(fixture=fixture.name):
                expected = json.loads(expected_path(fixture).read_text(encoding="utf-8"))
                self.assertEqual(run_fluent_bit(fixture), expected)

    def test_lua_accepts_a12_observability_units_and_drops_grafana_and_unlisted(self):
        records = [
            {"UNIT": "vector.service", "MESSAGE_ID": UNIT_STARTED, "MESSAGE": "Started Vector."},
            {"UNIT": "loki.service", "MESSAGE_ID": "d9b373ed55a64feb8242e02dbe79a49c", "MESSAGE": "loki.service: Failed."},
            {"UNIT": "error-detector.service", "MESSAGE_ID": "9d1aaa27d60140bd96365438aad20286", "MESSAGE": "Stopped error-detector."},
            {"UNIT": "grafana.service", "MESSAGE_ID": "d9b373ed55a64feb8242e02dbe79a49c", "MESSAGE": "grafana.service: Failed."},
            {"UNIT": "ssh.service", "MESSAGE_ID": "d9b373ed55a64feb8242e02dbe79a49c", "MESSAGE": "ssh.service: Failed."},
        ]
        with tempfile.TemporaryDirectory() as tmp:
            fixture = Path(tmp) / "a12-units.jsonl"
            fixture.write_text("".join(json.dumps(r) + "\n" for r in records), encoding="utf-8")
            got = run_fluent_bit(
                fixture,
                {
                    "OPS_VM": "a1-2",
                    "OPS_UNITS": "kafka.service,wg-quick@wg0.service,ops-agent.service,vector.service,loki.service,error-detector.service",
                },
            )
        self.assertEqual(
            got,
            [
                {
                    "schema": 1,
                    "kind": "unit.active",
                    "state": "up",
                    "subject": "unit/a1-2/vector.service",
                    "vm": "a1-2",
                    "observed_by": "a1-2",
                    "source": "systemd",
                    "detail": "Started Vector.",
                },
                {
                    "schema": 1,
                    "kind": "unit.failed",
                    "state": "down",
                    "subject": "unit/a1-2/loki.service",
                    "vm": "a1-2",
                    "observed_by": "a1-2",
                    "source": "systemd",
                    "detail": "loki.service: Failed.",
                },
                {
                    "schema": 1,
                    "kind": "unit.failed",
                    "state": "down",
                    "subject": "unit/a1-2/error-detector.service",
                    "vm": "a1-2",
                    "observed_by": "a1-2",
                    "source": "systemd",
                    "detail": "Stopped error-detector.",
                },
            ],
        )


def write_expected() -> None:
    EXPECTED.mkdir(parents=True, exist_ok=True)
    for fixture in fixtures():
        expected_path(fixture).write_text(json.dumps(spec_for(fixture), indent=2, sort_keys=True) + "\n", encoding="utf-8")
        print(f"{expected_path(fixture).name}: {len(spec_for(fixture))} events")


if __name__ == "__main__":
    if sys.argv[1:] == ["--write-expected"]:
        write_expected()
    else:
        unittest.main()
