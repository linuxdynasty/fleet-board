import { durationFromMs, durationMs, type Duration } from "@bufbuild/protobuf/wkt";
import type { SettingsScope } from "../../../gen/ts/fleet/hub/v1/settings_pb.ts";
import type { RoutineTrigger } from "../../../gen/ts/fleet/hub/v1/routine_pb.ts";

export const DAY_NAMES = ["Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"];

// providerForHost names the code host behind a repo address. The contract
// stores host and path only (RepoRef); the mockup reads the provider off the
// host the same way (view-settings.js).
export function providerForHost(host: string): string {
  const lower = host.toLowerCase();
  if (lower.includes("gitlab")) {
    return "GitLab";
  }
  if (lower.includes("bitbucket")) {
    return "Bitbucket";
  }
  if (lower.includes("github")) {
    return "GitHub";
  }
  return host;
}

// durationToMinutes reads an idle threshold as whole minutes.
export function durationToMinutes(d: Duration | undefined): number | null {
  if (d === undefined) {
    return null;
  }
  return Math.round(durationMs(d) / 60000);
}

// minutesToDuration builds an idle threshold from whole minutes.
export function minutesToDuration(minutes: number): Duration {
  if (!Number.isFinite(minutes) || minutes < 1) {
    throw new Error("minutes must be 1 or more");
  }
  return durationFromMs(Math.round(minutes * 60000));
}

export interface WeeklyTime {
  day: number;
  hour: number;
  minute: number;
}

// parseWeeklyCron reads a 5-field cron that fires once a week
// ("minute hour * * weekday"). Anything else returns null.
export function parseWeeklyCron(expression: string): WeeklyTime | null {
  const match = /^(\d+)\s+(\d+)\s+\*\s+\*\s+(\d+)$/.exec(expression.trim());
  if (match === null) {
    return null;
  }
  const minute = Number(match[1]);
  const hour = Number(match[2]);
  let day = Number(match[3]);
  if (!Number.isInteger(minute) || minute < 0 || minute > 59) {
    return null;
  }
  if (!Number.isInteger(hour) || hour < 0 || hour > 23) {
    return null;
  }
  if (!Number.isInteger(day) || day < 0 || day > 7) {
    return null;
  }
  if (day === 7) {
    day = 0;
  }
  return { day, hour, minute };
}

// formatWeeklyCron writes the once-a-week shape parseWeeklyCron reads.
export function formatWeeklyCron(day: number, hour: number, minute: number): string {
  return `${minute} ${hour} * * ${day}`;
}

// routineScheduleText says when a routine runs, in plain words.
export function routineScheduleText(trigger: RoutineTrigger | undefined): string {
  if (trigger === undefined || trigger.kind.case === undefined) {
    return "No schedule set.";
  }
  if (trigger.kind.case === "queueEmpty") {
    return "Runs when the repo has no ready work.";
  }
  const cron = trigger.kind.value;
  const parsed = parseWeeklyCron(cron.expression);
  if (parsed === null) {
    return cron.expression === "" ? "No schedule set." : `On a schedule: ${cron.expression}`;
  }
  const time = `${parsed.hour}:${String(parsed.minute).padStart(2, "0")}`;
  const zone = cron.timezone === "" ? "" : ` (${cron.timezone})`;
  return `Every ${DAY_NAMES[parsed.day]} at ${time}${zone}.`;
}

// layerLabel names the layer a Settings row belongs to, in plain words.
export function layerLabel(scope: SettingsScope | undefined): string {
  if (scope === undefined || scope.kind.case === undefined) {
    return "unknown layer";
  }
  switch (scope.kind.case) {
    case "fleet":
      return "fleet default";
    case "groupLabel":
      return `group ${scope.kind.value}`;
    case "machine":
      return `machine ${scope.kind.value.value}`;
  }
}
