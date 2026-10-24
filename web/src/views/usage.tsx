// Usage view (row 14): per-subscription waste lines, the as-shown tables,
// burn-down charts and source lines. Data: Watch ACCOUNTS for the live
// pacing and usage, ListUsageReadings per account for chart history.
// Plain words first; raw contract names live behind details toggles.
import { useEffect, useMemo, useRef, useState, type JSX } from "react";
import { useQuery } from "@tanstack/react-query";
import { create } from "@bufbuild/protobuf";
import uPlot from "uplot";
import type { Timestamp } from "@bufbuild/protobuf/wkt";
import type { Account } from "../../../gen/ts/fleet/hub/v1/account_pb.ts";
import { PacingState } from "../../../gen/ts/fleet/hub/v1/account_pb.ts";
import { ChangeType, WatchTopic } from "../../../gen/ts/fleet/hub/v1/watch_pb.ts";
import {
  ListUsageReadingsRequestSchema,
  type UsageReading,
} from "../../../gen/ts/fleet/hub/v1/account_pb.ts";
import type { Percent } from "../../../gen/ts/fleet/type/v1/status_pb.ts";
import { UsageSource, type LimitBucket, type ResetTime } from "../../../gen/ts/fleet/type/v1/usage_pb.ts";
import { fleetClient } from "../client.ts";
import { useWatch } from "../watch.ts";
import styles from "./usage.module.css";

function millis(ts: Timestamp | undefined): number | null {
  if (ts === undefined) {
    return null;
  }
  return Number(ts.seconds) * 1000;
}

function accountKey(a: Account): string {
  return a.id?.value ?? "";
}

export function percentText(p: Percent | undefined): string | null {
  if (p === undefined) {
    return null;
  }
  if (p.kind.case === "value") {
    return `${p.kind.value}%`;
  }
  return null;
}

export function resetText(resetsTime: ResetTime | undefined, nowMs: number): string {
  if (resetsTime === undefined) {
    return "reset unknown";
  }
  const resolved = millis(resetsTime.resolvedTime);
  if (resolved === null) {
    return resetsTime.originalText === "" ? "reset unknown" : `resets ${resetsTime.originalText}`;
  }
  const at = new Date(resolved);
  if (resetsTime.dateOnly) {
    // The contract says the UI shows original_text, not a countdown: the
    // resolved midnight belongs to the next day by design (usage.proto).
    return resetsTime.originalText === "" ? "reset unknown" : resetsTime.originalText;
  }
  const diffMs = at.getTime() - nowMs;
  if (diffMs <= 0) {
    return "reset passed";
  }
  const days = Math.floor(diffMs / 86400000);
  const hours = Math.floor((diffMs % 86400000) / 3600000);
  if (days > 0) {
    return `resets in ${days}d ${hours}h`;
  }
  return `resets in ${hours}h`;
}

export function bucketDisplay(
  bucket: LimitBucket | undefined,
): { label: "left" | "used"; text: string | null } {
  if (bucket === undefined) {
    return { label: "used", text: null };
  }
  // percent_used always stores the used share; a harness that showed
  // remaining has it converted, so "left" displays 100 minus used.
  const label = bucket.shownAsRemaining ? "left" : "used";
  const used =
    bucket.percentUsed?.kind.case === "value" ? bucket.percentUsed.kind.value : null;
  if (used === null) {
    return { label, text: null };
  }
  return { label, text: `${bucket.shownAsRemaining ? 100 - used : used}%` };
}

export function wasteLine(a: Account, nowMs: number, unknown: boolean): string {
  const pacing = a.status?.pacing;
  const parts = [`${a.spec?.displayName ?? accountKey(a)}`];
  const bucket = a.status?.usage?.buckets?.[0];
  if (unknown) {
    // Cached buckets may still hold old numbers; never present them as live.
    parts.push("usage unknown");
  } else {
    const shown = bucketDisplay(bucket);
    parts.push(shown.text === null ? "usage unknown" : `${shown.text} ${shown.label}`);
    parts.push(resetText(bucket?.resetsTime, nowMs));
  }
  parts.push(`${pacing?.activeGoals ?? 0} goal running`);
  const projected = pacing?.projectedUnusedPercent;
  if (!unknown && projected !== undefined) {
    parts.push(`on pace to leave ~${projected}% unused`);
  }
  return parts.join(" · ");
}

export function isHighlighted(a: Account): boolean {
  const pacing = a.status?.pacing;
  if (pacing === undefined) {
    return false;
  }
  return (pacing.projectedUnusedPercent ?? 0) >= 5 || pacing.idleWhileBehind;
}

function SourceLine({ account, nowMs }: { account: Account; nowMs: number }): JSX.Element {
  const usage = account.status?.usage;
  const source = usage?.source;
  const readMs = millis(usage?.readTime);
  const ago =
    readMs === null
      ? "never read"
      : `${Math.max(0, Math.round((nowMs - readMs) / 60000))} min ago`;
  let name = "unknown";
  if (source === UsageSource.PROBE) {
    name = "probe";
  } else if (source === UsageSource.PASSIVE) {
    name = "passive";
  }
  return (
    <p className={styles.source}>
      source: {name} {ago}
    </p>
  );
}

