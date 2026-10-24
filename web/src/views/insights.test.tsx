import { create } from "@bufbuild/protobuf";
import { describe, expect, it } from "vitest";
import {
  InsightDimension,
  InsightGroupKeySchema,
  OutcomeRowSchema,
  QuotaWindowSchema,
} from "../../../gen/ts/fleet/hub/v1/insights_pb.ts";
import {
  durationText,
  groupName,
  perGoalText,
  percentValue,
  rangeStartMs,
  rateText,
  reportBlocks,
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

  it("never shows a rate under 5 goals", () => {
    const few = create(OutcomeRowSchema, { ran: 3, merged: 2, mergeRatePercent: 67 });
    expect(rateText(few.ran, few.mergeRatePercent)).toContain("too few");
    expect(rateText(few.ran, few.mergeRatePercent)).toContain("3 goals");
    const unset = create(OutcomeRowSchema, { ran: 10, merged: 7 });
    expect(rateText(unset.ran, unset.mergeRatePercent)).toContain("too few");
    expect(rateText(10, 70)).toContain("70%");
    expect(rateText(10, 70)).toContain("10 goals");
  });

  it("means per goal only with enough goals behind them", () => {
    expect(perGoalText(12, 6)).toContain("2.0");
    expect(perGoalText(12, 6)).toContain("per goal");
    expect(perGoalText(3, 2)).toContain("2 goals");
    expect(perGoalText(3, 2)).not.toContain("per goal");
    expect(perGoalText(5, 0)).toContain("no goals");
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

  it("names outcome groups in plain words", () => {
    const key = (partial: Record<string, unknown>) => create(InsightGroupKeySchema, partial);
    expect(groupName(key({ harness: { name: "codex" } }), InsightDimension.HARNESS)).toBe("Codex");
    expect(groupName(key({ model: "opus" }), InsightDimension.MODEL)).toBe("opus");
    expect(groupName(key({}), InsightDimension.MODEL)).toBe("(no model)");
    expect(groupName(key({ account: { value: "a-1" } }), InsightDimension.ACCOUNT)).toBe("a-1");
    expect(groupName(key({ label: "perf" }), InsightDimension.LABEL)).toBe("perf");
    expect(groupName(key({}), InsightDimension.HARNESS)).toBe("(unknown)");
  });

  it("sanitizes the written report into text blocks", () => {
    const blocks = reportBlocks(
      "<!-- fleet-insights bundle=abc -->\n# Week 41\n- waste grew\n<script>alert(1)</script>\nSee [the numbers](https://example.com/x)",
    );
    expect(blocks.some((b) => b.text.includes("fleet-insights"))).toBe(false);
    expect(blocks.find((b) => b.kind === "heading")?.text).toBe("Week 41");
    expect(blocks.find((b) => b.kind === "bullet")?.text).toBe("waste grew");
    const raw = blocks.find((b) => b.text.includes("script"));
    expect(raw?.kind).toBe("text");
    expect(raw?.text).toBe("<script>alert(1)</script>");
    expect(blocks.some((b) => b.text.includes("the numbers (https://example.com/x)"))).toBe(true);
  });

  it("starts ranges at a UTC midnight", () => {
    const now = Date.UTC(2026, 9, 24, 15, 30);
    expect(rangeStartMs(7, now)).toBe(Date.UTC(2026, 9, 17));
    expect(rangeStartMs(30, now)).toBe(Date.UTC(2026, 8, 24));
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
