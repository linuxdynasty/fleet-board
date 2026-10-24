import { create } from "@bufbuild/protobuf";
import { describe, expect, it } from "vitest";
import { PaneKeySchema } from "../../../gen/ts/fleet/type/v1/ids_pb.ts";
import {
  SessionSchema,
  SessionWindowSchema,
} from "../../../gen/ts/fleet/hub/v1/session_pb.ts";
import {
  originTag,
  paneKeyText,
  sessionKey,
  windowLabel,
} from "./session.tsx";

describe("session helpers", () => {
  it("keys a session by its id", () => {
    const s = create(SessionSchema, { id: { value: "s-1" } });
    expect(sessionKey(s)).toBe("s-1");
    expect(sessionKey(create(SessionSchema, {}))).toBe("");
  });

  it("keys a pane by server, start id and pane id", () => {
    const key = create(PaneKeySchema, {
      server: "fleet",
      serverStartId: "abc",
      paneId: "%12",
    });
    expect(paneKeyText(key)).toBe("fleet/abc/%12");
    expect(paneKeyText(undefined)).toBe("");
  });

  it("tags who started the session in plain words", () => {
    expect(originTag(true)).toBe("started by fleet");
    expect(originTag(false)).toBe("yours");
  });

  it("labels a window like the status bar", () => {
    const w = create(SessionWindowSchema, { index: 2, name: "goal" });
    expect(windowLabel(w)).toBe("[2] goal");
  });
});
