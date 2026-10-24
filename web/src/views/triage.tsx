// Triage view (row 14): the hub's open triage feed, newest most urgent
// first. Read-only except Snooze and Dismiss; prompt answers and the
// special cards arrive in the follow-up slice. Data: the TRIAGE watch
// topic, SnoozeTriageItem and DismissTriageItem for the two buttons.
import { useCallback, useEffect, useMemo, useRef, useState, type FormEvent, type JSX } from "react";
import { Code, ConnectError } from "@connectrpc/connect";
import { create } from "@bufbuild/protobuf";
import { TRIAGE_MOVE_EVENT, clampSelection, moveSelection, triageMoveDelta } from "../keys.ts";
import {
  DismissTriageItemRequestSchema,
  Severity,
  SnoozeTriageItemRequestSchema,
  TriageItemSchema,
  TriageKind,
  type TriageItem,
} from "../../../gen/ts/fleet/hub/v1/triage_pb.ts";
import { ChangeType, WatchTopic } from "../../../gen/ts/fleet/hub/v1/watch_pb.ts";
import { fleetClient, setApprovalSecret } from "../client.ts";
import { useWatch } from "../watch.ts";
import styles from "./triage.module.css";

export function severityText(s: Severity): string {
  switch (s) {
    case Severity.CRITICAL:
      return "urgent";
    case Severity.ACTION:
      return "needs you";
    case Severity.WARNING:
      return "warning";
    case Severity.INFO:
      return "info";
    default:
      return "unknown";
  }
}

function severityRank(s: Severity): number {
  switch (s) {
    case Severity.CRITICAL:
      return 4;
    case Severity.ACTION:
      return 3;
    case Severity.WARNING:
      return 2;
    case Severity.INFO:
      return 1;
    default:
      return 0;
  }
}

// kindTitle names the kinds this slice renders, in plain words. A kind
// with no special card yet returns null and the card shows the item's
// own title line (ui-contract-map, "Triage kinds").
export function kindTitle(kind: TriageKind): string | null {
  switch (kind) {
    case TriageKind.PROMPT:
      return "asking a question";
    case TriageKind.CONDITION:
      return "hit an error or limit";
    case TriageKind.STUCK:
      return "stuck goal";
    case TriageKind.BLOCKED_OWNER:
      return "waiting on you";
    case TriageKind.DONE_DISAGREEMENT:
      return "done, but the code host disagrees";
    case TriageKind.PACING:
      return "usage or pacing";
    case TriageKind.SYSTEM:
      return "hub or machine health";
    case TriageKind.INTEGRATION_ACCESS_LOST:
      return "lost code host access";
    case TriageKind.START_FAILED:
      return "could not start";
    case TriageKind.WORK_ITEM_CHANGED:
      return "work item changed under a goal";
    case TriageKind.LIMIT_WAIT:
      return "waiting on a usage limit";
    default:
      return null;
  }
}

export function detailText(item: TriageItem): string | null {
  const detail = item.detail;
  if (detail.case === "prompt" || detail.case === "condition" || detail.case === "note") {
    return detail.value.text === "" ? null : detail.value.text;
  }
  return null;
}

function itemTime(item: TriageItem): number {
  const at = item.createTime;
  if (at === undefined) {
    return Number.MAX_SAFE_INTEGER;
  }
  return Number(at.seconds) * 1000 + Math.floor(at.nanos / 1e6);
}

// orderItems sorts the feed the way the contract map reads: severity
// first, then age (oldest first within one severity).
export function orderItems(items: TriageItem[]): TriageItem[] {
  return [...items].sort((a, b) => {
    const rank = severityRank(b.severity) - severityRank(a.severity);
    if (rank !== 0) {
      return rank;
    }
    return itemTime(a) - itemTime(b);
  });
}

function itemKey(item: TriageItem): string {
  return item.fingerprint !== "" ? item.fingerprint : (item.id?.value ?? "");
}

type ActState =
  | { status: "idle" }
  | { status: "busy" }
  | { status: "needs-secret" }
  | { status: "error"; message: string };

