import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

import { STATE_FILE, URL_FILE } from "./helpers.ts";

const webDir = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const distIndex = path.join(webDir, "dist", "index.html");

function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

async function waitExit(pid: number): Promise<void> {
  const deadline = Date.now() + 10_000;
  while (Date.now() < deadline) {
    try {
      process.kill(pid, 0);
    } catch {
      return;
    }
    await sleep(100);
  }
  try {
    process.kill(pid, "SIGKILL");
  } catch {
    // Gone between the checks.
  }
}

// Global teardown: stop the smoke hub, restore the committed dist
// placeholder the setup stashed, and remove the throwaway folder. It runs
// even when the specs fail, so a built page never leaks into git.
export default async function globalTeardown(): Promise<void> {
  if (fs.existsSync(STATE_FILE)) {
    const raw = fs.readFileSync(STATE_FILE, "utf8");
    const state = JSON.parse(raw) as { url: string; pid: number; tmpDir: string };
    try {
      process.kill(state.pid, "SIGTERM");
      await waitExit(state.pid);
    } catch {
      // The hub already exited.
    }
    const stashed = path.join(state.tmpDir, "placeholder-index.html");
    if (fs.existsSync(stashed)) {
      fs.copyFileSync(stashed, distIndex);
      console.log(
        `e2e teardown: restored dist/index.html (${fs.statSync(distIndex).size} bytes)`,
      );
    } else {
      console.log("e2e teardown: no stash found, dist/index.html left as built");
    }
    // The build above is smoke-only: drop its assets so a later go build
    // embeds the placeholder alone.
    fs.rmSync(path.join(webDir, "dist", "assets"), { recursive: true, force: true });
    fs.rmSync(state.tmpDir, { recursive: true, force: true });
    fs.rmSync(STATE_FILE, { force: true });
  }
  fs.rmSync(URL_FILE, { force: true });
}
