import { create } from "@bufbuild/protobuf";
import { describe, expect, it } from "vitest";
import {
  Severity,
  TriageItemSchema,
  TriageKind,
} from "../../../gen/ts/fleet/hub/v1/triage_pb.ts";
import { detailText, kindTitle, orderItems, severityText } from "./triage.tsx";

function item(partial: Record<string, unknown>) {
  return create(TriageItemSchema, partial);
}

describe("triage helpers", () => {
  it("names severities in plain words", () => {
    expect(severityText(Severity.CRITICAL)).toBe("urgent");
    expect(severityText(Severity.ACTION)).toBe("needs you");
    expect(severityText(Severity.WARNING)).toBe("warning");
    expect(severityText(Severity.INFO)).toBe("info");
  });

  it("orders by severity, then by age", () => {
    const oldAction = item({
      fingerprint: "a",
      severity: Severity.ACTION,
      createTime: { seconds: 100n, nanos: 0 },
    });
    const newAction = item({
      fingerprint: "b",
      severity: Severity.ACTION,
      createTime: { seconds: 200n, nanos: 0 },
    });
    const warning = item({
      fingerprint: "c",
      severity: Severity.WARNING,
      createTime: { seconds: 50n, nanos: 0 },
    });
    expect(orderItems([warning, newAction, oldAction]).map((i) => i.fingerprint)).toEqual([
      "a",
      "b",
      "c",
    ]);
  });

  it("titles known kinds and returns null for the rest", () => {
    expect(kindTitle(TriageKind.PROMPT)).toContain("asking");
    expect(kindTitle(TriageKind.STUCK)).toContain("stuck");
    expect(kindTitle(TriageKind.PACING)).toContain("usage");
    expect(kindTitle(TriageKind.REVIEW_LOOP)).toBeNull();
  });

  it("reads note and condition text, nothing otherwise", () => {
    const noted = item({ detail: { case: "note", value: { text: "goal line" } } });
    expect(detailText(noted)).toBe("goal line");
    const conditioned = item({ detail: { case: "condition", value: { text: "rate limited" } } });
    expect(detailText(conditioned)).toBe("rate limited");
    const prompted = item({ detail: { case: "prompt", value: { text: "fix it?" } } });
    expect(detailText(prompted)).toBe("fix it?");
    const bare = item({});
    expect(detailText(bare)).toBeNull();
  });
});
