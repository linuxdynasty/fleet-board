// View keys (row 14): the contract's keyboard slice from
// ui-contract-map ("View keys"). Digits switch views, n goes to the
// Session view, and j/k move a visible selection through the Triage
// feed. The jump-to palette (Cmd+K, /) is a later slice. Keys never
// fire while typing in a field. Plain helper functions live here so
// tests can call them directly; the App shell wires them up.
import type { RouteName } from "./router.ts";

// The contract's order: 1 Triage, 2 Work, 3 Session, 4 Usage,
// 5 Machines, 6 Settings, 7 Insights.
export const KEY_VIEW_ORDER: RouteName[] = [
  "triage",
  "work",
  "session",
  "usage",
  "machines",
  "settings",
  "insights",
];

export function viewForKey(key: string): RouteName | null {
  if (!/^[1-7]$/.test(key)) {
    return null;
  }
  return KEY_VIEW_ORDER[Number(key) - 1] ?? null;
}

export type KeyAction =
  | { kind: "view"; route: RouteName }
  | { kind: "next-session" }
  | { kind: "triage-move"; delta: 1 | -1 };

// keyAction says what a key press means on the given view, or null
// when the key does nothing there. Modifier keys are checked by the
// caller (the shell listener), not here.
export function keyAction(key: string, route: RouteName): KeyAction | null {
  const view = viewForKey(key);
  if (view !== null) {
    return { kind: "view", route: view };
  }
  if (key === "n") {
    return { kind: "next-session" };
  }
  if (route === "triage" && key === "j") {
    return { kind: "triage-move", delta: 1 };
  }
  if (route === "triage" && key === "k") {
    return { kind: "triage-move", delta: -1 };
  }
  return null;
}

// isTypingTarget keeps keys quiet while the owner types: any text
// field (including a password field), a dropdown, or an editable
// area. A plain press elsewhere (body, link, button) still counts.
export function isTypingTarget(target: EventTarget | null): boolean {
  if (target === null || typeof target !== "object") {
    return false;
  }
  const el = target as Partial<HTMLElement>;
  const tag = typeof el.tagName === "string" ? el.tagName.toUpperCase() : "";
  if (tag === "INPUT" || tag === "TEXTAREA" || tag === "SELECT") {
    return true;
  }
  return el.isContentEditable === true;
}

export const TRIAGE_MOVE_EVENT = "fleet:triage-move";

export interface TriageMoveDetail {
  delta: 1 | -1;
}

// triageMoveDelta reads the move direction back out of the event the
// shell fires, or null when the event carries anything else.
export function triageMoveDelta(event: Event): 1 | -1 | null {
  const detail = (event as CustomEvent<TriageMoveDetail>).detail;
  if (detail === null || detail === undefined) {
    return null;
  }
  if (detail.delta === 1 || detail.delta === -1) {
    return detail.delta;
  }
  return null;
}

// moveSelection steps the Triage selection and clamps it to the feed,
// so j on the last card and k on the first card stay put.
export function moveSelection(index: number, delta: 1 | -1, count: number): number {
  if (count <= 0) {
    return 0;
  }
  return Math.min(Math.max(index + delta, 0), count - 1);
}

// clampSelection keeps a stored Triage selection inside a changed feed.
// Live updates can remove cards the selection pointed at; the stored
// index is normalized so the next j/k moves from the card on screen,
// not from a stale position past the end.
export function clampSelection(index: number, count: number): number {
  if (count <= 0) {
    return 0;
  }
  return Math.min(Math.max(index, 0), count - 1);
}
