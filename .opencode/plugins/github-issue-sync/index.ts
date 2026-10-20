import { Plugin } from "@opencode/plugin"
import { spawn } from "node:child_process"

export default Plugin.define({
  id: "nacfson-github-issue-sync",
  setup(ctx) {
    const cwd = ctx.location.project.canonical
    try {
      const child = spawn("python3", ["scripts/sync-github-issues.py"], { cwd, stdio: "ignore" })
      child.on("error", () => {})
      child.on("exit", () => {})
    } catch {
      // spawn can throw before the child exists; setup must not
    }
  },
})
