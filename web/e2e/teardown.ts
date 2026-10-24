// Global teardown: stop the throwaway hub and restore the committed
// placeholder. The placeholder restore keys off the restore file written
// before the build, not off the hub state file, so even a setup failure
// that never started a hub leaves the worktree clean.
import fs from "node:fs";
import path from "node:path";
import { E2E_DIR, RESTORE_FILE, STATE_FILE, URL_FILE } from "./helpers.ts";

const distDir = path.join(E2E_DIR, "..", "dist");

export default async function globalTeardown(): Promise<void> {
  if (fs.existsSync(STATE_FILE)) {
    const state = JSON.parse(fs.readFileSync(STATE_FILE, "utf8")) as { pid: number };
    try {
      process.kill(state.pid, "SIGKILL");
      console.log(`e2e teardown: killed fleetd pid=${state.pid}`);
    } catch {
      // Already gone (for example a setup failure cleaned it up).
    }
  }
  if (fs.existsSync(RESTORE_FILE)) {
    const { tmpDir } = JSON.parse(fs.readFileSync(RESTORE_FILE, "utf8")) as { tmpDir: string };
    const backup = path.join(tmpDir, "placeholder-index.html");
    if (fs.existsSync(backup)) {
      fs.copyFileSync(backup, path.join(distDir, "index.html"));
      console.log("e2e teardown: restored dist/index.html placeholder");
      fs.rmSync(path.join(distDir, "assets"), { recursive: true, force: true });
    } else {
      console.log("e2e teardown: no stash found, dist/index.html left as built");
    }
    fs.rmSync(tmpDir, { recursive: true, force: true });
    fs.rmSync(RESTORE_FILE, { force: true });
  }
  fs.rmSync(STATE_FILE, { force: true });
  fs.rmSync(URL_FILE, { force: true });
}
