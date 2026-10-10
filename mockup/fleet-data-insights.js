// Mockup data, part 3: insights (docs/specs/05-insights.md) and system health (05b-observability.md).
// Everything here is SAMPLE (marked `sample` where it is shown): the real build computes it from the hub's rollups.
const insightRange = { label: "last 28 days", computed: ago(18 * 60), since: "2026-07-14" };

// Quota windows per subscription (weekly bucket): `used` = last reading before the reset, percent of the bucket.
// usable = 100 - reserve; unused (waste) = max(0, usable - used); computed in view-insights.js.
const efficiency = [
  { acct: "claude-acct", used: [68, 74, 76, 78], idleBehindH: 0, fleetUsed: 38 },
  { acct: "codex-acct", used: [52, 58, 64, 61], idleBehindH: 31, fleetUsed: 44 },
  { acct: "muse-acct", used: [3, 6, 12, 9], idleBehindH: 96, fleetUsed: 9 },
];

// OutcomeRow per harness (05 section 5). `ran` = merged + cleared; rates are shown only from 5 ran goals.
const outcomes = [
  { harness: "claude", ran: 21, merged: 17, cleared: 4, cancelled: 2, neverRan: 1, stuck: 5, p50: 2.4, p90: 6.1, nudges: 31, restarts: 6, reviews: 29, pts: 112, ptsUnmerged: 14 },
  { harness: "codex", ran: 24, merged: 21, cleared: 3, cancelled: 1, neverRan: 4, stuck: 4, p50: 1.7, p90: 4.2, nudges: 19, restarts: 3, reviews: 22, pts: 96, ptsUnmerged: 9 },
  { harness: "muse", ran: 7, merged: 5, cleared: 2, cancelled: 0, neverRan: 0, stuck: 2, p50: 3.4, p90: 7.8, nudges: 12, restarts: 2, reviews: 9, pts: 21, ptsUnmerged: 6 },
  { harness: "opencode", ran: 3, merged: 2, cleared: 1, cancelled: 0, neverRan: 0, stuck: 1, p50: 4.0, p90: 4.0, nudges: 4, restarts: 0, reviews: 3, pts: 0, ptsUnmerged: 0 },
];

// StuckInsights (05 section 4): counts and durations over closed vocabularies, never text.
const stuck = {
  prompts: [["claude", "run a command", 14, 9, "11 h 40 min"], ["codex", "edit files", 8, 6, "4 h 05 min"], ["claude", "trust a folder", 3, 3, "1 h 10 min"]],
  conditions: [["claude", "rate limited", "anthropic", 6, 5, "3 h 10 min"], ["codex", "usage limit", "openai", 2, 2, "9 h 30 min"], ["muse", "network", "moonshot", 3, 2, "40 min"]],
  incidents: [["anthropic", "rate limited", "Sep 25, 14:10 to 15:02", 6, 2]],
  parser: [["codex", "0.159.2", "parser 3", 3, 20, "0.158.0", true]],
  starts: [["mini", "codex", "HARNESS_VERSION_UNSUPPORTED", 4, 2], ["linux", "claude", "FOLDER_UNTRUSTED", 1, 1]],
};

// The latest weekly report as stored (InsightReport.text). UNTRUSTED text: the view shows it sanitized. Its last line is a
// hostile link on purpose: it must render as plain text, never as a link.
const latestReport = { harness: "codex", model: "gpt-5.3-codex", when: ago(2 * 86400), pts: 3, bucket: "weekly", digest: "9f3a1c07d2be",
  text: ["<!-- fleet-insights bundle=9f3a1c07d2be -->", "# Fleet insights, Sep 14 to Oct 11", "## Summary",
    "- Codex left about 29 points of its weekly quota unused at the last reset; Muse left about 81.",
    "- Claude used all of its usable quota in the last two windows.",
    "- Codex merged 21 of 24 goals; median time to merge 1.7 h.",
    "## Where work got stuck", "- Run-command prompts held goals for 11 h 40 min on Claude.",
    "- A rate-limit incident on anthropic on Sep 25 affected 6 goals.", "[full numbers](javascript:alert(1))"].join("\n") };

