import { create } from "@bufbuild/protobuf";
import { describe, expect, it } from "vitest";
import { QuotaWindowSchema } from "../../../gen/ts/fleet/hub/v1/insights_pb.ts";
import {
  dayRangeMs,
  durationText,
  percentValue,
  sampleText,
  windowLine,
} from "./insights-helpers.ts";

describe("insights helpers", () => {
  it("reads a percent value and maps unknown to null", () => {
    expect(
      percentValue({ $typeName: "fleet.type.v1.Percent", kind: { case: "value", value: 58 } }),
    ).toBe(58);
    expect(
      percentValue({
        $typeName: "fleet.type.v1.Percent",
        kind: { case: "unknown", value: { $typeName: "fleet.type.v1.PercentUnknown", reason: 0 } },
      }),
    ).toBeNull();
    expect(percentValue(undefined)).toBeNull();
  });

  it("says durations in plain words", () => {
    expect(durationText(undefined)).toBeNull();
    expect(durationText({ $typeName: "google.protobuf.Duration", seconds: 1200n, nanos: 0 })).toBe(
      "20 min",
    );
    expect(durationText({ $typeName: "google.protobuf.Duration", seconds: 6840n, nanos: 0 })).toBe(
      "1.9 h",
    );
  });

  it("ends ranges at next UTC midnight so today counts", () => {
    const now = Date.UTC(2026, 9, 24, 15, 30);
    expect(dayRangeMs(7, now)).toEqual({
      startMs: Date.UTC(2026, 9, 18),
      endMs: Date.UTC(2026, 9, 25),
    });
    expect(dayRangeMs(30, now)).toEqual({
      startMs: Date.UTC(2026, 8, 25),
      endMs: Date.UTC(2026, 9, 25),
    });
  });

  it("counts the sample behind every number", () => {
    expect(sampleText(1, "goal")).toBe("1 goal");
    expect(sampleText(5, "goal")).toBe("5 goals");
    expect(sampleText(23, "reading")).toBe("23 readings");
  });

  it("sums a quota window into one plain line", () => {
    const window = create(QuotaWindowSchema, {
      complete: true,
      reservePercent: 10,
      usedAtEnd: { kind: { case: "value", value: 75 } },
      unused: { kind: { case: "value", value: 15 } },
      fleetUsed: { kind: { case: "value", value: 40 } },
      behind: { seconds: 7200n, nanos: 0 },
      idleBehind: { seconds: 3600n, nanos: 0 },
      readings: 23,
    });
    const line = windowLine(window);
    expect(line).toContain("90");
    expect(line).toContain("15");
    expect(line).toContain("40");
    expect(line).toContain("23 readings");
  });
});
