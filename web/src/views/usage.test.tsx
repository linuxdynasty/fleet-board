import { create } from "@bufbuild/protobuf";
import { describe, expect, it } from "vitest";
import {
  AccountSchema,
  PacingState,
} from "../../../gen/ts/fleet/hub/v1/account_pb.ts";
import { ResetTimeSchema } from "../../../gen/ts/fleet/type/v1/usage_pb.ts";
import { LimitBucketSchema } from "../../../gen/ts/fleet/type/v1/usage_pb.ts";
import { bucketDisplay, isHighlighted, percentText, resetText, wasteLine } from "./usage.tsx";

describe("usage helpers", () => {
  it("reads a percent value and maps unknown to null", () => {
    expect(percentText({ $typeName: "fleet.type.v1.Percent", kind: { case: "value", value: 58 } })).toBe(
      "58%",
    );
    expect(
      percentText({
        $typeName: "fleet.type.v1.Percent",
        kind: { case: "unknown", value: { $typeName: "fleet.type.v1.PercentUnknown", reason: 0 } },
      }),
    ).toBeNull();
    expect(percentText(undefined)).toBeNull();
  });

  it("highlights waste of 5 points or more", () => {
    const wasteful = create(AccountSchema, {
      status: { pacing: { projectedUnusedPercent: 25 } },
    });
    expect(isHighlighted(wasteful)).toBe(true);
    const fine = create(AccountSchema, {
      status: {
        pacing: { projectedUnusedPercent: 2, state: PacingState.OK },
      },
    });
    expect(isHighlighted(fine)).toBe(false);
  });

  it("highlights an account behind pace with no goal running", () => {
    const idle = create(AccountSchema, {
      status: { pacing: { idleWhileBehind: true } },
    });
    expect(isHighlighted(idle)).toBe(true);
  });

  it("shows 100-minus-used where the harness showed remaining", () => {
    const remaining = create(LimitBucketSchema, {
      shownAsRemaining: true,
      percentUsed: { kind: { case: "value", value: 90 } },
    });
    expect(bucketDisplay(remaining)).toEqual({ label: "left", text: "10%" });
    const used = create(LimitBucketSchema, {
      shownAsRemaining: false,
      percentUsed: { kind: { case: "value", value: 90 } },
    });
    expect(bucketDisplay(used)).toEqual({ label: "used", text: "90%" });
  });

  it("hides cached quota when pacing is unknown", () => {
    const account = create(AccountSchema, {
      spec: { displayName: "mac" },
      status: {
        pacing: { state: PacingState.UNKNOWN, activeGoals: 1, projectedUnusedPercent: 40 },
        usage: {
          buckets: [
            {
              shownAsRemaining: true,
              percentUsed: { kind: { case: "value", value: 90 } },
              resetsTime: { originalText: "Resets Nov 4" },
            },
          ],
        },
      },
    });
    const line = wasteLine(account, Date.now(), true);
    expect(line).toContain("usage unknown");
    expect(line).not.toContain("90%");
    expect(line).not.toContain("10%");
    expect(line).not.toContain("Nov 4");
  });

  it("shows the original text for date-only resets", () => {
    const reset = create(ResetTimeSchema, {
      dateOnly: true,
      originalText: "Resets Nov 4",
      resolvedTime: { seconds: 1762300800n, nanos: 0 },
    });
    expect(resetText(reset, Date.now())).toBe("Resets Nov 4");
  });
});
