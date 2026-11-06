# Contract: Collector Pipeline and Stored Record

**Owner**: platform (`deploy/platform/observability/`). **Data shapes**: [data-model.md §3–§4](../data-model.md#3-stored-record-platform-owned). **Decisions**: [research.md R1–R6, R11–R12](../research.md).

## Ordered stages

| # | Stage | Fluent Bit element | Guarantees |
|---|---|---|---|
| 1 | Tail `proj-*` files | `tail`: `Path /var/log/pods/proj-*_*/*/*.log`, `Tag proj.*`, `multiline.parser cri`, `DB /state/tail.db` | Joins `P…F` partial lines (FR-013). Each file has its own tag. The bookmark survives restarts. |
| 2 | Group crashes | `multiline` filter, **first filter**: `multiline.key_content log`, parsers `go, python, java`, `buffer on` | A crash and its trace become one record (FR-018b). |
| 3 | Path fields | `parser` filter on the file path → temporary keys | namespace, pod, uid, container |
| 4 | JSON parse | `parser` filter `json` on `log`, `Reserve_Data On`, `Preserve_Key On` | Lossless field parse. The original text stays in `log` for the quarantine path. A non-JSON line keeps only `log` (FR-016). |
| 5 | Contract stage | `lua` filter `log_contract.lua` (`call process`) | Decides "refined or not" (FR-018a), then:<br>- **main:** strips reserved keys, then sets platform fields (FR-011/012); falls back to `msg`; chooses the main time (FR-015); redacts (FR-017); cuts `exception.stacktrace`, or a grouped crash's message, to 8 KiB with a `…[truncated N bytes]` marker (FR-018c); deletes `log`.<br>- **quarantine:** rebuilds the record as only `msg` = redacted original text, plus platform fields, `log.iostream`, and `observed_time`. Drops every parsed key and keeps the CRI timestamp. |
| 6 | Route | `rewrite_tag` on `$_route ^quarantine$` → `quarantine.$TAG`, `keep false` | Quarantine records leave the main tag. They are re-emitted at the head of the pipeline as `quarantine.*`. |
| 7 | Clean | `modify Remove _route`, **`Match *`** (the only filter that also sees `quarantine.*`) | Internal key never stored, on both paths. |

**Match rule (prevents losing source labels):**

- Stages 2–6 MUST use `Match proj.*`.
- Only stage 7 uses `Match *`.

Records re-emitted by `rewrite_tag` as `quarantine.*` therefore skip the Lua stage. If they ran through it a second time, it would delete their `k8s.*` labels as "project-supplied", and the temporary path keys needed to rebuild them would already be gone.
| 8a | Output, main | `http`, `Match proj.*`, headers `AccountID 0`, `ProjectID 0` | |
| 8b | Output, quarantine | `http`, `Match quarantine.*`, headers `AccountID 0`, `ProjectID 1` | |

Both outputs:

- URI: `/insert/jsonline?_stream_fields=k8s.namespace.name,k8s.pod.name,k8s.container.name&_msg_field=msg&_time_field=_time`
- `Format json_lines`, `json_date_key _time`, `json_date_format iso8601`, `Retry_Limit 5`.

## Invariants (enforced by manifest tests)

| Rule | Checked by |
|---|---|
| The `multiline` filter is the first `[FILTER]` | `tests/observability` |
| The Lua stage comes after the JSON parse and before `rewrite_tag` | `tests/observability` |
| Every `[FILTER]` except the final `modify Remove _route` uses `Match proj.*`, and no filter before `rewrite_tag` matches `quarantine.*` or `*` | `tests/observability` |
| A quarantined record holds only `msg`, the five `k8s.*` fields, `log.iostream`, and `observed_time`, and its `msg` equals the redacted original text | Lua unit test |
| Inside `log_contract.lua`, the reserved keys are removed before the platform fields are set | Lua unit test with forged keys |
| The reserved and required key lists in Lua match `CONTRACT_LOG_SCHEMA.md` | `tests/observability/test_log_contract_schema.py` |
| The Lua redaction gives the same results as the shared vectors | Lua unit test (`luajit`) |
| A stack trace over 8 KiB is cut at a line break inside the limit, keeps its top, and ends with `…[truncated N bytes]` where N is correct. A trace of exactly 8 KiB is unchanged. | Lua unit test |
| `legacy-containers.txt` equals the set of containers declared `v2/legacy` | preflight `check_log_contract` |
| Exactly one read-only `hostPath` (`/var/log/pods`), no privileged container | existing preflight and tests (Clarification 2.2.1) |
| No `grep` or `throttle` filter, and no output with `Match *` that discards records | `tests/observability` (FR-018: never drop) |
| Resources stay `50m` / `64Mi`, with requests equal to limits | `tests/observability`; any change needs a measurement (research R12) |

## Readers

| Reader | How it reads |
|---|---|
| Operator UI (vmui) | Main area by default. Quarantine through the tenant headers (`ProjectID: 1`) or `curl`/`vlogscli`. |
| Error detector (workstream 1) | Main area for errors. Quarantine area for violation counts. |
| `scripts/open-victorialogs-ui.sh --verify` | Checks that every returned record has `k8s.namespace.name` starting with `proj-`. |

Example queries:

```
{k8s.namespace.name="proj-pn"} level:=error
trace_id:=4bf92f3577b34da6a3ce929d0e0e4736
level:=error | stats by (service.version, exception.type) count()
_msg:"[REDACTED:"
```
