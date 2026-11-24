---
name: debug-mcp-session
description: Start, verify, use, and stop a temporary OS-isolated read-only Kubernetes Debug MCP daemon session without keeping nacfson-mcp.service running 24/7.
compatibility: Works on Linux workstations with systemd, Go 1.22+, and T3 Code / Antigravity / Claude Code / Codex / OpenCode.
---

## User Input
```text
$ARGUMENTS
```

## Purpose
Operate the `1B` OS-isolated read-only Kubernetes Debug MCP broker ([`services/debug-mcp/`](services/debug-mcp/)) as an **ephemeral, auto-expiring session** via [`scripts/mcp/mcp-session.sh`](scripts/mcp/mcp-session.sh). Always use the `nacfson-debug` MCP server tools first ([`.mcp.json`](.mcp.json), [`opencode.json`](opencode.json)) — never fall back to `kubectl` or cloud-console CLIs. The systemd daemon defaults to **disabled at boot**, shuts down automatically when `RuntimeMaxSec` expires (default `2h`, max `24h`), and deletes `/etc/nacfson-mcp/kubeconfig` via `ExecStopPost` whenever stopped.

## Workflow

1. **Call `nacfson-debug` MCP Server Tools First**:
   - Available read-only MCP tools: `cluster_overview`, `list_workloads`, `describe_pod`, `get_events`, `get_flux_status`, `get_pod_logs`.
   - `/usr/local/bin/nacfson-mcp --connect /run/nacfson-mcp/mcp.sock` uses a lazy-dial stdio bridge so tool registration works even when the daemon is stopped; calling a tool while stopped returns an actionable prompt to start a temporary session instead of requiring CLI fallback.

2. **Check Current Daemon Status (Unprivileged)**:
   ```bash
   ./scripts/mcp/mcp-session.sh status
   ```

3. **One-Time Workstation Install (If Binary or Unit Is Missing)**:
   ```bash
   sudo ./scripts/mcp/install-daemon.sh
   ```
   - Builds `/usr/local/bin/nacfson-mcp`, creates `_nacfson-mcp` / `nacfson-mcp`, adds the operator user to `nacfson-mcp`, installs [`infra/workstation/nacfson-mcp.service`](infra/workstation/nacfson-mcp.service), and leaves the service **disabled** on boot (never pass `--enable` unless the user explicitly asks for 24/7 persistence).

4. **Start a Temporary Session When Prompted by Stopped Daemon**:
   ```bash
   sudo ./scripts/mcp/mcp-session.sh start <gke|local-k3s|ssh-host> --ttl 2h
   ```
   - Mints a short-lived token (default `governance/operator-readonly`, override with `--service-account ai-readonly` once GitHub issue #190 lands) matching `--ttl` (valid range `10m..24h`), verifies `auth can-i get secrets -A != yes` before promoting `0400 /etc/nacfson-mcp/kubeconfig`, applies `RuntimeMaxSec=<ttl>`, and starts `nacfson-mcp.service`.
   - Once active, retry the `nacfson-debug` MCP tool (or verify directly with `--connect` if testing from a terminal with `nacfson-mcp` group membership):
     ```bash
     printf '{"jsonrpc":"2.0","id":1,"method":"tools/list"}\n{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"cluster_overview","arguments":{}}}\n' | /usr/local/bin/nacfson-mcp --connect /run/nacfson-mcp/mcp.sock
     ```

5. **Stop Early When Debugging Is Done**:
   ```bash
   sudo ./scripts/mcp/mcp-session.sh stop
   ```
   - Stops `nacfson-mcp.service`, removes `/etc/nacfson-mcp/kubeconfig`, and clears the transient `RuntimeMaxSec` drop-in.

## Security & Privilege Rules

- **No CLI fallback & no inline bypass**: `PreToolUse` hooks block direct cluster/cloud-console CLIs and inline `ALLOW_CLUSTER_CLI=1` in agent shells. Host-environment `ALLOW_CLUSTER_CLI=1` is strictly an operator-side break-glass override, never an agent debugging path.
- Never ask for or echo `sudo` passwords in chat. In T3 Code, if the operator explicitly asks the agent to execute `install-daemon.sh` or `mcp-session.sh start|stop`, request the password via `request_secret` (`delivery: "file"`).
- Never enable `nacfson-mcp.service` for 24/7 background persistence unless the user explicitly requests it.
- Keep `cat /etc/nacfson-mcp/kubeconfig` returning `Permission denied` for the unprivileged user at all times.
