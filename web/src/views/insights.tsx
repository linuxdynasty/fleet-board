// Insights view (row 14, issue #42): the hub's own rollups in plain words.
// NOT a Watch topic: reads on open and every few minutes (POLL_MS) with
// plain unary calls. GetInsights for EFFICIENCY/OUTCOMES/STUCK/ROUTINES,
// ListInsightReports for the newest weekly report, ListSuggestions with
// DismissSuggestion, and RunRoutine on the built-in weekly routine.
// Read-only except DismissSuggestion. Report text is untrusted model
// output: parsed into text blocks and rendered as text, never as HTML.
import { useEffect, useMemo, useState, type JSX } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { create } from "@bufbuild/protobuf";
import {
  GetInsightsRequestSchema,
  InsightDimension,
  InsightGranularity,
  InsightSection,
} from "../../../gen/ts/fleet/hub/v1/insights_pb.ts";
import {
  ListSuggestionsRequestSchema,
  SuggestionState,
} from "../../../gen/ts/fleet/hub/v1/suggestion_pb.ts";
import {
  GetInsightReportRequestSchema,
  ListInsightReportsRequestSchema,
  type InsightReport,
} from "../../../gen/ts/fleet/hub/v1/insight_report_pb.ts";
import {
  ListRoutinesRequestSchema,
  RunRoutineRequestSchema,
} from "../../../gen/ts/fleet/hub/v1/routine_pb.ts";
import { fleetClient } from "../client.ts";
import { agoText, capName, deniedOf, millis, rangeStartMs, sampleText } from "./insights-helpers.ts";
import {
  EfficiencySection,
  OutcomesSection,
  ReportText,
  RoutinesSection,
  SecretForm,
  StuckSection,
  SuggestionCard,
} from "./insights-sections.tsx";
import styles from "./insights.module.css";

