import { spawn } from "node:child_process";
import * as path from "node:path";

export default function gitFetchSessionStart(pi: any): void {
  const repoRoot = process.cwd();
  const hookPath = path.join(repoRoot, "scripts", "hooks", "universal-agent-hook.py");

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
      });
      child.unref();
    } catch (err: any) {
      if (ctx?.ui?.notify) {
        ctx.ui.notify(`[GIT-FETCH] skipped: ${String(err)}`, "warning");
      }
    }
  });
}
