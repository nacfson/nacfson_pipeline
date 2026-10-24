/**
 * OpenCode Plugin — Git Remote Refresh at Session Start.
 *
 * Runs `scripts/universal-agent-hook.py --event SessionStart`, which performs a
 * non-mutating `git fetch` (throttled, fail-open). The child is detached
 * so a slow or offline remote never delays the session.
 */

import { spawn } from "node:child_process";
import * as path from "node:path";

const plugin = {
  id: "nacfson-git-fetch-session-start",
  server: async (input) => {
    const repoRoot = input?.directory || process.cwd();
    const hookPath = path.join(repoRoot, "scripts", "universal-agent-hook.py");

    const runFetch = () => {
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
      } catch {
        // Fail open.
      }
    };

    // Plugin load covers resumed sessions where `session.created` already fired.
    runFetch();

    return {
      event: async ({ event }) => {
        if (event?.type === "session.created") {
          runFetch();
        }
      },
    };
  },
};

export default plugin;
