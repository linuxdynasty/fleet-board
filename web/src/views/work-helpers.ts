// Work board data shaping (row 14): keys, columns, plain goal words,
// manual-only reasons, and the requeue call. No rendering here.
import { create } from "@bufbuild/protobuf";
import type { Goal } from "../../../gen/ts/fleet/hub/v1/goal_pb.ts";
import { GoalPhase } from "../../../gen/ts/fleet/hub/v1/goal_pb.ts";
import type {
  ChangeRequest,
  WorkItem,
} from "../../../gen/ts/fleet/hub/v1/work_pb.ts";
import {
  RequeueWorkItemRequestSchema,
  WorkManualReason,
  WorkStage,
} from "../../../gen/ts/fleet/hub/v1/work_pb.ts";
import type { WorkItemKey } from "../../../gen/ts/fleet/type/v1/work_pb.ts";
import { ProviderKind } from "../../../gen/ts/fleet/type/v1/work_pb.ts";
import type { CheckSummary } from "../../../gen/ts/fleet/integration/v1/model_pb.ts";
import { fleetClient } from "../client.ts";

export function workItemKey(item: WorkItem): string {
  const id = item.id;
  if (id === undefined) {
    return "";
  }
  let scope = "";
  if (id.integration?.value !== undefined && id.integration.value !== "") {
    scope = id.integration.value;
  } else if (id.providerName !== "") {
    scope = `~${id.providerName}`;
  }
  if (scope === "" && id.project === "" && id.key === "") {
    return "";
  }
  return `${scope}/${id.kind}/${id.project}/${id.key}`;
}

export function changeRequestKey(change: ChangeRequest): string {
  const id = change.id;
  if (id === undefined) {
    return "";
  }
  const scope =
    id.integration?.value !== undefined && id.integration.value !== ""
      ? id.integration.value
      : `~${id.providerName}`;
  if (scope === "~" && id.project === "" && id.key === "") {
    return "";
  }
  return `${scope}/${id.kind}/${id.project}/${id.key}`;
}

export function goalKey(goal: Goal): string {
  return goal.id?.value ?? "";
}

export const COLUMNS: string[] = [
  "Ready",
  "Manual only",
  "Claimed elsewhere",
  "Starting",
  "Working",
  "Verifying",
  "Closed",
];

// columnFor maps a hub stage to its board column. SEEN (mirrored, not yet
// evaluated) and UNSPECIFIED are not shown.
export function columnFor(stage: WorkStage): string | null {
  switch (stage) {
    case WorkStage.READY:
      return "Ready";
    case WorkStage.MANUAL_ONLY:
      return "Manual only";
    case WorkStage.CLAIMED_ELSEWHERE:
      return "Claimed elsewhere";
    case WorkStage.DISPATCHED:
      return "Starting";
    case WorkStage.RUNNING:
      return "Working";
    case WorkStage.DONE_CLAIMED:
    case WorkStage.DISAGREE:
      return "Verifying";
    case WorkStage.CLOSED:
      return "Closed";
    default:
      return null;
  }
}

// goalWords says what a goal phase means in plain words.
export function goalWords(phase: GoalPhase): string {
  switch (phase) {
    case GoalPhase.STARTING:
    case GoalPhase.START_UNKNOWN:
      return "starting";
    case GoalPhase.RUNNING:
    case GoalPhase.STOPPED:
      return "working";
    case GoalPhase.WAITING:
      return "waiting on CI";
    case GoalPhase.BLOCKED_OWNER:
      return "blocked on a person";
    case GoalPhase.STUCK:
    case GoalPhase.PANE_LOST:
      return "stuck";
    case GoalPhase.COOLDOWN:
      return "cooling down";
    case GoalPhase.PAUSED:
      return "paused";
    case GoalPhase.LIMIT_WAIT:
      return "waiting for the limit to reset";
    case GoalPhase.VERIFYING:
      return "done? checking with the code host";
    case GoalPhase.DISAGREE:
      return "done? the code host disagrees";
    case GoalPhase.MERGED:
      return "merged";
    case GoalPhase.START_FAILED:
      return "could not start";
    case GoalPhase.START_EXHAUSTED:
      return "could not start after retries";
    case GoalPhase.CLEARED:
      return "cleared by a person";
    case GoalPhase.CANCELLED:
      return "cancelled";
    case GoalPhase.ABANDONED:
      return "abandoned";
    case GoalPhase.EXPIRED:
      return "expired";
    case GoalPhase.CLOSED:
      return "closed";
    default:
      return "unknown";
  }
}

export function manualReasonText(reason: WorkManualReason): string {
  switch (reason) {
    case WorkManualReason.UNTRUSTED:
      return "not trusted: needs a label from someone on the allow-list";
    case WorkManualReason.GOAL_CLEARED:
      return "you cleared the goal";
    case WorkManualReason.START_EXHAUSTED:
      return "three starts failed to start";
    case WorkManualReason.EDITED_AFTER_TRUST:
      return "edited after it was trusted: needs a fresh label";
    default:
      return "waiting for a person";
  }
}

// canRequeue is true where a click may put the item back: manual-only, and
// never past the trusted-issues gate (untrusted or edited-after-trust
// needs a label from an allow-listed person instead).
export function canRequeue(stage: WorkStage, reason: WorkManualReason): boolean {
  return (
    stage === WorkStage.MANUAL_ONLY &&
    (reason === WorkManualReason.GOAL_CLEARED ||
      reason === WorkManualReason.START_EXHAUSTED)
  );
}

export function providerName(provider: ProviderKind): string {
  switch (provider) {
    case ProviderKind.GITHUB:
      return "GitHub";
    case ProviderKind.GITLAB:
      return "GitLab";
    case ProviderKind.JIRA:
      return "Jira";
    case ProviderKind.BITBUCKET:
      return "Bitbucket";
    default:
      return "provider";
  }
}

export function checksText(checks: CheckSummary | undefined): string {
  if (checks === undefined) {
    return "no checks reported";
  }
  if (checks.passing === 0 && checks.failing === 0 && checks.pending === 0) {
    return "no checks yet";
  }
  return `${checks.passing} passing · ${checks.failing} failing · ${checks.pending} pending`;
}

export async function requeueWorkItem(
  id: WorkItemKey,
  expectedVersion: bigint,
): Promise<void> {
  await fleetClient().requeueWorkItem(
    create(RequeueWorkItemRequestSchema, {
      requestId: crypto.randomUUID(),
      id,
      expectedVersion,
    }),
  );
}
