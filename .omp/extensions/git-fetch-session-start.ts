/**
 * Oh-my-pi (omp / pi-coding-agent) Extension — Git Remote Refresh at Session Start.
 *
 * Runs `scripts/universal-agent-hook.py --event SessionStart`, which performs a
 * non-mutating `git fetch` (throttled, fail-open) so the agent starts
 * with current remote-tracking refs. The child is detached so a slow or offline
 * remote never delays the session.
 */

import { spawn } from "node:child_process";
import * as path from "node:path";

export default function gitFetchSessionStart(pi: any): void {
  const repoRoot = process.cwd();
  const hookPath = path.join(repoRoot, "scripts", "universal-agent-hook.py");

  pi.on("session_start", async (_event: any, ctx: any) => {
    try {
      const child = spawn(
        "python3",
        [hookPath, "--agent", "generic", "--event", "SessionStart"],
        {
          cwd: repoRoot,
          detached: true,
          stdio: "ignore",
        }
      );
      child.on("error", () => {
        // Fail open: a missing python3 or offline remote must not block the session.
      });
      child.unref();
    } catch (err: any) {
      if (ctx?.ui?.notify) {
        ctx.ui.notify(`[GIT-FETCH] skipped: ${String(err)}`, "warning");
      }
    }
  });
}
