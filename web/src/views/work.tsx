// Work view (row 14): the work board by stage with provider chips, the
// change requests no goal owns, and the goal list with phase and first
// line. One Watch stream over WORK_ITEMS, CHANGE_REQUESTS and GOALS,
// deduped by (id, version). Read-only except RequeueWorkItem: a button on
// manual-only cards that a click is allowed to requeue.
import { useMemo, useState, type JSX } from "react";
import type { Goal } from "../../../gen/ts/fleet/hub/v1/goal_pb.ts";
import { GoalPhase } from "../../../gen/ts/fleet/hub/v1/goal_pb.ts";
import type {
  ChangeRequest,
  WorkItem,
} from "../../../gen/ts/fleet/hub/v1/work_pb.ts";
import { WorkStage } from "../../../gen/ts/fleet/hub/v1/work_pb.ts";
import type { WatchResponse } from "../../../gen/ts/fleet/hub/v1/watch_pb.ts";
import {
  ChangeType,
  WatchTopic,
} from "../../../gen/ts/fleet/hub/v1/watch_pb.ts";
import {
  canRequeue,
  changeRequestKey,
  checksText,
  columnFor,
  goalKey,
  goalWords,
  manualReasonText,
  providerName,
  requeueWorkItem,
  workItemKey,
  COLUMNS,
} from "./work-helpers.ts";
import { useWatch } from "../watch.ts";
import styles from "./work.module.css";

function GoalLine({ goal }: { goal: Goal | undefined }): JSX.Element | null {
  if (goal?.status === undefined) {
    return null;
  }
  const first = goal.status.firstLine;
  return (
    <p className={styles.goal}>
      Goal {goalWords(goal.status.phase)}
      {first !== "" ? `: ${first}` : ""}
    </p>
  );
}

function WorkCard({
  item,
  goal,
  requeue,
  onRequeue,
}: {
  item: WorkItem;
  goal: Goal | undefined;
  requeue: string | null;
  onRequeue: () => void;
}): JSX.Element {
  const ref = item.item?.ref;
  const claims = item.item?.claims ?? [];
  const repos = item.item?.repos ?? [];
  const by = claims.find((c) => c.by !== undefined)?.by;
  return (
    <li className={styles.card}>
      <p className={styles.title}>
        {ref?.display !== undefined && ref.display !== "" ? ref.display : workItemKey(item)}
        {ref !== undefined && <span className={styles.chip}>{providerName(ref.provider)}</span>}
      </p>
      <p>{item.item?.title ?? "(no title)"}</p>
      {repos.length > 0 && (
        <p className={styles.meta}>
          code: {repos.map((r) => `${r.host}/${r.path}`).join(", ")}
        </p>
      )}
      {by !== undefined && (
        <p className={styles.meta}>
          claimed by {by.login !== "" ? by.login : by.displayName}
        </p>
      )}
      {item.stage === WorkStage.MANUAL_ONLY && (
        <p className={styles.meta}>{manualReasonText(item.manualReason)}</p>
      )}
      <GoalLine goal={goal} />
      {canRequeue(item.stage, item.manualReason) && item.id !== undefined && (
        <div>
          <button type="button" onClick={onRequeue}>
            Put it back in the queue
          </button>
          {requeue !== null && <p role="alert">{requeue}</p>}
        </div>
      )}
      <details>
        <summary>Details</summary>
        <p className={styles.meta}>
          stage {WorkStage[item.stage]} · version {String(item.version)}
        </p>
      </details>
    </li>
  );
}

