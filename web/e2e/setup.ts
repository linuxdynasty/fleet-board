import { execFileSync, spawn, type ChildProcess } from "node:child_process";
import fs from "node:fs";
import http from "node:http";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";

import { RESTORE_FILE, STATE_FILE, URL_FILE } from "./helpers.ts";

// Fixed loopback ports for the smoke hub, away from the dev defaults (7080,
// 7443) so the smoke never talks to a developer's hub.
const API_PORT = 18080;
const AGENT_PORT = 18443;

const webDir = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const repoRoot = path.resolve(webDir, "..");
const distIndex = path.join(webDir, "dist", "index.html");

interface E2EState {
  url: string;
  pid: number;
  tmpDir: string;
}

// waitForLogLine resolves when the fleetd log contains the line, so the
// setup can prove the child bound its listeners instead of trusting a 200
// that any hub on the fixed ports could have answered.
function waitForLogLine(logPath: string, line: RegExp, timeoutMs: number): Promise<void> {
  const started = Date.now();
  return new Promise((resolve, reject) => {
    const attempt = (): void => {
      let text = "";
      try {
        text = fs.readFileSync(logPath, "utf8");
      } catch {
        // Not written yet; keep polling.
      }
      if (line.test(text)) {
        resolve();
        return;
      }
      if (Date.now() - started > timeoutMs) {
        reject(new Error(`timed out waiting for ${line} in ${logPath}`));
        return;
      }
      setTimeout(attempt, 200);
    };
    attempt();
  });
}

function run(cmd: string, args: string[], cwd: string): void {
  execFileSync(cmd, args, { cwd, stdio: "inherit" });
}

function readPlaceholder(): Buffer {
  try {
    return execFileSync("git", ["show", "HEAD:web/dist/index.html"], {
      cwd: repoRoot,
    });
  } catch {
    return fs.readFileSync(distIndex);
  }
}

function waitForDashboard(url: string): Promise<void> {
  const deadline = Date.now() + 60_000;
  return new Promise((resolve, reject) => {
    const attempt = (): void => {
      const req = http.get(url, (res) => {
        res.resume();
        if (res.statusCode === 200) {
          resolve();
          return;
        }
        if (Date.now() > deadline) {
          reject(new Error(`dashboard at ${url} answered ${res.statusCode}`));
          return;
        }
        setTimeout(attempt, 200);
      });
      req.on("error", () => {
        if (Date.now() > deadline) {
          reject(new Error(`dashboard at ${url} never answered`));
          return;
        }
        setTimeout(attempt, 200);
      });
    };
    attempt();
  });
}

function startFleetd(binary: string, configPath: string, logPath: string): ChildProcess {
  const log = fs.openSync(logPath, "w");
  const child = spawn(binary, ["run", "--config", configPath], {
    detached: true,
    stdio: ["ignore", log, log],
  });
  child.unref();
  return child;
}

// Global setup: stash the committed dist placeholder, build the real page,
// build fleetd with it embedded, seed a throwaway state folder, start fleetd
// on fixed loopback ports, and publish its URL for the specs. Needs Go, npm
// dependencies and gen/ts (documented setup: make tools gen, npm install);
// it installs the Playwright Chromium browser when missing.
export default async function globalSetup(): Promise<void> {
  const tmpDir = fs.mkdtempSync(path.join(os.tmpdir(), "fleet-e2e-"));
  // Stash first: the build below overwrites the committed placeholder.
  // Prefer the committed copy, so a stray earlier build cannot poison the
  // stash; fall back to the worktree file before the placeholder is committed.
  const placeholder = readPlaceholder();
  console.log(`e2e setup: stashing dist/index.html (${placeholder.length} bytes)`);
  fs.writeFileSync(path.join(tmpDir, "placeholder-index.html"), placeholder);
  // Publish the restore pointer before anything that can fail: the
  // teardown keys the placeholder restore off this file, not off the hub
  // state file written at the end, so an early failure still cleans up.
  fs.writeFileSync(RESTORE_FILE, JSON.stringify({ tmpDir }));

  run("npm", ["run", "build"], webDir);
  run("npx", ["playwright", "install", "chromium"], webDir);

  const binary = path.join(tmpDir, "fleetd");
  run("go", ["build", "-o", binary, "./cmd/fleetd"], repoRoot);

  const stateDir = path.join(tmpDir, "state");
  const configPath = path.join(tmpDir, "fleetd.yaml");
  fs.writeFileSync(
    configPath,
    `state_dir: ${stateDir}\nlisten: {agent: '127.0.0.1:${AGENT_PORT}', api: '127.0.0.1:${API_PORT}'}\ndiscovery: {mdns: false}\n`,
  );
  // e2eseed runs fleetd init (a real CA identity, which a booting hub
  // needs) then writes the shared seed rows for the machines.
  run("go", ["run", "./internal/e2eseed", configPath], repoRoot);
  const child = startFleetd(binary, configPath, path.join(tmpDir, "fleetd.log"));
  if (child.pid === undefined) {
    throw new Error("fleetd did not start: no pid");
  }
  const url = `http://127.0.0.1:${API_PORT}`;
  try {
    await waitForDashboard(`${url}/`);
  } catch (err) {
    try {
      process.kill(child.pid, "SIGKILL");
    } catch {
      // Already gone; the log below tells the story.
    }
    throw err;
  }
  // A 200 proves a hub answers here, not that it is ours: a concurrent
  // run's hub on the same fixed ports answers the same poll, and our
  // child is briefly alive before it binds. So after the poll, wait for
  // our child's own bind proof in its log, re-poll, and re-check it is
  // still alive; a dead child or a missing marker means the poll hit
  // someone else's hub (for example a bind failure on our side).
  const logPath = path.join(tmpDir, "fleetd.log");
  try {
    await waitForLogLine(logPath, /hub\.started/, 30000);
  } catch (err) {
    try {
      process.kill(child.pid, "SIGKILL");
    } catch {
      // Already gone; the error below tells the story.
    }
    throw new Error(
      `e2e setup: fleetd pid=${child.pid} never logged its start; see ${logPath}. ` +
        `Another run may hold the smoke ports: ${err instanceof Error ? err.message : err}`,
    );
  }
  await waitForDashboard(`${url}/`);
  if (child.exitCode !== null || child.signalCode !== null) {
    throw new Error(
      `e2e setup: fleetd pid=${child.pid} died after binding; see ${logPath}. ` +
        "Another run may hold the smoke ports.",
    );
  }
  const state: E2EState = { url, pid: child.pid, tmpDir };
  fs.writeFileSync(STATE_FILE, JSON.stringify(state));
  fs.writeFileSync(URL_FILE, url);
}
