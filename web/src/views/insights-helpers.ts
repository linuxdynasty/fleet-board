// Insights helpers (row 14, issue #42): plain-word formatting over the
// hub's insight rollups. Pure functions, tested in insights.test.tsx.
// Sections and the view live in insights-sections.tsx and insights.tsx.
import { Code, ConnectError } from "@connectrpc/connect";
import type { Duration, Timestamp } from "@bufbuild/protobuf/wkt";
import {
  InsightDimension,
  InsightMetric,
  InsightUnit,
  type InsightGroupKey,
  type QuotaWindow,
} from "../../../gen/ts/fleet/hub/v1/insights_pb.ts";
import type { SuggestionEvidence } from "../../../gen/ts/fleet/hub/v1/suggestion_pb.ts";
import { GoalPhaseReason } from "../../../gen/ts/fleet/hub/v1/goal_pb.ts";
import { HarnessConditionKind, PromptClass } from "../../../gen/ts/fleet/type/v1/harness_pb.ts";
import { ReasonCode, type Percent } from "../../../gen/ts/fleet/type/v1/status_pb.ts";

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

// rateText never shows a rate under 5 goals: the number stays, the rate does not.
export function rateText(ran: number, rate: number | undefined): string {
  const noun = ran === 1 ? "goal" : "goals";
  if (ran < 5 || rate === undefined) {
    return `too few goals (${ran} ${noun})`;
  }
  return `${rate}% of ${ran} goals`;
}

export function perGoalText(total: number, ran: number): string {
  if (ran === 0) {
    return "no goals ran";
  }
  if (ran < 5) {
    return `${total} across ${ran} goals`;
  }
  return `${(total / ran).toFixed(1)} per goal`;
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

export function capName(name: string): string {
  return name === "" ? "(unknown)" : name.charAt(0).toUpperCase() + name.slice(1);
}

export function groupName(
  group: InsightGroupKey | undefined,
  dimension: InsightDimension,
): string {
  switch (dimension) {
    case InsightDimension.HARNESS:
      return capName(group?.harness?.name ?? "");
    case InsightDimension.MODEL:
      return group?.model === undefined || group.model === "" ? "(no model)" : group.model;
    case InsightDimension.REPO:
      return group?.repo?.value === undefined || group.repo.value === "" ? "(unknown)" : group.repo.value;
    case InsightDimension.ACCOUNT:
      return group?.account?.value === undefined || group.account.value === "" ? "(unknown)" : group.account.value;
    case InsightDimension.MACHINE:
      return group?.machine?.value === undefined || group.machine.value === "" ? "(unknown)" : group.machine.value;
    case InsightDimension.LABEL:
      return group?.label === undefined || group.label === "" ? "(unknown)" : group.label;
    default:
      return "(unknown)";
  }
}

export interface ReportBlock {
  kind: "heading" | "bullet" | "text";
  text: string;
}

// reportBlocks parses the report into plain-text blocks. Links keep their
// address as text, marker comments are dropped, and anything that looks
// like markup stays literal text: the renderer never uses raw HTML.
export function reportBlocks(text: string): ReportBlock[] {
  const out: ReportBlock[] = [];
  for (const line of text.split("\n")) {
    if (line.startsWith("<!--")) {
      continue;
    }
    const linked = line.replace(/\[([^\]]*)\]\(([^)]*)\)/g, (_, a: string, u: string) => `${a} (${u})`);
    if (linked.startsWith("## ")) {
      out.push({ kind: "heading", text: linked.slice(3) });
    } else if (linked.startsWith("# ")) {
      out.push({ kind: "heading", text: linked.slice(2) });
    } else if (linked.startsWith("- ")) {
      out.push({ kind: "bullet", text: linked.slice(2) });
    } else if (linked.trim() !== "") {
      out.push({ kind: "text", text: linked });
    }
  }
  return out;
}

