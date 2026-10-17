/**
 * Oh-my-pi (omp / pi-coding-agent) Extension — Architecture Handbook Sync Gate.
 *
 * Enforces that `docs/platform-architecture.html` is updated whenever
 * architecture-affecting files are modified before `git commit` or turn completion.
 */

import { execFileSync } from "node:child_process";
import * as path from "node:path";

export default function archDocSyncExtension(pi: any): void {
  const repoRoot = process.cwd();
  const checkerPath = path.join(repoRoot, "scripts", "check-arch-doc-sync.py");

  // 1. Intercept bash/shell tool calls that run `git commit`
  pi.on("tool_call", async (event: any) => {
    const toolName = String(event?.toolName || "");
    if (toolName !== "bash" && toolName !== "run_command" && toolName !== "shell") {
      return undefined;
    }
    const cmd = String(event?.input?.command || event?.input?.CommandLine || "");
    if (!cmd.includes("git commit")) {
      return undefined;
    }
    try {
      execFileSync("python3", [checkerPath, "--staged"], {
        cwd: repoRoot,
        stdio: "pipe",
      });
      return undefined;
    } catch (err: any) {
      const stderr = err?.stderr ? String(err.stderr).trim() : String(err);
      return {
        block: true,
        reason: `[ARCH-DOC-SYNC BLOCKED] Update docs/platform-architecture.html before committing:\n${stderr}`,
      };
    }
  });

  // 2. Verify working tree sync when agent finishes its turn
  pi.on("agent_end", async (_event: any, ctx: any) => {
    try {
      execFileSync("python3", [checkerPath, "--worktree"], {
        cwd: repoRoot,
        stdio: "pipe",
      });
    } catch (err: any) {
      const stderr = err?.stderr ? String(err.stderr).trim() : String(err);
      if (ctx?.ui?.notify) {
        ctx.ui.notify(
          `[ARCH-DOC-SYNC] docs/platform-architecture.html must be updated: ${stderr}`,
          "warning"
        );
      }
    }
  });
}
