// Session view (row 14): the tmux sessions by machine with their windows,
// and each pane's last lines. Read-only: panes show
// PaneSummary.last_lines, and the full visible screen loads on demand
// through CapturePane. No attach controls and nothing that types.
import { useMemo, useState, type JSX } from "react";
import { create } from "@bufbuild/protobuf";
import type {
  Pane,
  PaneSummary,
  Session,
  SessionWindow,
} from "../../../gen/ts/fleet/hub/v1/session_pb.ts";
import { CapturePaneRequestSchema } from "../../../gen/ts/fleet/hub/v1/session_pb.ts";
import {
  GetOperationRequestSchema,
  OperationState,
} from "../../../gen/ts/fleet/hub/v1/common_pb.ts";
import type { WatchResponse } from "../../../gen/ts/fleet/hub/v1/watch_pb.ts";
import {
  ChangeType,
  WatchTopic,
} from "../../../gen/ts/fleet/hub/v1/watch_pb.ts";
import type { PaneKey } from "../../../gen/ts/fleet/type/v1/ids_pb.ts";
import { fleetClient } from "../client.ts";
import { useWatch } from "../watch.ts";
import styles from "./session.module.css";

export function sessionKey(s: Session): string {
  return s.id?.value ?? "";
}

export function paneKeyText(key: PaneKey | undefined): string {
  if (key === undefined) {
    return "";
  }
  return `${key.server}/${key.serverStartId}/${key.paneId}`;
}

export function originTag(startedByFleet: boolean): string {
  return startedByFleet ? "started by fleet" : "yours";
}

export function windowLabel(w: SessionWindow): string {
  return `[${w.index}] ${w.name}`;
}

function machineOf(s: Session): string {
  return s.status?.machine?.value ?? "unknown machine";
}

function sessionName(s: Session): string {
  return s.status?.name && s.status.name !== "" ? s.status.name : sessionKey(s);
}

type CaptureState =
  | { status: "idle" }
  | { status: "loading" }
  | { status: "ready"; text: string }
  | { status: "error" };

function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => {
    setTimeout(resolve, ms);
  });
}

async function captureScreen(
  sessionId: string,
  pane: PaneKey | undefined,
): Promise<string> {
  if (pane === undefined) {
    throw new Error("pane has no key");
  }
  const started = await fleetClient().capturePane(
    create(CapturePaneRequestSchema, {
      requestId: crypto.randomUUID(),
      session: { value: sessionId },
      pane: {
        server: pane.server,
        serverStartId: pane.serverStartId,
        paneId: pane.paneId,
      },
      lastLines: 0,
      keepAnsi: false,
      maxBytes: 65536,
    }),
  );
  const opId = started.operation?.id;
  if (opId === undefined) {
    throw new Error("capture returned no operation");
  }
  for (let attempt = 0; attempt < 20; attempt++) {
    const got = await fleetClient().getOperation(
      create(GetOperationRequestSchema, { id: opId }),
    );
    if (got.operation?.state === OperationState.DONE) {
      return got.operation.outputText;
    }
    await sleep(400);
  }
  throw new Error("capture did not finish");
}

function PaneCard({
  sessionId,
  pane,
  summary,
  capture,
  onCapture,
}: {
  sessionId: string;
  pane: Pane;
  summary: PaneSummary | undefined;
  capture: CaptureState;
  onCapture: () => void;
}): JSX.Element {
  const cmd = pane.currentCommand !== "" ? pane.currentCommand : "idle";
  return (
    <li className={styles.pane}>
      <p className={styles.meta}>
        pane {pane.index}
        {pane.active ? " (active)" : ""} · {cmd}
      </p>
      <pre>{summary?.lastLines ?? "No screen yet."}</pre>
      {capture.status === "idle" && (
        <button type="button" onClick={onCapture}>
          Show full screen
        </button>
      )}
      {capture.status === "loading" && (
        <p role="status">Reading the full screen…</p>
      )}
      {capture.status === "ready" && (
        <pre>{capture.text !== "" ? capture.text : "(empty screen)"}</pre>
      )}
      {capture.status === "error" && <p>The full screen is unavailable.</p>}
      <details>
        <summary>Details</summary>
        <p className={styles.meta}>
          {sessionId}:{pane.index} · pane {paneKeyText(pane.key)}
          {pane.model !== "" ? ` · model line ${pane.model}` : ""}
        </p>
      </details>
    </li>
  );
}

