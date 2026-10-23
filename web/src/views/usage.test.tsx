import { create } from "@bufbuild/protobuf";
import { describe, expect, it } from "vitest";
import {
  AccountSchema,
  PacingState,
} from "../../../gen/ts/fleet/hub/v1/account_pb.ts";
import { isHighlighted, percentText } from "./usage.tsx";

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
});
