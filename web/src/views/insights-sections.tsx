// Insights sections (row 14, issue #42): the presentational blocks of the
// Insights view. Pure rendering over the rollup responses; data fetching
// and the run-report action live in insights.tsx.
import { useState, type FormEvent, type JSX } from "react";
import { create } from "@bufbuild/protobuf";
import {
  InsightDimension,
  type AccountEfficiency,
  type OutcomeRow,
  type RoutineOutcomeRow,
} from "../../../gen/ts/fleet/hub/v1/insights_pb.ts";
import type {
  ConditionRow,
  ParserFailureRow,
  PromptBlockRow,
  ProviderIncident,
  StartFailureRow,
  StuckInsights,
  StuckReasonRow,
} from "../../../gen/ts/fleet/hub/v1/insights_stuck_pb.ts";
import {
  DismissSuggestionRequestSchema,
  type Suggestion,
} from "../../../gen/ts/fleet/hub/v1/suggestion_pb.ts";
import { fleetClient, setApprovalSecret } from "../client.ts";
import {
  capName,
  conditionKindText,
  deniedOf,
  durationText,
  evidenceText,
  groupName,
  percentValue,
  perGoalText,
  promptClassText,
  rateText,
  reasonCodeText,
  reportBlocks,
  sampleText,
  stuckReasonText,
  windowLine,
} from "./insights-helpers.ts";
import styles from "./insights.module.css";

export function SecretForm({ onSaved }: { onSaved: () => void }): JSX.Element {
  const [secret, setSecret] = useState("");
  const save = (event: FormEvent): void => {
    event.preventDefault();
    setApprovalSecret(secret);
    setSecret("");
    onSaved();
  };
  return (
    <form onSubmit={save}>
      <p role="alert">
        The hub refused that action. If this hub runs in local-trusted mode, type its local
        approval secret, then try again.
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
  );
}

export function ReportText({ text }: { text: string }): JSX.Element {
  return (
    <>
      {reportBlocks(text).map((b, i) =>
        b.kind === "heading" ? (
          <h4 key={i}>{b.text}</h4>
        ) : b.kind === "bullet" ? (
          <p key={i} className={styles.bullet}>
            • {b.text}
          </p>
        ) : (
          <p key={i}>{b.text}</p>
        ),
      )}
    </>
  );
}

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

export function OutcomesSection({
  rows,
  dimension,
}: {
  rows: OutcomeRow[];
  dimension: InsightDimension;
}): JSX.Element {
  if (rows.length === 0) {
    return <p>No goals ended in this range.</p>;
  }
  return (
    <>
      <table className={styles.table}>
        <thead>
          <tr>
            <th scope="col">Group</th>
            <th scope="col">Ran</th>
            <th scope="col">Merged</th>
            <th scope="col">Time to merge (usual / slowest 1 in 10)</th>
            <th scope="col">Hints per goal</th>
            <th scope="col">Stuck share</th>
            <th scope="col">Reviews per goal</th>
            <th scope="col">Restarts per goal</th>
            <th scope="col">Quota per merged change</th>
          </tr>
        </thead>
        <tbody>
          {rows.map((r, i) => {
            const p50 = durationText(r.timeToMergeP50);
            const p90 = durationText(r.timeToMergeP90);
            const perMerged =
              r.quotaPointsPerMerged === undefined
                ? "–"
                : `${r.quotaPointsPerMerged.toFixed(1)}${r.ran < 5 ? ` over ${sampleText(r.ran, "goal")}` : ""}`;
            return (
              <tr key={i}>
                <td>
                  {groupName(r.group, dimension)}
                  {r.lowConfidence ? " (low confidence)" : ""}
                </td>
                <td>{sampleText(r.ran, "goal")}</td>
                <td>{rateText(r.ran, r.mergeRatePercent)}</td>
                <td>{p50 === null || p90 === null ? "–" : `${p50} / ${p90}`}</td>
                <td>{perGoalText(r.nudgesTotal, r.ran)}</td>
                <td>{rateText(r.ran, r.stuckRatePercent)}</td>
                <td>{perGoalText(r.reviewRoundsTotal, r.ran)}</td>
                <td>{perGoalText(r.restartsTotal, r.ran)}</td>
                <td>{perMerged}</td>
              </tr>
            );
          })}
        </tbody>
      </table>
      <p className={styles.line}>
        A goal that never ran or was cancelled is counted, but not in the rates. Quota points are
        comparable inside one account only.
      </p>
      <details>
        <summary>Details</summary>
        <p>
          GetInsights{"{sections: OUTCOMES, group_by}"} → OutcomeRow{"{ran, merged, merge_rate_percent, time_to_merge_p50, p90, nudges_total, ever_stuck, low_confidence}"}
        </p>
      </details>
    </>
  );
}