export function SessionView(): JSX.Element {
  const [sessions, setSessions] = useState<Map<string, Session>>(new Map());
  const [summaries, setSummaries] = useState<Map<string, PaneSummary>>(
    new Map(),
  );
  const [initialDone, setInitialDone] = useState(false);
  const [selectedSession, setSelectedSession] = useState<string | null>(null);
  const [selectedWindow, setSelectedWindow] = useState<number | null>(null);
  const [captures, setCaptures] = useState<Map<string, CaptureState>>(
    new Map(),
  );
  const state = useWatch(
    useMemo(
      () => [
        { topic: WatchTopic.SESSIONS, sendInitial: true },
        { topic: WatchTopic.PANE_SUMMARIES, sendInitial: true },
      ],
      [],
    ),
    useMemo(
      () => ({
        onResponse: (msg: WatchResponse) => {
          if (
            msg.topic !== WatchTopic.SESSIONS &&
            msg.topic !== WatchTopic.PANE_SUMMARIES
          ) {
            return;
          }
          if (msg.changeType === ChangeType.INITIAL_DONE) {
            setInitialDone(true);
            return;
          }
          if (msg.resource.case === "session") {
            const session = msg.resource.value;
            setSessions((prev) => {
              const next = new Map(prev);
              if (msg.changeType === ChangeType.DELETED) {
                next.delete(sessionKey(session));
              } else {
                const key = sessionKey(session);
                const seen = next.get(key);
                if (seen === undefined || session.version >= seen.version) {
                  next.set(key, session);
                }
              }
              return next;
            });
          } else if (msg.resource.case === "paneSummary") {
            const summary = msg.resource.value;
            setSummaries((prev) => {
              const next = new Map(prev);
              const key = paneKeyText(summary.pane);
              if (msg.changeType === ChangeType.DELETED) {
                next.delete(key);
              } else {
                const seen = next.get(key);
                if (seen === undefined || summary.version >= seen.version) {
                  next.set(key, summary);
                }
              }
              return next;
            });
          }
        },
        onResync: () => {
          setSessions(new Map());
          setSummaries(new Map());
          setInitialDone(false);
        },
      }),
      [],
    ),
  );

  const list = [...sessions.values()].sort(
    (a, b) =>
      machineOf(a).localeCompare(machineOf(b)) ||
      sessionName(a).localeCompare(sessionName(b)),
  );
  const current =
    list.find((s) => sessionKey(s) === selectedSession) ?? list.at(0);
  const windows = current?.status?.windows ?? [];
  const activeWindow =
    windows.find((w) => w.index === selectedWindow) ??
    windows.find((w) => w.active) ??
    windows.at(0);

  if (state === "reconnecting" && list.length === 0 && !initialDone) {
    return (
      <section aria-busy="true">
        <h1>Session</h1>
        <p>Connecting to the hub…</p>
      </section>
    );
  }

  return (
    <section>
      <h1>Session</h1>
      {state === "reconnecting" && <p role="status">Reconnecting…</p>}
      <p className={styles.meta}>
        Read-only: this view shows screens but never types into a pane.
      </p>
      {list.length === 0 ? (
        <p>{initialDone ? "No sessions." : "Loading…"}</p>
      ) : (
        <div className={styles.layout}>
          <nav className={styles.side} aria-label="Tmux sessions">
            <ul>
              {list.map((s) => (
                <li key={sessionKey(s)}>
                  <p className={styles.group}>
                    {machineOf(s)} · {sessionName(s)}
                    <span className={styles.tag}>
                      {originTag(s.status?.startedByFleet === true)}
                    </span>
                  </p>
                  <ul>
                    {(s.status?.windows ?? []).map((w) => (
                      <li key={w.index}>
                        <button
                          type="button"
                          aria-current={
                            current !== undefined &&
                            sessionKey(s) === sessionKey(current) &&
                            activeWindow?.index === w.index
                              ? "true"
                              : undefined
                          }
                          onClick={() => {
                            setSelectedSession(sessionKey(s));
                            setSelectedWindow(w.index);
                          }}
                        >
                          {windowLabel(w)}
                        </button>
                      </li>
                    ))}
                  </ul>
                </li>
              ))}
            </ul>
          </nav>
          <div className={styles.main}>
            {current === undefined || activeWindow === undefined ? (
              <p>No windows.</p>
            ) : (
              <>
                <h2>
                  [{sessionName(current)}] {activeWindow.name}
                </h2>
                <ul className={styles.tabs}>
                  {windows.map((w) => (
                    <li key={w.index}>
                      <button
                        type="button"
                        aria-current={w.index === activeWindow.index}
                        onClick={() => {
                          setSelectedSession(sessionKey(current));
                          setSelectedWindow(w.index);
                        }}
                      >
                        {windowLabel(w)}
                      </button>
                    </li>
                  ))}
                </ul>
                <ul className={styles.tabs}>
                  {activeWindow.panes.map((pane) => {
                    const key = paneKeyText(pane.key);
                    const capture = captures.get(key) ?? { status: "idle" };
                    return (
                      <PaneCard
                        key={key}
                        sessionId={sessionKey(current)}
                        pane={pane}
                        summary={summaries.get(key)}
                        capture={capture}
                        onCapture={() => {
                          const paneKey = pane.key;
                          setCaptures((prev) => {
                            const next = new Map(prev);
                            next.set(key, { status: "loading" });
                            return next;
                          });
                          void captureScreen(sessionKey(current), paneKey)
                            .then((text) => {
                              setCaptures((prev) => {
                                const next = new Map(prev);
                                next.set(key, { status: "ready", text });
                                return next;
                              });
                            })
                            .catch(() => {
                              setCaptures((prev) => {
                                const next = new Map(prev);
                                next.set(key, { status: "error" });
                                return next;
                              });
                            });
                        }}
                      />
                    );
                  })}
                </ul>
              </>
            )}
          </div>
        </div>
      )}
    </section>
  );
}
