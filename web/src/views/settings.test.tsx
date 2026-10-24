import { create } from "@bufbuild/protobuf";
import { describe, expect, it } from "vitest";
import { RoutineTriggerSchema } from "../../../gen/ts/fleet/hub/v1/routine_pb.ts";
import { SettingsScopeSchema } from "../../../gen/ts/fleet/hub/v1/settings_pb.ts";
import { durationFromMs } from "@bufbuild/protobuf/wkt";
import {
  durationToMinutes,
  formatWeeklyCron,
  layerLabel,
  minutesToDuration,
  parseWeeklyCron,
  providerForHost,
  routineScheduleText,
} from "./settings-helpers.ts";

describe("settings helpers", () => {
  it("names the code host from its address", () => {
    expect(providerForHost("github.com")).toBe("GitHub");
    expect(providerForHost("gitlab.acme.dev")).toBe("GitLab");
    expect(providerForHost("bitbucket.org")).toBe("Bitbucket");
  });

  it("falls back to the address itself for an unknown host", () => {
    expect(providerForHost("code.example.dev")).toBe("code.example.dev");
  });

  it("round-trips minutes through a Duration", () => {
    expect(durationToMinutes(durationFromMs(2 * 60000))).toBe(2);
    expect(durationToMinutes(undefined)).toBeNull();
    expect(durationMsOf(minutesToDuration(5))).toBe(5 * 60000);
  });

  it("rejects a zero or negative minute count", () => {
    expect(() => minutesToDuration(0)).toThrow();
    expect(() => minutesToDuration(-3)).toThrow();
  });

  it("parses a weekly cron time and formats it back", () => {
    expect(parseWeeklyCron("0 7 * * 1")).toEqual({ day: 1, hour: 7, minute: 0 });
    expect(formatWeeklyCron(1, 7, 0)).toBe("0 7 * * 1");
  });

  it("reads Sunday as day 7 too", () => {
    expect(parseWeeklyCron("30 18 * * 7")).toEqual({ day: 0, hour: 18, minute: 30 });
  });

  it("returns null for a non-weekly cron shape", () => {
    expect(parseWeeklyCron("*/15 * * * *")).toBeNull();
    expect(parseWeeklyCron("not a cron")).toBeNull();
  });

  it("describes the routine schedule in plain words", () => {
    const weekly = create(RoutineTriggerSchema, {
      kind: { case: "cron", value: { expression: "0 7 * * 1", timezone: "UTC" } },
    });
    expect(routineScheduleText(weekly)).toContain("Monday");
    expect(routineScheduleText(weekly)).toContain("7:00");
    const emptyQueue = create(RoutineTriggerSchema, {
      kind: { case: "queueEmpty", value: {} },
    });
    expect(routineScheduleText(emptyQueue)).toContain("no ready work");
    expect(routineScheduleText(undefined)).toContain("No schedule");
  });

  it("labels each settings layer in plain words", () => {
    const fleet = create(SettingsScopeSchema, { kind: { case: "fleet", value: {} } });
    expect(layerLabel(fleet)).toBe("fleet default");
    const group = create(SettingsScopeSchema, { kind: { case: "groupLabel", value: "laptop" } });
    expect(layerLabel(group)).toBe("group laptop");
    const machine = create(SettingsScopeSchema, {
      kind: { case: "machine", value: { value: "m-12" } },
    });
    expect(layerLabel(machine)).toBe("machine m-12");
    expect(layerLabel(undefined)).toBe("unknown layer");
  });
});

function durationMsOf(d: { seconds: bigint; nanos: number }): number {
  return Number(d.seconds) * 1000 + Math.floor(d.nanos / 1e6);
}
