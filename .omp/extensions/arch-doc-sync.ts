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
  const reviewCyclePath = path.join(repoRoot, "scripts", "review-cycle.py");

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
    } catch (err: any) {
      const stderr = err?.stderr ? String(err.stderr).trim() : String(err);
      return {
        block: true,
        reason: `[ARCH-DOC-SYNC BLOCKED] Update docs/platform-architecture.html before committing:\n${stderr}`,
      };
    }

    if (process.env.SKIP_REVIEW_GATE !== "1" && !cmd.includes("SKIP_REVIEW_GATE=1")) {
      try {
        execFileSync(
          "python3",
          [reviewCyclePath, "check-gate", "--scope", "staged", "--require-pipeline", "code-review"],
          {
            cwd: repoRoot,
            stdio: "pipe",
          }
        );
      } catch (err: unknown) {
        const stderr =
          err && typeof err === "object" && "stderr" in err && err.stderr
            ? String(err.stderr).trim()
            : String(err);
        return {
          block: true,
          reason: `[REVIEW-CYCLE BLOCKED] Run the code-review skill (.agents/skills/code-review/SKILL.md) before committing. Do not self-record a receipt:\n${stderr}`,
        };
      }
    }
    return undefined;
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