export function WorkView(): JSX.Element {
  const [items, setItems] = useState<Map<string, WorkItem>>(new Map());
  const [changes, setChanges] = useState<Map<string, ChangeRequest>>(new Map());
  const [goals, setGoals] = useState<Map<string, Goal>>(new Map());
  const [done, setDone] = useState({ items: false, changes: false, goals: false });
  const [requeues, setRequeues] = useState<Map<string, string>>(new Map());
  const state = useWatch(
    useMemo(
      () => [
        { topic: WatchTopic.WORK_ITEMS, sendInitial: true },
        { topic: WatchTopic.CHANGE_REQUESTS, sendInitial: true },
        { topic: WatchTopic.GOALS, sendInitial: true },
      ],
      [],
    ),
    useMemo(
      () => ({
        onResponse: (msg: WatchResponse) => {
          if (
            msg.topic !== WatchTopic.WORK_ITEMS &&
            msg.topic !== WatchTopic.CHANGE_REQUESTS &&
            msg.topic !== WatchTopic.GOALS
          ) {
            return;
          }
          if (msg.changeType === ChangeType.INITIAL_DONE) {
            if (msg.topic === WatchTopic.WORK_ITEMS) {
              setDone((prev) => ({ ...prev, items: true }));
            } else if (msg.topic === WatchTopic.CHANGE_REQUESTS) {
              setDone((prev) => ({ ...prev, changes: true }));
            } else {
              setDone((prev) => ({ ...prev, goals: true }));
            }
            return;
          }
          if (msg.resource.case === "workItem") {
            const item = msg.resource.value;
            setItems((prev) => {
              const next = new Map(prev);
              const key = workItemKey(item);
              if (msg.changeType === ChangeType.DELETED) {
                next.delete(key);
              } else {
                const seen = next.get(key);
                if (seen === undefined || item.version >= seen.version) {
                  next.set(key, item);
                }
              }
              return next;
            });
          } else if (msg.resource.case === "changeRequest") {
            const change = msg.resource.value;
            setChanges((prev) => {
              const next = new Map(prev);
              const key = changeRequestKey(change);
              if (msg.changeType === ChangeType.DELETED) {
                next.delete(key);
              } else {
                const seen = next.get(key);
                if (seen === undefined || change.version >= seen.version) {
                  next.set(key, change);
                }
              }
              return next;
            });
          } else if (msg.resource.case === "goal") {
            const goal = msg.resource.value;
            setGoals((prev) => {
              const next = new Map(prev);
              const key = goalKey(goal);
              if (msg.changeType === ChangeType.DELETED) {
                next.delete(key);
              } else {
                const seen = next.get(key);
                if (seen === undefined || goal.version >= seen.version) {
                  next.set(key, goal);
                }
              }
              return next;
            });
          }
        },
        onResync: () => {
          setItems(new Map());
          setChanges(new Map());
          setGoals(new Map());
          setDone({ items: false, changes: false, goals: false });
        },
      }),
      [],
    ),
  );

  const initialDone = done.items && done.changes && done.goals;
  const columns = useMemo(() => {
    const grouped = new Map<string, WorkItem[]>();
    for (const item of items.values()) {
      const column = columnFor(item.stage);
      if (column === null) {
        continue;
      }
      const list = grouped.get(column) ?? [];
      list.push(item);
      grouped.set(column, list);
    }
    for (const list of grouped.values()) {
      list.sort((a, b) => workItemKey(a).localeCompare(workItemKey(b)));
    }
    return grouped;
  }, [items]);

  const unowned = [...changes.values()]
    .filter((c) => (c.goal?.value ?? "") === "")
    .sort((a, b) => changeRequestKey(a).localeCompare(changeRequestKey(b)));
  const goalList = [...goals.values()].sort((a, b) =>
    goalKey(a).localeCompare(goalKey(b)),
  );

  if (state === "denied") {
    return (
      <section>
        <h1>Work</h1>
        <p role="alert">
          The hub refused access: sign-in expired or permission revoked. Nothing shown is live.
        </p>
      </section>
    );
  }
  if (state === "reconnecting" && columns.size === 0 && !initialDone) {
    return (
      <section aria-busy="true">
        <h1>Work</h1>
        <p>Connecting to the hub…</p>
      </section>
    );
  }

  async function requeue(item: WorkItem): Promise<void> {
    if (item.id === undefined) {
      return;
    }
    const key = workItemKey(item);
    try {
      await requeueWorkItem(item.id, item.version);
      setRequeues((prev) => {
        const next = new Map(prev);
        next.delete(key);
        return next;
      });
    } catch (err) {
      setRequeues((prev) => {
        const next = new Map(prev);
        next.set(
          key,
          `Could not put it back in the queue: ${err instanceof Error ? err.message : "unknown error"}`,
        );
        return next;
      });
    }
  }

  return (
    <section>
      <h1>Work</h1>
      {state === "reconnecting" && <p role="status">Reconnecting…</p>}
      {columns.size === 0 && unowned.length === 0 ? (
        <p>{initialDone ? "No work items." : "Loading…"}</p>
      ) : (
        <>
          {COLUMNS.map((column) => {
            const list = columns.get(column) ?? [];
            if (list.length === 0) {
              return null;
            }
            return (
              <section key={column} aria-label={column}>
                <h2>
                  {column} ({list.length})
                </h2>
                <ul className={styles.list}>
                  {list.map((item) => (
                    <WorkCard
                      key={workItemKey(item)}
                      item={item}
                      goal={
                        item.goal?.value === undefined || item.goal.value === ""
                          ? undefined
                          : goals.get(item.goal.value)
                      }
                      requeue={requeues.get(workItemKey(item)) ?? null}
                      onRequeue={() => void requeue(item)}
                    />
                  ))}
                </ul>
              </section>
            );
          })}
          {unowned.length > 0 && (
            <section aria-label="Change requests with no goal">
              <h2>Change requests with no goal ({unowned.length})</h2>
              <ul className={styles.list}>
                {unowned.map((change) => {
                  const ref = change.changeRequest?.ref;
                  return (
                    <li key={changeRequestKey(change)} className={styles.card}>
                      <p className={styles.title}>
                        {ref?.display !== undefined && ref.display !== ""
                          ? ref.display
                          : changeRequestKey(change)}
                        {ref !== undefined && (
                          <span className={styles.chip}>{providerName(ref.provider)}</span>
                        )}
                      </p>
                      <p>{change.changeRequest?.title ?? "(no title)"}</p>
                      <p className={styles.meta}>
                        CI: {checksText(change.changeRequest?.checks)}
                        {change.changeRequest?.mergedTime !== undefined &&
                          " · merged"}
                      </p>
                    </li>
                  );
                })}
              </ul>
            </section>
          )}
        </>
      )}
      <section aria-label="Goals">
        <h2>Goals ({goalList.length})</h2>
        {goalList.length === 0 ? (
          <p>{initialDone ? "No goals." : "Loading…"}</p>
        ) : (
          <ul className={styles.list}>
            {goalList.map((goal) => (
              <li key={goalKey(goal)} className={styles.card}>
                <p className={styles.meta}>{goalKey(goal)}</p>
                <p>
                  {goal.status === undefined
                    ? "unknown"
                    : goalWords(goal.status.phase)}
                  {goal.status?.firstLine !== undefined &&
                    goal.status.firstLine !== "" &&
                    `: ${goal.status.firstLine}`}
                </p>
                {goal.status !== undefined && (
                  <details>
                    <summary>Details</summary>
                    <p className={styles.meta}>
                      phase {GoalPhase[goal.status.phase]} · version{" "}
                      {String(goal.version)}
                    </p>
                  </details>
                )}
              </li>
            ))}
          </ul>
        )}
      </section>
    </section>
  );
}