export function StuckSection({ stuck }: { stuck: StuckInsights | undefined }): JSX.Element {
  if (stuck === undefined) {
    return <p>Nothing recorded in this range.</p>;
  }
  const prompts: PromptBlockRow[] = stuck.prompts;
  const conditions: ConditionRow[] = stuck.conditions;
  const incidents: ProviderIncident[] = stuck.incidents;
  const parsers: ParserFailureRow[] = stuck.parserFailures;
  const starts: StartFailureRow[] = stuck.startFailures;
  const reasons: StuckReasonRow[] = stuck.stuckReasons;
  const empty =
    prompts.length + conditions.length + incidents.length + parsers.length + starts.length + reasons.length === 0;
  if (empty) {
    return <p>Nothing recorded in this range.</p>;
  }
  return (
    <div className={styles.cards}>
      <div>
        <h4>Waiting on a person</h4>
        <ul className={styles.sub}>
          {prompts.map((p, i) => (
            <li key={i}>
              {capName(p.harness?.name ?? "")} {promptClassText(p.promptClass)}:{" "}
              {sampleText(p.prompts, "prompt")} on {sampleText(p.goalsAffected, "goal")}, waited{" "}
              {durationText(p.waitedTotal) ?? "unknown time"}
              {p.destructive ? " (always waits for a person)" : ""}
            </li>
          ))}
          {prompts.length === 0 && <li>None.</li>}
        </ul>
      </div>
      <div>
        <h4>Provider trouble</h4>
        <ul className={styles.sub}>
          {conditions.map((c, i) => (
            <li key={i}>
              {capName(c.harness?.name ?? "")} {conditionKindText(c.kind)}
              {c.provider === "" ? "" : ` (${c.provider})`}: {c.occurrences} times on{" "}
              {sampleText(c.goalsAffected, "goal")}
            </li>
          ))}
          {incidents.map((pc, i) => (
            <li key={`i${i}`}>
              Incident on {pc.provider === "" ? "the provider" : pc.provider}:{" "}
              {sampleText(pc.goalsAffected, "goal")} on {sampleText(pc.machinesAffected, "machine")}
            </li>
          ))}
          {conditions.length + incidents.length === 0 && <li>None.</li>}
        </ul>
      </div>
      <div>
        <h4>After updates and on start</h4>
        <ul className={styles.sub}>
          {parsers.map((p, i) => (
            <li key={i}>
              {capName(p.harness?.name ?? "")} usage parsing failed {p.failures} of {p.readings}{" "}
              readings on {p.harnessVersion === "" ? "an unknown version" : p.harnessVersion}
              {p.afterUpdate ? " (soon after the update)" : ""}
            </li>
          ))}
          {starts.map((s, i) => (
            <li key={i}>
              {s.machine?.value ?? "(unknown machine)"} · {capName(s.harness?.name ?? "")}:{" "}
              {sampleText(s.attempts, "start attempt")} failed ({reasonCodeText(s.reason)}) on{" "}
              {sampleText(s.goals, "goal")}
            </li>
          ))}
          {reasons.map((r, i) => (
            <li key={`r${i}`}>
              {capName(r.harness?.name ?? "")} stuck ({stuckReasonText(r.reason)}):{" "}
              {sampleText(r.goals, "goal")}
            </li>
          ))}
          {parsers.length + starts.length + reasons.length === 0 && <li>None.</li>}
        </ul>
      </div>
      <details>
        <summary>Details</summary>
        <p>
          GetInsights{"{sections: STUCK}"} → StuckInsights{"{prompts[], conditions[], incidents[], parser_failures[], start_failures[], stuck_reasons[]}"}
        </p>
      </details>
    </div>
  );
}

export function RoutinesSection({ rows }: { rows: RoutineOutcomeRow[] }): JSX.Element {
  if (rows.length === 0) {
    return <p>No routine runs ended in this range.</p>;
  }
  return (
    <table className={styles.table}>
      <thead>
        <tr>
          <th scope="col">Routine</th>
          <th scope="col">Runs</th>
          <th scope="col">Done</th>
          <th scope="col">Failed</th>
          <th scope="col">Skipped fires</th>
          <th scope="col">Typical run time</th>
          <th scope="col">Quota spent</th>
        </tr>
      </thead>
      <tbody>
        {rows.map((r, i) => (
          <tr key={i}>
            <td>{r.routine?.value ?? "(unknown)"}</td>
            <td>{sampleText(r.runs, "run")}</td>
            <td>{r.runsDone}</td>
            <td>{r.runsFailed}</td>
            <td>{r.skippedFires}</td>
            <td>{durationText(r.runTimeP50) ?? "–"}</td>
            <td>{r.quotaPointsTotal.toFixed(1)} points</td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}

export function SuggestionCard({
  suggestion,
  onDone,
}: {
  suggestion: Suggestion;
  onDone: () => void;
}): JSX.Element {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [needsSecret, setNeedsSecret] = useState(false);

  const dismiss = async (): Promise<void> => {
    setBusy(true);
    setError(null);
    try {
      await fleetClient().dismissSuggestion(
        create(DismissSuggestionRequestSchema, {
          requestId: crypto.randomUUID(),
          id: suggestion.id,
          expectedVersion: suggestion.version,
        }),
      );
      onDone();
    } catch (err) {
      if (deniedOf(err)) {
        setNeedsSecret(true);
      } else {
        setError(err instanceof Error ? err.message : String(err));
      }
    } finally {
      setBusy(false);
    }
  };

  return (
    <li>
      <p className={styles.head}>{suggestion.title === "" ? "Suggestion" : suggestion.title}</p>
      <ul className={styles.sub}>
        {suggestion.evidence.map((ev, i) => (
          <li key={i}>{evidenceText(ev)}</li>
        ))}
      </ul>
      <button
        type="button"
        disabled={busy}
        onClick={() => {
          void dismiss();
        }}
      >
        Dismiss
      </button>
      {error !== null && <p role="alert">That did not go through: {error}</p>}
      {needsSecret && (
        <SecretForm
          onSaved={() => {
            setNeedsSecret(false);
          }}
        />
      )}
      <details>
        <summary>Details</summary>
        <p>
          Suggestion{"{kind, evidence[]}"} · DismissSuggestion{"{id, expected_version}"}
        </p>
      </details>
    </li>
  );
}
