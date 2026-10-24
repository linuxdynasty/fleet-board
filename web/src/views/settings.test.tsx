import { create } from "@bufbuild/protobuf";
import { describe, expect, it } from "vitest";
import { SettingsScopeSchema } from "../../../gen/ts/fleet/hub/v1/settings_pb.ts";
import { durationFromMs } from "@bufbuild/protobuf/wkt";
import {
  durationToMinutes,
  layerLabel,
  providerForHost,
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

  it("reads an idle threshold as whole minutes", () => {
    expect(durationToMinutes(durationFromMs(2 * 60000))).toBe(2);
    expect(durationToMinutes(undefined)).toBeNull();
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