function BurnChart({ readings }: { readings: UsageReading[] }): JSX.Element {
  const hostRef = useRef<HTMLDivElement | null>(null);
  const points = useMemo(() => {
    const xs: number[] = [];
    const ys: number[] = [];
    for (const r of readings) {
      const bucket = r.buckets[0];
      const value =
        bucket?.percentUsed?.kind.case === "value" ? bucket.percentUsed.kind.value : null;
      const at = millis(r.readTime);
      if (value === null || at === null) {
        continue;
      }
      xs.push(at);
      ys.push(value);
    }
    return { xs, ys };
  }, [readings]);

  useEffect(() => {
    const host = hostRef.current;
    if (host === null || points.xs.length < 2) {
      return;
    }
    const plot = new uPlot(
      {
        width: Math.max(280, host.clientWidth),
        height: 140,
        series: [{}, { stroke: "CanvasText", width: 1.5 }],
      },
      [points.xs, points.ys],
      host,
    );
    return () => {
      plot.destroy();
    };
  }, [points]);

  if (points.xs.length < 2) {
    return <p className={styles.chart}>Not enough history for a burn-down line yet.</p>;
  }
  return <div ref={hostRef} className={styles.chart} role="img" aria-label="Burn-down chart" />;
}

function ReadingsChart({ accountId }: { accountId: string }): JSX.Element {
  const query = useQuery({
    queryKey: ["usage-readings", accountId],
    queryFn: () =>
      fleetClient().listUsageReadings(
        create(ListUsageReadingsRequestSchema, {
          account: { value: accountId },
        }),
      ),
  });
  if (query.isPending) {
    return <p className={styles.chart}>Loading history…</p>;
  }
  if (query.isError) {
    return <p className={styles.chart}>History is unavailable.</p>;
  }
  return <BurnChart readings={query.data.readings} />;
}

function useNow(): number {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const timer = setInterval(() => {
      setNow(Date.now());
    }, 60000);
    return () => {
      clearInterval(timer);
    };
  }, []);
  return now;
}

function AccountCard({ account }: { account: Account }): JSX.Element {
  const pacing = account.status?.pacing;
  const unknown = pacing?.state === PacingState.UNKNOWN;
  const buckets = account.status?.usage?.buckets ?? [];
  const spend = account.status?.usage?.spend;
  const now = useNow();
  return (
    <li className={isHighlighted(account) ? styles.highlight : undefined}>
      <p className={styles.waste}>{wasteLine(account, now, unknown)}</p>
      {pacing?.readingStale === true && <p role="status">Stale — reset passed, re-probe pending.</p>}
      {unknown ? (
        <p>Unknown: no numbers yet, nothing old shown.</p>
      ) : (
        <table className={styles.shown}>
          <tbody>
            {buckets.map((b, i) => {
            const shown = bucketDisplay(b);
            return (
              <tr key={i}>
                <td>{shown.label}</td>
                <td>{shown.text ?? "unknown"}</td>
                <td>{resetText(b.resetsTime, now)}</td>
              </tr>
            );
          })}
          </tbody>
        </table>
      )}
      {spend !== undefined && (
        <p>
          Spent so far ${(Number(spend.amountMicros) / 1e6).toFixed(2)} {spend.currency}
          {pacing?.state === PacingState.EXCLUDED && ", manual only"}
        </p>
      )}
      <SourceLine account={account} nowMs={now} />
      <ReadingsChart accountId={accountKey(account)} />
      <details>
        <summary>Details</summary>
        <p>
          Plan {account.status?.plan ?? "unknown"} · pacing {pacing === undefined ? "unknown" : PacingState[pacing.state]}
        </p>
      </details>
    </li>
  );
}

export function UsageView(): JSX.Element {
  const [rows, setRows] = useState<Map<string, Account>>(new Map());
  const [initialDone, setInitialDone] = useState(false);
  const state = useWatch(
    useMemo(() => [{ topic: WatchTopic.ACCOUNTS, sendInitial: true }], []),
    useMemo(
      () => ({
        onResponse: (msg) => {
          if (msg.topic !== WatchTopic.ACCOUNTS) {
            return;
          }
          if (msg.changeType === ChangeType.INITIAL_DONE) {
            setInitialDone(true);
            return;
          }
          const account = msg.resource.case === "account" ? msg.resource.value : undefined;
          if (account === undefined) {
            return;
          }
          setRows((prev) => {
            const next = new Map(prev);
            if (msg.changeType === ChangeType.DELETED) {
              next.delete(accountKey(account));
            } else {
              const key = accountKey(account);
              const seen = next.get(key);
              if (seen === undefined || account.version >= seen.version) {
                next.set(key, account);
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

  const list = [...rows.values()];
  if (state === "denied") {
    return (
      <section>
        <h1>Usage</h1>
        <p role="alert">
          The hub refused access: sign-in expired or permission revoked. Nothing shown is live.
        </p>
      </section>
    );
  }
  if (state === "reconnecting" && list.length === 0 && !initialDone) {
    return (
      <section aria-busy="true">
        <h1>Usage</h1>
        <p>Connecting to the hub…</p>
      </section>
    );
  }
  return (
    <section>
      <h1>Usage</h1>
      {state === "reconnecting" && <p role="status">Reconnecting…</p>}
      {list.length === 0 ? (
        <p>{initialDone ? "No accounts." : "Loading…"}</p>
      ) : (
        <ul className={styles.list}>
          {list.map((a) => (
            <AccountCard key={accountKey(a)} account={a} />
          ))}
        </ul>
      )}
    </section>
  );
}
