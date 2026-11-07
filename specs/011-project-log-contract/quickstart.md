# Quickstart: Validate Project Log Contract v3

These are validation scenarios only. The implementation is in `tasks.md`. Shapes and rules are in [data-model.md](data-model.md) and [contracts/](contracts/).

## Prerequisites

- Go 1.22, Python 3 with PyYAML, `luajit`, Docker, `kubectl kustomize`, `flux`.
- The images used in scenario 3: `fluent/fluent-bit:4.2.8` and `victoriametrics/victoria-logs:v1.53.0`. Both are pinned already.
- No cluster access is needed for scenarios 1–4.

## 1. Unit and contract tests (repository)

```bash
(cd services/error-pipeline && go test ./... && go vet ./...)
python3 -m unittest discover tests/observability
python3 -m unittest discover tests/preflight
luajit tests/observability/lua/run_log_contract_tests.lua
```

Expected: all of these pass, including:

- the contract-drift test: the contract tables in `CONTRACT_LOG_SCHEMA.md` and the Lua key lists agree;
- the redaction-parity test: Go and Lua both match `services/error-pipeline/internal/redact/testdata/vectors.json`;
- the filter-order test.

## 2. Manifest gates

```bash
./scripts/render-validate.sh local-k3s
./scripts/render-validate.sh vps-k3s
```

Expected: both pass.

Then check that each fixture under `tests/preflight/fixtures/log_contract/` fails with its rule ID:

| Fixture | Expected rule |
|---|---|
| `missing-annotation` | `log-declaration-missing` |
| `log-file-env` | `log-file-output` |
| `undeclared-log-volume` | `log-volume-undeclared` |
| `v0-plain` | `log-version-label` (plain `v0.0.0` rejected in all clusters) |
| `legacy-list-drift` | `log-legacy-list-drift` |
| `done-regressed` | `log-migration-regressed` |

PN's declared learning-log path (`/var/log/project-pn`) passes.

## 3. Collector end-to-end (local Docker, no cluster)

1. Start VictoriaLogs locally with the production flags.
2. Start Fluent Bit with the repository's ConfigMap files mounted. Point the `tail` `Path` at a fixture directory laid out like `/var/log/pods/proj-test_app-0_<uid>/<container>/0.log`, and point the outputs at the local VictoriaLogs.
3. Copy `tests/observability/fixtures/cri/*.log` into that directory. The fixtures cover:
   - a conforming JSON line;
   - a 40 KB JSON line split into `P`/`F` pieces;
   - a Go panic with its trace;
   - a line forging `k8s.namespace.name`;
   - a text line from a v3 container;
   - a JSON line missing `service.version`;
   - a line with `Bearer eyJ…`;
   - a text line from a container listed in `legacy-containers.txt`.

Query the stores (after `POST /internal/force_flush`) and check:

| Query | Expected |
|---|---|
| `curl …/select/logsql/query -d 'query=*'` (tenant 0:0) | conforming, split-then-joined, panic (one record), forged (with the real namespace), and legacy-text records; secret shown as `[REDACTED:bearer]` |
| the same with `-H 'ProjectID: 1'` | exactly the text line and the line missing `service.version` |
| `query=k8s.namespace.name:proj-test` count | equals the number of fixture records (nothing lost) |
| `query=_msg:"eyJ"` in both tenants | 0 results |
| `_time` of the conforming line | equals its printed `time`; `observed_time` holds the CRI timestamp |

This scenario proves SC-002, SC-003, SC-004, SC-004a, and the storage half of SC-008.

## 4. Collector load (SC-009)

Run the scenario 3 setup with Docker limits `--cpus 0.05 --memory 64m`. Feed:

- 100 lines/s for 5 minutes;
- then 1,000 lines/s for 10 s.

The input mixes JSON, crash, and secret lines.

Expected:

- no OOM kill;
- the record count in VictoriaLogs equals the lines written (counting a grouped crash as one record);
- the Fluent Bit log has no repeated `[error]` lines.

## 5. Image-pin gate

```bash
python3 -m unittest discover -s tests/skills
```

Expected: the helper tests cover:

- a missing or malformed version label (blocked);
- a plain `v0.0.0` with no commit (blocked in both clusters);
- a missing or failed `log-contract` check run (blocked; `gh` is mocked);
- a passing release or untagged case (`v0.0.0-<commit>`) that also writes `app.kubernetes.io/version`.

## 6. After deployment (operator, read-only)

Run `./scripts/open-observe.sh` (an SSH helper, which needs explicit allowance), then:

| Check | Expected |
|---|---|
| `{k8s.namespace.name="proj-pn"}` in vmui | records show `k8s.*`, `log.iostream`, `observed_time`, and parsed fields for migrated containers |
| `./scripts/open-victorialogs-ui.sh --verify` | passes with the v3 field check |
| one failing PN request, then `trace_id:=<id>` | frontend and backend lines of that request only (SC-001) |