// Rollups move hourly, so a read every few minutes stays fresh without a stream.
const POLL_MS = 180000;
const RANGES = [7, 30, 90];
const DIMENSIONS: { value: InsightDimension; label: string }[] = [
  { value: InsightDimension.HARNESS, label: "harness" },
  { value: InsightDimension.MODEL, label: "model" },
  { value: InsightDimension.REPO, label: "repo" },
  { value: InsightDimension.ACCOUNT, label: "account" },
  { value: InsightDimension.MACHINE, label: "machine" },
];

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
  const [groupBy, setGroupBy] = useState<InsightDimension>(InsightDimension.HARNESS);
  const [reportSel, setReportSel] = useState<{ id: string; numbers: boolean } | null>(null);
  const [runMsg, setRunMsg] = useState<string | null>(null);
  const [runBusy, setRunBusy] = useState(false);
  const [runNeedsSecret, setRunNeedsSecret] = useState(false);
  const queryClient = useQueryClient();

  // The range edges are floored to the poll interval so the query key
  // stays still between background refetches.
  const now = useNow();
  const range = useMemo(() => {
    const endMs = Math.floor(now / POLL_MS) * POLL_MS;
    return {
      startMs: rangeStartMs(days, endMs),
      endMs,
    };
  }, [days, now]);

  const insights = useQuery({
    queryKey: ["insights", days, groupBy, range.startMs, range.endMs],
    queryFn: () =>
      fleetClient().getInsights(
        create(GetInsightsRequestSchema, {
          range: {
            startTime: { $typeName: "google.protobuf.Timestamp", seconds: BigInt(Math.floor(range.startMs / 1000)), nanos: 0 },
            endTime: { $typeName: "google.protobuf.Timestamp", seconds: BigInt(Math.floor(range.endMs / 1000)), nanos: 0 },
          },
          granularity: InsightGranularity.DAY,
          sections: [
            InsightSection.EFFICIENCY,
            InsightSection.OUTCOMES,
            InsightSection.STUCK,
            InsightSection.ROUTINES,
          ],
          groupBy,
        }),
      ),
    refetchInterval: POLL_MS,
  });

  const reports = useQuery({
    queryKey: ["insight-reports"],
    queryFn: () =>
      fleetClient().listInsightReports(create(ListInsightReportsRequestSchema, { pageSize: 1 })),
    refetchInterval: POLL_MS,
  });

  const suggestions = useQuery({
    queryKey: ["suggestions"],
    queryFn: () =>
      fleetClient().listSuggestions(
        create(ListSuggestionsRequestSchema, { filter: "state = OPEN" }),
      ),
    refetchInterval: POLL_MS,
  });

  const fullReport = useQuery({
    queryKey: ["insight-report", reportSel?.id ?? "", reportSel?.numbers ?? false],
    queryFn: () => {
      if (reportSel === null) {
        throw new Error("no report selected");
      }
      return fleetClient().getInsightReport(
        create(GetInsightReportRequestSchema, {
          id: { value: reportSel.id },
          includeBundle: reportSel.numbers,
        }),
      );
    },
    enabled: reportSel !== null,
  });

  const runReport = async (): Promise<void> => {
    setRunBusy(true);
    setRunMsg(null);
    try {
      const routines = await fleetClient().listRoutines(
        create(ListRoutinesRequestSchema, { filter: "builtin = INSIGHTS_WEEKLY", pageSize: 1 }),
      );
      const routine = routines.routines[0];
      if (routine?.id === undefined) {
        setRunMsg("No weekly report routine found.");
        return;
      }
      const res = await fleetClient().runRoutine(
        create(RunRoutineRequestSchema, {
          requestId: crypto.randomUUID(),
          id: routine.id,
          acknowledgedWarnings: [],
        }),
      );
      if (res.approval?.value !== undefined && res.approval.value !== "") {
        setRunMsg("Waiting on your confirm in Triage.");
      } else if (res.goal?.value !== undefined && res.goal.value !== "") {
        setRunMsg(`Started a run (goal ${res.goal.value}).`);
      } else {
        setRunMsg("The hub accepted the run.");
      }
    } catch (err) {
      if (deniedOf(err)) {
        setRunNeedsSecret(true);
      } else {
        setRunMsg(err instanceof Error ? err.message : String(err));
      }
    } finally {
      setRunBusy(false);
    }
  };

  if (insights.isPending) {
    return (
      <section aria-busy="true">
        <h1>Insights</h1>
        <p>Loading the hub&apos;s numbers…</p>
      </section>
    );
  }
  const firstError = insights.error ?? reports.error ?? suggestions.error;
  if (firstError !== null && deniedOf(insights.error ?? firstError)) {
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
  const latest: InsightReport | undefined = reports.data?.reports[0];
  const openSuggestions = (suggestions.data?.suggestions ?? []).filter(
    (s) => s.state === SuggestionState.OPEN,
  );
  const cost = (latest?.usageConsumed ?? []).reduce((sum, u) => sum + u.percentPoints, 0);

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
        </label>{" "}
        <label>
          Group outcomes by{" "}
          <select
            aria-label="Group outcomes by"
            value={groupBy}
            onChange={(event) => {
              setGroupBy(Number(event.target.value) as InsightDimension);
            }}
          >
            {DIMENSIONS.map((d) => (
              <option key={d.value} value={d.value}>
                {d.label}
              </option>
            ))}
          </select>
        </label>
      </div>

      {suggestions.isError && !deniedOf(suggestions.error) && (
        <p role="alert">Suggestions did not load.</p>
      )}
      {openSuggestions.length > 0 && (
        <section aria-label="Suggestions">
          <h3>Suggestions</h3>
          <p className={styles.line}>Rules over the numbers below; they advise, never change a setting.</p>
          <ul className={styles.list}>
            {openSuggestions.map((s) => (
              <SuggestionCard
                key={s.id?.value ?? s.title}
                suggestion={s}
                onDone={() => {
                  void queryClient.invalidateQueries({ queryKey: ["suggestions"] });
                }}
              />
            ))}
          </ul>
        </section>
      )}

      <h3>Are the subscriptions used before they reset?</h3>
      <EfficiencySection rows={data.efficiency} />

      <h3>What finished</h3>
      <OutcomesSection rows={data.outcomes} dimension={groupBy} />

      <h3>Where work gets stuck</h3>
      <p className={styles.line}>Counts and times; never prompt or screen text.</p>
      <StuckSection stuck={data.stuck} />

      <h3>Routine runs</h3>
      <RoutinesSection rows={data.routines} />

      <h3>Latest weekly report</h3>
      {reports.isPending && <p>Loading the report…</p>}
      {reports.isError && <p role="alert">The report did not load.</p>}
      {reports.data !== undefined && latest === undefined && <p>No report written yet.</p>}
      {latest !== undefined && (
        <div className={styles.report}>
          <p className={styles.line}>
            Written by {capName(latest.harness?.name ?? "")} {latest.model === "" ? "(no model named)" : latest.model}{" "}
            {agoText(millis(latest.createTime), now)} · cost {cost} quota points, spare quota only
          </p>
          <p className={styles.line}>
            Model-written text, shown as plain text: no HTML, no images, links shown with their
            address. The numbers it was written from are one click away.
          </p>
          <ReportText text={latest.excerpt === "" ? "(empty excerpt)" : latest.excerpt} />
          <div className={styles.controls}>
            <button
              type="button"
              onClick={() => {
                setReportSel(latest.id === undefined ? null : { id: latest.id.value, numbers: false });
              }}
            >
              Full report
            </button>{" "}
            <button
              type="button"
              onClick={() => {
                setReportSel(latest.id === undefined ? null : { id: latest.id.value, numbers: true });
              }}
            >
              Show the numbers
            </button>{" "}
            <button
              type="button"
              disabled={runBusy}
              onClick={() => {
                void runReport();
              }}
            >
              Run the report now
            </button>
          </div>
          <p className={styles.line}>
            Weekly, Monday 07:00 · runs only when an account is behind pace or would waste quota,
            never into your reserve.
          </p>
          {runMsg !== null && <p role="status">{runMsg}</p>}
          {runNeedsSecret && (
            <SecretForm
              onSaved={() => {
                setRunNeedsSecret(false);
              }}
            />
          )}
          {reportSel !== null && fullReport.data !== undefined && (
            <>
              <ReportText text={fullReport.data.report?.text ?? ""} />
              {reportSel.numbers && (
                <>
                  <pre className={styles.bundle}>{fullReport.data.report?.bundleJson ?? ""}</pre>
                  <ul className={styles.sub}>
                    {(fullReport.data.report?.aliases ?? []).map((a, i) => (
                      <li key={i}>
                        {a.alias} stands for {a.machine?.value ?? a.account?.value ?? "(unknown)"}
                      </li>
                    ))}
                  </ul>
                </>
              )}
            </>
          )}
          {fullReport.isError && <p role="alert">The full report did not load.</p>}
          <details>
            <summary>Details</summary>
            <p>
              InsightReport{"{excerpt, text, usage_consumed, bundle_digest}"} · ListInsightReports,
              GetInsightReport{"{include_bundle}"} · routine ROUTINE_BUILTIN_INSIGHTS_WEEKLY ·
              only_spare_quota
            </p>
          </details>
        </div>
      )}
    </section>
  );
}
