// Insights sections (row 14, issue #42): the presentational blocks of the
// Insights view. Pure rendering over the efficiency rollup response.
// The outcomes, stuck, routines, report and suggestion sections arrive
// with row 27, which builds them on the hub.
import type { JSX } from "react";
import type { AccountEfficiency } from "../../../gen/ts/fleet/hub/v1/insights_pb.ts";
import { percentValue, sampleText, windowLine } from "./insights-helpers.ts";
import styles from "./insights.module.css";

export function EfficiencySection({ rows }: { rows: AccountEfficiency[] }): JSX.Element {
  if (rows.length === 0) {
    return <p>No subscription windows in this range yet.</p>;
  }
  return (
    <ul className={styles.list}>
      {rows.map((e) => (
        <li key={e.account?.value ?? "unknown"}>
          <p className={styles.head}>Account {e.account?.value ?? "(unknown)"}</p>
          {e.summaries.map((s, i) => {
            const meanUsed = percentValue(s.meanUsableUsed);
            const meanUnused = percentValue(s.meanUnused);
            return (
              <p key={i} className={styles.line}>
                {s.bucketKey === "" ? "quota" : s.bucketKey}: {sampleText(s.windows, "window")},{" "}
                {meanUsed === null ? "average use unknown" : `used ${meanUsed}% of usable on average`}
                {meanUnused === null ? "" : `, left ${meanUnused} points unused`}
                {s.wasteTrendPoints !== undefined && s.wasteTrendPoints !== 0
                  ? `, waste ${s.wasteTrendPoints > 0 ? "growing" : "shrinking"} ${Math.abs(s.wasteTrendPoints)} points per window`
                  : ""}
              </p>
            );
          })}
          <ul className={styles.sub}>
            {e.windows.map((w, i) => (
              <li key={i}>{windowLine(w)}</li>
            ))}
          </ul>
          <details>
            <summary>Details</summary>
            <p>
              GetInsights{"{sections: EFFICIENCY}"} → AccountEfficiency{"{windows[] QuotaWindow, summaries[]}"}
            </p>
          </details>
        </li>
      ))}
    </ul>
  );
}
