// Machines view (row 14, first slice): the watched machines by display
// name with their state. One Watch stream, deduped by (id, version);
// PANE_SUMMARIES are latest-wins and never queued, so machine rows are
// replaced, never appended.
import { useMemo, useState, type JSX } from "react";
import type {
  Machine,
} from "../../../gen/ts/fleet/hub/v1/machine_pb.ts";
import type {
  WatchResponse,
} from "../../../gen/ts/fleet/hub/v1/watch_pb.ts";
import {
  ChangeType,
  WatchTopic,
} from "../../../gen/ts/fleet/hub/v1/watch_pb.ts";
import { useWatch } from "../watch.ts";
import styles from "./machines.module.css";

function machineKey(m: Machine): string {
  return m.id?.value ?? "";
}

export function MachinesView(): JSX.Element {
  const [rows, setRows] = useState<Map<string, Machine>>(new Map());
  const [initialDone, setInitialDone] = useState(false);
  const state = useWatch(
    useMemo(() => [{ topic: WatchTopic.MACHINES, sendInitial: true }], []),
    useMemo(
      () => ({
        onResponse: (msg: WatchResponse) => {
          if (msg.topic !== WatchTopic.MACHINES) {
            return;
          }
          if (msg.changeType === ChangeType.INITIAL_DONE) {
            setInitialDone(true);
            return;
          }
          const machine = msg.resource.case === "machine" ? msg.resource.value : undefined;
          if (machine === undefined) {
            return;
          }
          setRows((prev) => {
            const next = new Map(prev);
            if (msg.changeType === ChangeType.DELETED) {
              next.delete(machineKey(machine));
            } else {
              const key = machineKey(machine);
              const seen = next.get(key);
              if (seen === undefined || machine.version >= seen.version) {
                next.set(key, machine);
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

  const list = [...rows.values()].sort((a, b) =>
    (a.spec?.displayName ?? "").localeCompare(b.spec?.displayName ?? ""),
  );

  if (state === "reconnecting" && list.length === 0 && !initialDone) {
    return (
      <section aria-busy="true">
        <h1>Machines</h1>
        <p>Connecting to the hub…</p>
      </section>
    );
  }

  return (
    <section>
      <h1>Machines</h1>
      {state === "reconnecting" && <p role="status">Reconnecting…</p>}
      {list.length === 0 ? (
        <p>{initialDone ? "No machines enrolled." : "Loading…"}</p>
      ) : (
        <ul className={styles.list}>
          {list.map((m) => (
            <li key={machineKey(m)}>
              {m.spec?.displayName ?? machineKey(m)}
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}