const suggestions = [
  { id: "s1", kind: "HARNESS_FASTER", title: "Codex merges perf issues faster than Muse (median 1.6 h vs 3.4 h)", ev: "perf label: codex 8 merged, muse 5 merged · p50 1.6 h vs 3.4 h · needs 5 each" },
  { id: "s2", kind: "NEAR_RESERVE_WEEKDAY", title: "Claude is near its reserve most Thursdays", ev: "within 5 points of the reserve on a Thursday in 3 of the last 4 windows" },
  { id: "s3", kind: "IDLE_BEHIND_NO_WORK", title: "Muse sat idle behind pace for 96 hours: no ready work", ev: "idle_behind 96 h over the last two windows · cause NO_READY_ITEMS · consider a routine" },
];

// SystemSignal (05b section 8), sample values. state: ok | warning | critical | unknown.
// measures[] is SystemMeasure: kind, value (unit; null = not known), unit, direction, warn and crit (the thresholds;
// absent = no such level), warnHold and critHold (seconds; absent = at once), critClear. The levels and holds are the
// real 05b ones; the readings are sample. Every word shown is built from these fields in view-machines.js.
const GiB = 2 ** 30;
const systemSignals = [
  { kind: "WRITER_BATCH_LAG", subject: "hub", state: "ok", measures: [
    { kind: "MAIN", value: 0.04, unit: "SECONDS", direction: "ABOVE", warn: 1, crit: 5 },
    { kind: "WRITER_COMMIT_TIME", value: 0.018, unit: "SECONDS", direction: "ABOVE", warn: 0.25 },
    { kind: "WRITER_QUEUE_FULL", value: 0, unit: "SECONDS", direction: "ABOVE", crit: 30 }] },
  { kind: "OUTBOX_BACKLOG", subject: "hub", state: "ok", measures: [{ kind: "MAIN", value: 0, unit: "SECONDS", direction: "ABOVE", warn: 120, crit: 600 }] },
  { kind: "WATCH_DROPS", subject: "hub", state: "ok", measures: [{ kind: "MAIN", value: 0, unit: "COUNT", direction: "ABOVE", warn: 3, crit: 20 }] },
  { kind: "INTEGRATION_SYNC_LAG", subject: "github-com", state: "ok", measures: [{ kind: "MAIN", value: 42, unit: "SECONDS", direction: "ABOVE", warn: 300, crit: 600 }] },
  { kind: "LOG_STORE_USAGE", subject: "hub", state: "ok", measures: [{ kind: "MAIN", value: 3, unit: "PERCENT", direction: "ABOVE", warn: 100, crit: 100, warnHold: 600, critHold: 3600 }] },
  { kind: "DISK_FREE", subject: "hub", state: "ok", measures: [{ kind: "MAIN", value: 212 * GiB, unit: "BYTES", direction: "BELOW", warn: 10 * GiB, crit: 2 * GiB, critClear: 3 * GiB }] },
  { kind: "BACKUP_AGE", subject: "hub", state: "warning", note: "the nightly `fleetd backup` has not run since Wednesday",
    measures: [{ kind: "MAIN", value: 41 * 3600, unit: "SECONDS", direction: "ABOVE", warn: 36 * 3600, crit: 7 * 86400 }] },
  { kind: "USAGE_PROBE_FRESHNESS", subject: "all accounts", state: "ok", measures: [{ kind: "MAIN", value: 720, unit: "SECONDS", direction: "ABOVE", warn: 1800, crit: 7200 }] },
  { kind: "AGENT_CONNECTED", subject: "always-on machines", state: "ok", measures: [{ kind: "MAIN", value: 4, unit: "SECONDS", direction: "ABOVE", warn: 600, crit: 3600 }] },
  { kind: "CLOCK_SKEW", subject: "machines", state: "ok", measures: [{ kind: "MAIN", value: 18, unit: "SECONDS", direction: "ABOVE", warn: 300, crit: 900 }] },
];
// MachineAgentHealth per machine (sample): ring events, ring MiB, unacked results, reconnects since start, RSS MiB.
const agentHealth = { mbp: [1204, 0.9, 0, 1, 38], linux: [980, 0.7, 0, 0, 41], mbp2: [2210, 1.6, 0, 2, 44], mini: [310, 0.2, 1, 5, 36] };