export function rangeStartMs(days: number, nowMs: number): number {
  const at = new Date(nowMs);
  return Date.UTC(at.getUTCFullYear(), at.getUTCMonth(), at.getUTCDate()) - days * 86400000;
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

export function promptClassText(c: PromptClass): string {
  switch (c) {
    case PromptClass.RUN_COMMAND:
      return "run-command prompts";
    case PromptClass.EDIT_FILES:
      return "file-edit prompts";
    case PromptClass.NETWORK_OR_TOOL:
      return "network or tool prompts";
    case PromptClass.TRUST:
      return "trust prompts";
    case PromptClass.PLAN_APPROVAL:
      return "plan-approval prompts";
    case PromptClass.QUESTION:
      return "question prompts";
    case PromptClass.SETUP:
      return "setup prompts";
    default:
      return "other prompts";
  }
}

export function conditionKindText(k: HarnessConditionKind): string {
  switch (k) {
    case HarnessConditionKind.PROVIDER_ERROR:
      return "provider errors";
    case HarnessConditionKind.RATE_LIMITED:
      return "rate limits";
    case HarnessConditionKind.USAGE_LIMIT:
      return "usage limits";
    case HarnessConditionKind.SIGN_IN_REQUIRED:
      return "sign-ins needed";
    case HarnessConditionKind.NETWORK:
      return "network trouble";
    case HarnessConditionKind.GOAL_INTERRUPTED:
      return "goal interruptions";
    case HarnessConditionKind.GOAL_PAUSED:
      return "goal pauses";
    default:
      return "other conditions";
  }
}

export function reasonCodeText(r: ReasonCode): string {
  switch (r) {
    case ReasonCode.HARNESS_NOT_INSTALLED:
      return "harness not installed";
    case ReasonCode.HARNESS_VERSION_UNSUPPORTED:
      return "harness version unsupported";
    case ReasonCode.HARNESS_NOT_SIGNED_IN:
      return "harness not signed in";
    case ReasonCode.FOLDER_UNTRUSTED:
      return "folder not trusted";
    case ReasonCode.WORKTREE_DIRTY:
      return "unsaved work in the worktree";
    case ReasonCode.DISK_LOW:
      return "disk low";
    case ReasonCode.POLICY_DENIED:
      return "refused by policy";
    case ReasonCode.SLOTS_FULL:
      return "no free slot";
    default:
      return `start failure ${ReasonCode[r] ?? r}`;
  }
}

export function stuckReasonText(r: GoalPhaseReason): string {
  switch (r) {
    case GoalPhaseReason.BLOCKED_NO_WATCHER:
      return "nothing is watching it";
    case GoalPhaseReason.WATCH_DEAD:
      return "its watcher died";
    case GoalPhaseReason.STOPPED_TWICE:
      return "stopped twice";
    case GoalPhaseReason.SESSION_CLOSED:
      return "session closed";
    case GoalPhaseReason.LOCAL_CONFIRM_TIMEOUT:
      return "a local confirm timed out";
    case GoalPhaseReason.PANE_LOST_TIMEOUT:
      return "pane lost";
    case GoalPhaseReason.NO_PROGRESS:
      return "no progress";
    case GoalPhaseReason.USAGE_LIMIT:
      return "usage limit";
    default:
      return "another reason";
  }
}

function metricText(m: InsightMetric): string {
  switch (m) {
    case InsightMetric.QUOTA_USED_PERCENT:
      return "quota used";
    case InsightMetric.QUOTA_UNUSED_PERCENT:
      return "quota unused";
    case InsightMetric.IDLE_BEHIND_SECONDS:
      return "idle while behind";
    case InsightMetric.GOALS_ENDED:
      return "goals ended";
    case InsightMetric.GOALS_MERGED:
      return "goals merged";
    case InsightMetric.MERGE_RATE_PERCENT:
      return "merge rate";
    case InsightMetric.GOALS_STUCK:
      return "stuck goals";
    case InsightMetric.NUDGES:
      return "hints sent";
    case InsightMetric.QUOTA_POINTS_SPENT:
      return "quota points spent";
    default:
      return "number";
  }
}

function unitSuffix(u: InsightUnit): string {
  switch (u) {
    case InsightUnit.PERCENT:
      return "%";
    case InsightUnit.SECONDS:
      return " s";
    case InsightUnit.QUOTA_POINTS:
      return " points";
    default:
      return "";
  }
}

export function evidenceText(ev: SuggestionEvidence): string {
  const base = ev.baseline === undefined ? "" : ` (compared with ${ev.baseline}${unitSuffix(ev.unit)})`;
  const n = ev.sampleSize;
  return `${metricText(ev.metric)}: ${ev.value}${unitSuffix(ev.unit)}${base} · ${n} goal${n === 1 ? "" : "s"} or window${n === 1 ? "" : "s"} behind it`;
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