function TriageCard({
  item,
  selected,
  selectedRef,
}: {
  item: TriageItem;
  selected: boolean;
  selectedRef?: (el: HTMLLIElement | null) => void;
}): JSX.Element {
  const [act, setAct] = useState<ActState>({ status: "idle" });
  const [secret, setSecret] = useState("");
  const kind = kindTitle(item.kind);
  const detail = detailText(item);

  const run = async (action: "snooze" | "dismiss"): Promise<void> => {
    setAct({ status: "busy" });
    try {
      const base = {
        requestId: crypto.randomUUID(),
        id: item.id ?? create(TriageItemSchema, {}).id,
        expectedVersion: item.version,
      };
      if (action === "snooze") {
        // One quiet day; the watch update removes the card meanwhile.
        const until = new Date(Date.now() + 86400000);
        await fleetClient().snoozeTriageItem(
          create(SnoozeTriageItemRequestSchema, {
            ...base,
            untilTime: {
              $typeName: "google.protobuf.Timestamp",
              seconds: BigInt(Math.floor(until.getTime() / 1000)),
              nanos: 0,
            },
          }),
        );
      } else {
        await fleetClient().dismissTriageItem(
          create(DismissTriageItemRequestSchema, { ...base, note: "" }),
        );
      }
      setAct({ status: "idle" });
    } catch (err) {
      if (err instanceof ConnectError && err.code === Code.PermissionDenied) {
        // Usually the local_trusted hub asking for its approval secret
        // (ADR 0032); sometimes a missing scope. Either way the owner
        // types the secret and retries, so ask for it, don't just fail.
        setAct({ status: "needs-secret" });
      } else {
        setAct({ status: "error", message: err instanceof Error ? err.message : String(err) });
      }
    }
  };

  const saveSecret = (event: FormEvent): void => {
    event.preventDefault();
    setApprovalSecret(secret);
    setSecret("");
    setAct({ status: "idle" });
  };

  return (
    <li
      ref={selectedRef}
      className={selected ? `${styles.card} ${styles.selected}` : styles.card}
      aria-current={selected ? "true" : undefined}
    >
      <p className={styles.head}>
        <span className={styles.chip}>{severityText(item.severity)}</span>{" "}
        {kind ?? "triage item"}
      </p>
      <p className={styles.title}>{item.title === "" ? "No title." : item.title}</p>
      {detail !== null && <p className={styles.detail}>{detail}</p>}
      <div className={styles.actions}>
        <button
          type="button"
          disabled={act.status === "busy"}
          onClick={() => {
            void run("snooze");
          }}
        >
          Snooze a day
        </button>
        <button
          type="button"
          disabled={act.status === "busy"}
          onClick={() => {
            void run("dismiss");
          }}
        >
          Dismiss
        </button>
      </div>
      {act.status === "error" && <p role="alert">That did not go through: {act.message}</p>}
      {act.status === "needs-secret" && (
        <form onSubmit={saveSecret}>
          <p role="alert">
            The hub refused that action. If this hub runs in local-trusted mode, type its
            local approval secret, then try again.
          </p>
          <label>
            Approval secret{" "}
            <input
              type="password"
              value={secret}
              autoComplete="off"
              onChange={(event) => {
                setSecret(event.target.value);
              }}
            />
          </label>{" "}
          <button type="submit">Save secret</button>
        </form>
      )}
    </li>
  );
}

export function TriageView(): JSX.Element {
  const [rows, setRows] = useState<Map<string, TriageItem>>(new Map());
  const [initialDone, setInitialDone] = useState(false);
  // The card j/k moves through. The shell fires one event per press;
  // the count ref keeps the handler clamped to the latest feed.
  const [selected, setSelected] = useState(0);
  const countRef = useRef(0);
  useEffect(() => {
    const onMove = (event: Event): void => {
      const delta = triageMoveDelta(event);
      if (delta === null) {
        return;
      }
      // Clamp the stored index first: the feed can shrink between
      // presses, stranding the selection past the end.
      setSelected((prev) => moveSelection(clampSelection(prev, countRef.current), delta, countRef.current));
    };
    window.addEventListener(TRIAGE_MOVE_EVENT, onMove);
    return () => {
      window.removeEventListener(TRIAGE_MOVE_EVENT, onMove);
    };
  }, []);
  const state = useWatch(
    // The feed shows open items only (ListTriageItems defaults the same
    // way). A snoozed or dismissed card leaves the filter, arriving once
    // as UNMATCHED so the view drops it (02c, "Leaving a filter").
    useMemo(() => [{ topic: WatchTopic.TRIAGE, sendInitial: true, filter: "state = OPEN" }], []),
    useMemo(
      () => ({
        onResponse: (msg) => {
          if (msg.topic !== WatchTopic.TRIAGE) {
            return;
          }
          if (msg.changeType === ChangeType.INITIAL_DONE) {
            setInitialDone(true);
            return;
          }
          const triage = msg.resource.case === "triageItem" ? msg.resource.value : undefined;
          if (triage === undefined) {
            return;
          }
          setRows((prev) => {
            const next = new Map(prev);
            if (msg.changeType === ChangeType.DELETED || msg.changeType === ChangeType.UNMATCHED) {
              next.delete(itemKey(triage));
            } else {
              const key = itemKey(triage);
              const seen = next.get(key);
              if (seen === undefined || triage.version >= seen.version) {
                next.set(key, triage);
              }
            }
            return next;
          });
        },
        onResync: () => {
          setRows(new Map());
          setInitialDone(false);
        },
      }),
      [],
    ),
  );

  const list = orderItems([...rows.values()]);
  useEffect(() => {
    countRef.current = list.length;
  });
  // One stable callback, so the ref only attaches when the selection
  // actually moves cards; attaching scrolls the new card into view.
  const scrollToSelected = useCallback((el: HTMLLIElement | null) => {
    el?.scrollIntoView({ block: "nearest" });
  }, []);
  const shown = list.length === 0 ? 0 : Math.min(selected, list.length - 1);
  if (state === "denied") {
    return (
      <section>
        <h1>Triage</h1>
        <p role="alert">
          The hub refused access: sign-in expired or permission revoked. Nothing shown is live.
        </p>
      </section>
    );
  }
  if (state === "reconnecting" && list.length === 0 && !initialDone) {
    return (
      <section aria-busy="true">
        <h1>Triage</h1>
        <p>Connecting to the hub…</p>
      </section>
    );
  }
  return (
    <section>
      <h1>Triage</h1>
      {state === "reconnecting" && <p role="status">Reconnecting…</p>}
      {list.length === 0 ? (
        <p>{initialDone ? "Nothing needs you." : "Loading…"}</p>
      ) : (
        <ul className={styles.list}>
          {list.map((i, idx) => (
            <TriageCard
              key={itemKey(i)}
              item={i}
              selected={idx === shown}
              selectedRef={idx === shown ? scrollToSelected : undefined}
            />
          ))}
        </ul>
      )}
    </section>
  );
}
