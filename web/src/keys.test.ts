import { describe, expect, it } from "vitest";
import {
  KEY_VIEW_ORDER,
  isTypingTarget,
  keyAction,
  moveSelection,
  triageMoveDelta,
  viewForKey,
} from "./keys.ts";

function element(tag: string, editable = false): HTMLElement {
  const el = document.createElement(tag);
  if (editable) {
    el.contentEditable = "true";
  }
  return el;
}

describe("view keys", () => {
  it("maps 1-7 to the contract's view order", () => {
    expect(KEY_VIEW_ORDER).toEqual([
      "triage",
      "work",
      "session",
      "usage",
      "machines",
      "settings",
      "insights",
    ]);
    expect(viewForKey("1")).toBe("triage");
    expect(viewForKey("2")).toBe("work");
    expect(viewForKey("3")).toBe("session");
    expect(viewForKey("4")).toBe("usage");
    expect(viewForKey("5")).toBe("machines");
    expect(viewForKey("6")).toBe("settings");
    expect(viewForKey("7")).toBe("insights");
    expect(viewForKey("0")).toBeNull();
    expect(viewForKey("8")).toBeNull();
    expect(viewForKey("a")).toBeNull();
    expect(viewForKey("")).toBeNull();
  });

  it("goes to a view on its digit from anywhere", () => {
    expect(keyAction("2", "machines")).toEqual({ kind: "view", route: "work" });
    expect(keyAction("5", "triage")).toEqual({ kind: "view", route: "machines" });
  });

  it("goes to the session view on n", () => {
    expect(keyAction("n", "machines")).toEqual({ kind: "next-session" });
    expect(keyAction("n", "triage")).toEqual({ kind: "next-session" });
  });

  it("moves the triage selection on j and k, only in triage", () => {
    expect(keyAction("j", "triage")).toEqual({ kind: "triage-move", delta: 1 });
    expect(keyAction("k", "triage")).toEqual({ kind: "triage-move", delta: -1 });
    expect(keyAction("j", "work")).toBeNull();
    expect(keyAction("k", "session")).toBeNull();
  });

  it("ignores anything else", () => {
    expect(keyAction("a", "work")).toBeNull();
    expect(keyAction("o", "triage")).toBeNull();
    expect(keyAction("Enter", "triage")).toBeNull();
  });

  it("stays quiet while typing, including passwords", () => {
    const password = element("input");
    password.setAttribute("type", "password");
    expect(isTypingTarget(element("input"))).toBe(true);
    expect(isTypingTarget(password)).toBe(true);
    expect(isTypingTarget(element("textarea"))).toBe(true);
    expect(isTypingTarget(element("select"))).toBe(true);
    expect(isTypingTarget(element("div", true))).toBe(true);
    expect(isTypingTarget(element("div"))).toBe(false);
    expect(isTypingTarget(document.body)).toBe(false);
    expect(isTypingTarget(null)).toBe(false);
  });

  it("clamps the triage selection to the feed", () => {
    expect(moveSelection(0, 1, 3)).toBe(1);
    expect(moveSelection(2, 1, 3)).toBe(2);
    expect(moveSelection(0, -1, 3)).toBe(0);
    expect(moveSelection(5, 1, 3)).toBe(2);
    expect(moveSelection(0, 1, 0)).toBe(0);
    expect(moveSelection(0, 1, 1)).toBe(0);
  });

  it("reads the triage move direction, nothing else", () => {
    expect(triageMoveDelta(new CustomEvent("x", { detail: { delta: 1 } }))).toBe(1);
    expect(triageMoveDelta(new CustomEvent("x", { detail: { delta: -1 } }))).toBe(-1);
    expect(triageMoveDelta(new CustomEvent("x", { detail: null }))).toBeNull();
    expect(triageMoveDelta(new Event("x"))).toBeNull();
  });
});
