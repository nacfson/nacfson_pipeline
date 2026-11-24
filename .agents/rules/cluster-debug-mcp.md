# Cluster Debugging: `nacfson-debug` MCP Server First

For any Kubernetes, FluxCD, pod, workload, event, or container log inspection, **always use the registered `nacfson-debug` MCP server first** (configured in [`.mcp.json`](../../.mcp.json) and [`opencode.json`](../../opencode.json)):

- `cluster_overview`
- `list_workloads`
- `describe_pod`
- `get_events`
- `get_flux_status`
- `get_pod_logs`

## Ephemeral Session Handling

- `/usr/local/bin/nacfson-mcp --connect /run/nacfson-mcp/mcp.sock` is a lazy-dial stdio bridge: tool discovery (`tools/list`) works even while `nacfson-mcp.service` is stopped.
- If a `tools/call` reports that the daemon is stopped, inspect `./scripts/mcp/mcp-session.sh status` and ask the operator to start a temporary session:
  ```bash
  sudo ./scripts/mcp/mcp-session.sh start <gke|local-k3s> --ttl 1h
  ```
  Then retry the `nacfson-debug` MCP tool (see [`.agents/skills/debug-mcp-session/SKILL.md`](../skills/debug-mcp-session/SKILL.md)).

## No Agent CLI Fallback

- **Never** fall back to `kubectl`, `k3s`, `oc`, `flux`, `helm`, `gcloud`, `gsutil`, `aws`, `az`, `oci`, `ssh`, `sudo`, mutating `terraform`/`tofu`, or inline `ALLOW_CLUSTER_CLI=1` in agent shell commands.
- `PreToolUse` hooks block direct cluster/cloud-console CLIs and inline `ALLOW_CLUSTER_CLI=1` self-bypasses (`ALLOW_CLUSTER_CLI=1` in a host process environment is strictly a human-operator break-glass control).
