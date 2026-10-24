import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

// Shared paths for the smoke run. The global setup writes the fleetd URL to
// .e2e-url (gitignored); FLEET_E2E_URL overrides it for a hand-started hub.

const here = path.dirname(fileURLToPath(import.meta.url));

export const E2E_DIR = here;
export const SCREENSHOT_DIR = path.join(here, "screenshots");
export const URL_FILE = path.join(here, ".e2e-url");
export const STATE_FILE = path.join(here, ".e2e-state.json");
// Written before the build, so a setup failure still restores the
// committed placeholder even when no hub ever started.
export const RESTORE_FILE = path.join(here, ".e2e-restore.json");

export const VIEWS = [
  "machines",
  "usage",
  "session",
  "triage",
  "work",
  "insights",
  "settings",
] as const;

export type ViewName = (typeof VIEWS)[number];

export const VIEW_HEADINGS: Record<ViewName, string> = {
  machines: "Machines",
  usage: "Usage",
  session: "Session",
  triage: "Triage",
  work: "Work",
  insights: "Insights",
  settings: "Settings",
};

export function viewUrl(base: string, view: ViewName): string {
  return `${base}/#/${view}`;
}

export function screenshotPath(view: ViewName): string {
  return path.join(SCREENSHOT_DIR, `${view}.png`);
}

export function fleetdUrl(): string {
  const fromEnv = process.env["FLEET_E2E_URL"];
  if (fromEnv !== undefined && fromEnv !== "") {
    return fromEnv;
  }
  if (!fs.existsSync(URL_FILE)) {
    throw new Error(
      "fleetd URL unknown: run the smoke through the global setup, or set FLEET_E2E_URL",
    );
  }
  return fs.readFileSync(URL_FILE, "utf8").trim();
}
