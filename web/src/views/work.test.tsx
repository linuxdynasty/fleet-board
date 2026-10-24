// Work view tests (row 14, issue #42): helpers for keys, columns and goal
// lines, plus the view fed through the Watch handlers.
import { create } from "@bufbuild/protobuf";
import { act, cleanup, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { GoalPhase, GoalSchema } from "../../../gen/ts/fleet/hub/v1/goal_pb.ts";
import type { WatchResponse } from "../../../gen/ts/fleet/hub/v1/watch_pb.ts";
import {
  ChangeType,
  WatchResponseSchema,
  WatchTopic,
} from "../../../gen/ts/fleet/hub/v1/watch_pb.ts";
import {
  ChangeRequestSchema,
  WorkItemSchema,
  WorkManualReason,
  WorkStage,
} from "../../../gen/ts/fleet/hub/v1/work_pb.ts";
import type { WatchHandlers } from "../watch.ts";
import { WorkView } from "./work.tsx";
import {
  changeRequestKey,
  columnFor,
  goalKey,
  goalWords,
  manualReasonText,
  workItemKey,
} from "./work-helpers.ts";

const { useWatchMock } = vi.hoisted(() => ({
  useWatchMock: vi.fn(),
}));

vi.mock("../watch.ts", () => ({
  useWatch: (subs: unknown, handlers: WatchHandlers): string =>
    useWatchMock(subs, handlers) as string,
}));

function feed(msg: WatchResponse): void {
  const handlers = useWatchMock.mock.calls.at(-1)?.[1] as WatchHandlers;
  act(() => {
    handlers.onResponse(msg);
  });
}

function initialDone(topic: WatchTopic): WatchResponse {
  return create(WatchResponseSchema, {
    topic,
    changeType: ChangeType.INITIAL_DONE,
  });
}

afterEach(() => {
  cleanup();
  useWatchMock.mockReset();
});

function showView(state: string): void {
  useWatchMock.mockReturnValue(state);
  render(<WorkView />);
}

describe("work keys", () => {
  it("keys a work item by integration, kind, project and key", () => {
    const item = create(WorkItemSchema, {
      id: {
        integration: { value: "github" },
        kind: 1,
        project: "eshu",
        key: "7398",
      },
    });
    expect(workItemKey(item)).toBe("github/1/eshu/7398");
  });

  it("keys an item with no integration by its provider name", () => {
    const item = create(WorkItemSchema, {
      id: { kind: 3, project: "infra", key: "12", providerName: "gitlab" },
    });
    expect(workItemKey(item)).toBe("~gitlab/3/infra/12");
    expect(workItemKey(create(WorkItemSchema, {}))).toBe("");
  });

  it("keys change requests and goals by their ids", () => {
    const change = create(ChangeRequestSchema, {
      id: {
        integration: { value: "github" },
        kind: 3,
        project: "eshu",
        key: "12",
      },
    });
    expect(changeRequestKey(change)).toBe("github/3/eshu/12");
    const goal = create(GoalSchema, { id: { value: "g-1" } });
    expect(goalKey(goal)).toBe("g-1");
    expect(goalKey(create(GoalSchema, {}))).toBe("");
  });
});

describe("board columns", () => {
  it("maps every shown stage to its column and hides mirrored-only items", () => {
    expect(columnFor(WorkStage.READY)).toBe("Ready");
    expect(columnFor(WorkStage.MANUAL_ONLY)).toBe("Manual only");
    expect(columnFor(WorkStage.CLAIMED_ELSEWHERE)).toBe("Claimed elsewhere");
    expect(columnFor(WorkStage.DISPATCHED)).toBe("Starting");
    expect(columnFor(WorkStage.RUNNING)).toBe("Working");
    expect(columnFor(WorkStage.DONE_CLAIMED)).toBe("Verifying");
    expect(columnFor(WorkStage.DISAGREE)).toBe("Verifying");
    expect(columnFor(WorkStage.CLOSED)).toBe("Closed");
    expect(columnFor(WorkStage.SEEN)).toBeNull();
    expect(columnFor(WorkStage.UNSPECIFIED)).toBeNull();
  });
});

describe("goal words", () => {
  it("says what each live phase means in plain words", () => {
    expect(goalWords(GoalPhase.RUNNING)).toBe("working");
    expect(goalWords(GoalPhase.WAITING)).toBe("waiting on CI");
    expect(goalWords(GoalPhase.STUCK)).toBe("stuck");
    expect(goalWords(GoalPhase.PAUSED)).toBe("paused");
    expect(goalWords(GoalPhase.BLOCKED_OWNER)).toBe("blocked on a person");
    expect(goalWords(GoalPhase.LIMIT_WAIT)).toBe("waiting for the limit to reset");
    expect(goalWords(GoalPhase.VERIFYING)).toBe("done? checking with the code host");
    expect(goalWords(GoalPhase.DISAGREE)).toBe("done? the code host disagrees");
    expect(goalWords(GoalPhase.MERGED)).toBe("merged");
  });
});

describe("manual-only reasons", () => {
  it("explains why an item waits for a person", () => {
    expect(manualReasonText(WorkManualReason.GOAL_CLEARED)).toContain("cleared");
    expect(manualReasonText(WorkManualReason.START_EXHAUSTED)).toContain("failed to start");
  });

});

describe("WorkView", () => {
  it("renders items from watch state, newest version wins", () => {
    showView("live");
    feed(
      create(WatchResponseSchema, {
        topic: WatchTopic.WORK_ITEMS,
        changeType: ChangeType.INITIAL,
        resource: {
          case: "workItem",
          value: create(WorkItemSchema, {
            id: {
              integration: { value: "github" },
              kind: 1,
              project: "eshu",
              key: "7398",
            },
            version: 1n,
            stage: WorkStage.READY,
            item: { ref: { display: "eshu#7398" }, title: "Old title" },
          }),
        },
      }),
    );
    feed(
      create(WatchResponseSchema, {
        topic: WatchTopic.WORK_ITEMS,
        changeType: ChangeType.UPDATED,
        resource: {
          case: "workItem",
          value: create(WorkItemSchema, {
            id: {
              integration: { value: "github" },
              kind: 1,
              project: "eshu",
              key: "7398",
            },
            version: 2n,
            stage: WorkStage.READY,
            item: { ref: { display: "eshu#7398" }, title: "New title" },
          }),
        },
      }),
    );
    feed(initialDone(WatchTopic.WORK_ITEMS));
    feed(initialDone(WatchTopic.CHANGE_REQUESTS));
    feed(initialDone(WatchTopic.GOALS));
    expect(screen.getByText("New title")).toBeDefined();
    expect(screen.queryByText("Old title")).toBeNull();
    expect(screen.getByText("eshu#7398")).toBeDefined();
  });

  it("keeps the newer goal status when an older one arrives at the same version", () => {
    showView("live");
    const goalId = { value: "g-9" };
    feed(
      create(WatchResponseSchema, {
        topic: WatchTopic.GOALS,
        changeType: ChangeType.UPDATED,
        resource: {
          case: "goal",
          value: create(GoalSchema, {
            id: goalId,
            version: 1n,
            status: { phase: GoalPhase.RUNNING, hubSeq: 10n, firstLine: "new line" },
          }),
        },
      }),
    );
    feed(
      create(WatchResponseSchema, {
        topic: WatchTopic.GOALS,
        changeType: ChangeType.UPDATED,
        resource: {
          case: "goal",
          value: create(GoalSchema, {
            id: goalId,
            version: 1n,
            status: { phase: GoalPhase.WAITING, hubSeq: 9n, firstLine: "old line" },
          }),
        },
      }),
    );
    feed(initialDone(WatchTopic.WORK_ITEMS));
    feed(initialDone(WatchTopic.CHANGE_REQUESTS));
    feed(initialDone(WatchTopic.GOALS));
    expect(screen.getByText(/new line/)).toBeDefined();
    expect(screen.queryByText(/old line/)).toBeNull();
  });

  it("shows the denied state when the hub refuses access", () => {
    showView("denied");
    expect(screen.getByRole("alert")).toBeDefined();
    expect(screen.getByText(/Nothing shown is live\./)).toBeDefined();
  });
});
