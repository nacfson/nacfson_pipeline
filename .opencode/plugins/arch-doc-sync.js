/**
 * OpenCode Plugin — Architecture Handbook Synchronization Gate.
 *
 * Hooks into OpenCode tool execution and session idle events to enforce that
 * `docs/platform-architecture.html` is updated whenever architecture-affecting
 * files change.
 */

import { execFileSync } from "node:child_process";
import * as path from "node:path";

const plugin = {
  id: "nacfson-arch-doc-sync",
  server: async (input) => {
    const repoRoot = input?.directory || process.cwd();
    const checkerPath = path.join(repoRoot, "scripts", "check-arch-doc-sync.py");

    return {
      "tool.execute.before": async (event, output) => {
        const toolName = String(event?.tool || "");
        if (toolName !== "bash" && toolName !== "shell") return;
        const cmd = String(output?.args?.command || "");
        if (!cmd.includes("git commit")) return;

        try {
          execFileSync("python3", [checkerPath, "--staged"], {
            cwd: repoRoot,
            stdio: "pipe",
          });
        } catch (err) {
          const stderr = err?.stderr ? String(err.stderr).trim() : String(err);
          throw new Error(
            `[ARCH-DOC-SYNC BLOCKED] Update docs/platform-architecture.html before committing:\n${stderr}`
          );
        }
      },
      event: async ({ event }) => {
        if (event?.type === "session.idle") {
          try {
            execFileSync("python3", [checkerPath, "--worktree"], {
              cwd: repoRoot,
              stdio: "pipe",
            });
          } catch (err) {
            const stderr = err?.stderr ? String(err.stderr).trim() : String(err);
            console.warn(`[ARCH-DOC-SYNC WARNING] ${stderr}`);
          }
        }
      },
    };
  },
};

export default plugin;
