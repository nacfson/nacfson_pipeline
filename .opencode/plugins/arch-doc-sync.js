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
    const reviewCyclePath = path.join(repoRoot, "scripts", "review-cycle.py");

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
          } catch (err) {
            const stderr = err?.stderr ? String(err.stderr).trim() : String(err);
            throw new Error(
              `[REVIEW-CYCLE BLOCKED] Run the code-review skill (.agents/skills/code-review/SKILL.md) before committing. Do not self-record a receipt:\n${stderr}`
            );
          }
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
