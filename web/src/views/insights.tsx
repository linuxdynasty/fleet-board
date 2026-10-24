// Insights view (row 14, issue #42): the hub's efficiency rollups in plain
// words. NOT a Watch topic: reads on open and every few minutes (POLL_MS)
// with a plain unary call. Row 14 serves efficiency only — asking for the
// outcomes, stuck or routines sections fails the whole call, and the
// reports, suggestions and run-report calls land with row 27 — so the view
// asks for EFFICIENCY alone and renders that.
import { useEffect, useMemo, useState, type JSX } from "react";
import { useQuery } from "@tanstack/react-query";
import { create } from "@bufbuild/protobuf";
import {
  GetInsightsRequestSchema,
  InsightGranularity,
  InsightSection,
} from "../../../gen/ts/fleet/hub/v1/insights_pb.ts";
import { fleetClient } from "../client.ts";
import { agoText, dayRangeMs, deniedOf, millis, sampleText } from "./insights-helpers.ts";
import { EfficiencySection } from "./insights-sections.tsx";
import styles from "./insights.module.css";

// Rollups move hourly, so a read every few minutes stays fresh without a stream.
const POLL_MS = 180000;
const RANGES = [7, 30, 90];

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

export function InsightsView(): JSX.Element {
  const [days, setDays] = useState(30);
  const now = useNow();
  const range = useMemo(() => dayRangeMs(days, now), [days, now]);

  const insights = useQuery({
    queryKey: ["insights", days, range.startMs, range.endMs],
    queryFn: () =>
      fleetClient().getInsights(
        create(GetInsightsRequestSchema, {
          range: {
            startTime: { $typeName: "google.protobuf.Timestamp", seconds: BigInt(Math.floor(range.startMs / 1000)), nanos: 0 },
            endTime: { $typeName: "google.protobuf.Timestamp", seconds: BigInt(Math.floor(range.endMs / 1000)), nanos: 0 },
          },
          granularity: InsightGranularity.DAY,
          sections: [InsightSection.EFFICIENCY],
        }),
      ),
    refetchInterval: POLL_MS,
  });

  if (insights.isPending) {
    return (
      <section aria-busy="true">
        <h1>Insights</h1>
        <p>Loading the hub&apos;s numbers…</p>
      </section>
    );
  }
  if (deniedOf(insights.error)) {
    return (
      <section>
        <h1>Insights</h1>
        <p role="alert">
          The hub refused access: sign-in expired or permission revoked. Nothing shown is live.
        </p>
      </section>
    );
  }
  if (insights.error !== null) {
    return (
      <section>
        <h1>Insights</h1>
        <p role="alert">
          The numbers did not load: {insights.error instanceof Error ? insights.error.message : String(insights.error)}
        </p>
        <button
          type="button"
          onClick={() => {
            void insights.refetch();
          }}
        >
          Try again
        </button>
      </section>
    );
  }

  const data = insights.data;
  return (
    <section>
      <h1>Insights</h1>
      <p className={styles.line}>
        Numbers the hub computed from its own rows, no model involved.{" "}
        {data.computedTime !== undefined && <>computed {agoText(millis(data.computedTime), now)}</>}
        {data.dataStartTime !== undefined && (
          <> · history begins {new Date(millis(data.dataStartTime) ?? 0).toLocaleDateString()}</>
        )}
        {data.goalsLive > 0 && <> · {sampleText(data.goalsLive, "goal")} still live, not counted</>}
      </p>
      <div className={styles.controls}>
        <label>
          Range{" "}
          <select
            aria-label="Range"
            value={days}
            onChange={(event) => {
              setDays(Number(event.target.value));
            }}
          >
            {RANGES.map((d) => (
              <option key={d} value={d}>
                {d} days
              </option>
            ))}
          </select>
        </label>
      </div>

      <h3>Are the subscriptions used before they reset?</h3>
      <EfficiencySection rows={data.efficiency} />

      <details>
        <summary>Details</summary>
        <p>
          GetInsights{"{range, granularity: DAY, sections: EFFICIENCY}"} · refetch every 3 minutes ·
          the other sections (outcomes, stuck, routines) arrive with row 27
        </p>
      </details>
    </section>
  );
}
