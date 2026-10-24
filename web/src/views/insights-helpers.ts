// Insights helpers (row 14, issue #42): plain-word formatting over the
// hub's efficiency rollups. Pure functions, tested in insights.test.tsx.
// Row 14 serves efficiency only; the outcomes, stuck, routines, report and
// suggestion helpers arrive with row 27.
import { Code, ConnectError } from "@connectrpc/connect";
import type { Duration, Timestamp } from "@bufbuild/protobuf/wkt";
import type { QuotaWindow } from "../../../gen/ts/fleet/hub/v1/insights_pb.ts";
import type { Percent } from "../../../gen/ts/fleet/type/v1/status_pb.ts";

export function millis(ts: Timestamp | undefined): number | null {
  if (ts === undefined) {
    return null;
  }
  return Number(ts.seconds) * 1000;
}

export function percentValue(p: Percent | undefined): number | null {
  if (p?.kind.case === "value") {
    return p.kind.value;
  }
  return null;
}

export function durationText(d: Duration | undefined): string | null {
  if (d === undefined) {
    return null;
  }
  const seconds = Number(d.seconds);
  if (seconds < 3600) {
    return `${Math.round(seconds / 60)} min`;
  }
  return `${(seconds / 3600).toFixed(1)} h`;
}

// dayRangeMs returns the UTC-day range behind a day span. The end is next
// UTC midnight, so today's quota windows count: the hub floors an explicit
// end down to midnight, and passing "now" would cut today off. The start is
// that end minus the span, keeping the picked day count exact.
export function dayRangeMs(days: number, nowMs: number): { startMs: number; endMs: number } {
  const dayMs = 86400000;
  const at = new Date(nowMs);
  const endMs = Date.UTC(at.getUTCFullYear(), at.getUTCMonth(), at.getUTCDate()) + dayMs;
  return { startMs: endMs - days * dayMs, endMs };
}

export function sampleText(n: number, noun: string): string {
  return `${n} ${noun}${n === 1 ? "" : "s"}`;
}

// windowLine sums one quota window into one plain line: every number names
// what stands behind it, and unknown use stays unknown, never zero.
export function windowLine(w: QuotaWindow): string {
  const usable = 100 - w.reservePercent;
  const used = percentValue(w.usedAtEnd);
  const unused = percentValue(w.unused);
  const fleet = percentValue(w.fleetUsed);
  const behind = durationText(w.behind) ?? "unknown time";
  const idle = durationText(w.idleBehind) ?? "unknown time";
  const parts = [w.complete ? "ended" : "so far"];
  parts.push(used === null ? "use unknown" : `used ${used}% of ${usable}% usable`);
  if (unused !== null) {
    parts.push(`left ${unused} points unused`);
  }
  if (fleet !== null) {
    parts.push(`the fleet spent ${fleet} points`);
  }
  parts.push(`behind pace ${behind} (idle ${idle})`);
  parts.push(sampleText(w.readings, "reading"));
  if (w.readings < 20) {
    parts.push("low confidence");
  }
  return parts.join(" · ");
}

export function agoText(ms: number | null, nowMs: number): string {
  if (ms === null) {
    return "unknown time";
  }
  const diff = Math.max(0, nowMs - ms);
  if (diff < 3600000) {
    return `${Math.max(1, Math.round(diff / 60000))} min ago`;
  }
  if (diff < 86400000) {
    return `${Math.round(diff / 3600000)} h ago`;
  }
  return new Date(ms).toLocaleDateString();
}

export function deniedOf(err: unknown): boolean {
  return (
    err instanceof ConnectError &&
    (err.code === Code.PermissionDenied || err.code === Code.Unauthenticated)
  );
}
