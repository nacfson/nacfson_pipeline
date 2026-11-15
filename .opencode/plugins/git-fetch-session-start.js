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
        });
        child.unref();
      } catch {
      }
    };

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
