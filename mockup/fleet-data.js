// Mockup data. Real: machines, RAM, tools, weekly usage (status-command probes
// 2026-09-28 ~20:35 EDT), PRs, issues, merges. Sample: goals, goal-file lines, audit.
const T0 = Date.now();
const ago = s => new Date(T0 - s * 1000);
const at = iso => new Date(iso).getTime();

const machines = [
  { id: "mbp", name: "Linuxdynasty-MacBook-Pro-14", short: "mbp", ip: "192.168.1.118", user: "asanabria", os: "macOS 27 · arm64", ram: 64, hub: true,
    harnesses: ["claude", "codex", "muse"], maxActive: 2, maxTotal: 4, repoRoot: "~/repos", tmux: "fleet", beat: ago(2), drained: false },
  { id: "omarchy", name: "omarchy", short: "omarchy", ip: "192.168.1.150", user: "linuxdynasty", os: "Linux 7.2 · x86_64", ram: 62,
    harnesses: ["claude", "codex", "muse"], maxActive: 2, maxTotal: 4, repoRoot: "~/repos", tmux: "0", beat: ago(4), drained: false },
  { id: "allen", name: "allens-Apple-MacBook-Pro", short: "allen", ip: "192.168.1.232", user: "allen", os: "macOS 25.6 · arm64", ram: 128,
    harnesses: ["claude", "codex", "muse"], maxActive: 4, maxTotal: 8, repoRoot: "~/repos", tmux: "fleet", beat: ago(3), drained: false },
  { id: "m124", name: "ip-192-168-1-124", short: ".124", ip: "192.168.1.124", user: "linuxdynasty", os: "macOS 27 · arm64", ram: 64,
    harnesses: ["claude", "codex", "muse"], maxActive: 2, maxTotal: 4, repoRoot: "~/repos", tmux: "fleet", beat: ago(6), drained: false },
];

// One entry per subscription account. Usage is shared by every machine signed in to it.
// `buckets` lists every weekly limit the status screen reports; the highest one binds.
const accounts = [
  { id: "claude-max", harness: "claude", label: "Claude Code", reserve: 25, source: "/usage",
    buckets: [{ label: "all models", used: 81 }, { label: "Fable", used: 53 }],
    short: { label: "session", used: 10, resets: at("2026-09-28T23:40:00-04:00") },
    resets: at("2026-09-30T22:00:00-04:00"), windowDays: 7, read: ago(60 * 60) },
  { id: "chatgpt-pro", harness: "codex", label: "Codex", reserve: 10, source: "/status",
    buckets: [{ label: "weekly", used: 42 }], short: null,
    resets: at("2026-10-04T21:29:00-04:00"), windowDays: 7, read: ago(62 * 60) },
  { id: "muse-power", harness: "muse", label: "Muse", reserve: 10, source: "/usage",
    buckets: [{ label: "weekly", used: 2 }], short: { label: "current", used: 0, resets: at("2026-09-28T21:06:00-04:00") },
    resets: at("2026-10-04T20:00:00-04:00"), windowDays: 7, read: ago(60 * 60) },
];

const settings = {
  repos: [{ repo: "eshu-hq/eshu", on: true }, { repo: "CigarsOnly/virtual-humidor", on: false }],
  consent: ["push", "pr-open", "merge"],
  autoDispatch: true,
};

// Goals: one per dispatched session. `line` is the first line of the goal file.
const sessions = [
  { id: "g1", m: "omarchy", harness: "claude", target: "0:1", repo: "eshu-hq/eshu", issue: 7380, pr: 7412, branch: "perf/7380-entity-anchor-plans",
    line: "BLOCKED: waiting on CI for #7412 WATCH=48213", watchAlive: true, last: ago(40), started: ago(5 * 3600),
    tasks: [["Claim #7380", 1], ["Reproduce the slow anchor loop", 1], ["Replace with one label-set statement", 1], ["Benchmark before/after", 1], ["Open PR #7412", 1], ["CI green", 0], ["Review rounds", 0], ["Merge", 0]],
    pane: [["dim","● Bash(gh pr checks 7412 --repo eshu-hq/eshu --watch)"],["","  ⎿  54 passing · 1 pending · 1 failing (retrying)"],["",""],["warn","● Goal: BLOCKED: waiting on CI for #7412 WATCH=48213"],["dim","  watcher 48213 alive · wakes on check completion"]] },
  { id: "g2", m: "omarchy", harness: "codex", target: "0:2", repo: "eshu-hq/eshu", issue: 7251, pr: 7410, branch: "perf/7251-graph-summary",
    line: "Drive eshu-hq/eshu#7251 to merged", last: ago(15), started: ago(3 * 3600),
    tasks: [["Claim #7251", 1], ["Aggregate Function degrees in Neo4j", 1], ["Open PR #7410", 1], ["Fix failing check", 0], ["Review rounds", 0], ["Merge", 0]],
    pane: [["","• gh pr checks 7410: 1 failing (go-test-integration)"],["","• Reading failure log"],["hl","• Working (1m 12s · esc to interrupt)"]] },
  { id: "g3", m: "allen", harness: "muse", target: "fleet:1", repo: "CigarsOnly/virtual-humidor", issue: 780, pr: 794, branch: "fix/780-db-test-leak-detector",
    line: "BLOCKED: TEST_DATABASE_URL not reachable from this machine", watchAlive: false, last: ago(14 * 60 + 20), started: ago(80 * 60),
    tasks: [["Claim #780", 1], ["Write row-leak detector", 1], ["Open PR #794", 1], ["Run gated DB tests", 0], ["CI green", 0], ["Merge", 0]],
    pane: [["","> go test ./humidor-service/... -run Leak"],["bad","    dial tcp 127.0.0.1:55432: connect: connection refused"],["warn","Goal: BLOCKED: TEST_DATABASE_URL not reachable from this machine"],["dim","(no watcher armed · no output for 14m)"]] },
  { id: "g4", m: "allen", harness: "muse", target: "fleet:2", repo: "eshu-hq/eshu", issue: 7407, pr: null, branch: "fix/7407-failure-details-bound",
    line: "Drive eshu-hq/eshu#7407 to merged", last: ago(25), started: ago(35 * 60), perm: "p1",
    tasks: [["Claim #7407", 1], ["Find Fail-time writer", 1], ["Add size bound + test", 0], ["Open PR", 0], ["CI green", 0], ["Merge", 0]],
    pane: [["","> Bash: rm -rf go/internal/queue/testdata/fixtures"],["",""],["bad"," Allow this command?"],[""," ❯ 1. Allow once"],["","   2. Allow for this session"],["","   3. Deny"]] },
  { id: "g5", m: "mbp", harness: "claude", target: "fleet:0", repo: "eshu-hq/eshu", issue: 6696, pr: 6927, branch: "feat/6696-service-singular",
    line: "DONE", last: ago(6 * 60), started: ago(9 * 3600),
    tasks: [["Claim #6696", 1], ["Rename service leaf", 1], ["Open PR #6927", 1], ["CI green", 1], ["Merge", 1]],
    pane: [["ok","● Goal marked DONE"],["dim","  #6927 merged, #6696 closed"]] },
  { id: "g6", m: "m124", harness: "codex", target: "fleet:0", repo: "eshu-hq/eshu", issue: null, pr: 7411, branch: "codex-agent-spawn-guard",
    line: "BLOCKED: waiting for review on #7411 WATCH=9120", watchAlive: true, last: ago(18 * 60), started: ago(6 * 3600),
    tasks: [["Guard Codex spawns with named roles", 1], ["Open PR #7411", 1], ["CI green", 1], ["Review", 0], ["Merge", 0]],
    pane: [["","• PR #7411: 83 passing · 1 pending"],["warn","Goal: BLOCKED: waiting for review on #7411 WATCH=9120"]] },
  { id: "s0", m: "omarchy", harness: "shell", target: "0:0", repo: "btop", issue: null, pr: null, branch: "", line: null,
    last: ago(3), started: ago(3 * 86400), tasks: [], pane: [["dim","btop — cpu 18%  mem 9.1/31.2G  load 1.42 0.98 0.77"]] },
];

const permissions = [
  { id: "p1", s: "g4", text: "Allow: rm -rf go/internal/queue/testdata/fixtures", options: ["1 · Allow once", "2 · Allow for this session", "3 · Deny"],
    keys: ["1", "2", "3"], def: "3 · Deny", why: "Deletes files. Destructive prompts default to Deny and never auto-apply." },
];

// Real PRs (gh, 2026-09-28). `state` is what GitHub says.
const prs = [
  { repo: "eshu-hq/eshu", n: 7412, t: "perf(query): replace the entity-context anchor loop with one label-set statement on Neo4j", branch: "perf/7380-entity-anchor-plans", issues: [7380], ci: { pass: 54, fail: 1, pend: 1 }, state: "open" },
  { repo: "eshu-hq/eshu", n: 7411, t: "chore(agents): guard Codex spawns with named roles", branch: "codex-agent-spawn-guard", issues: [], ci: { pass: 83, fail: 0, pend: 1 }, state: "open" },
  { repo: "eshu-hq/eshu", n: 7410, t: "#7251 Aggregate graph-summary Function degrees in Neo4j", branch: "perf/7251-graph-summary", issues: [7251], ci: { pass: 75, fail: 1, pend: 0 }, state: "open" },
  { repo: "eshu-hq/eshu", n: 6927, t: "refactor(6696): de-stutter service leaf to collector/cloud/aws/service", branch: "feat/6696-service-singular", issues: [6696], ci: { pass: 65, fail: 2, pend: 0 }, state: "open" },
  { repo: "eshu-hq/eshu", n: 7414, t: "fix(storage): keep the prior failure when a supersede overwrites a work row", branch: "", issues: [7320], ci: { pass: 0, fail: 0, pend: 0 }, state: "merged", mergedAt: at("2026-09-28T21:49:23Z") },
  { repo: "eshu-hq/eshu", n: 7421, t: "perf(query): reduce language-inventory aggregation cost", branch: "", issues: [7247], ci: { pass: 0, fail: 0, pend: 0 }, state: "merged", mergedAt: at("2026-09-29T01:26:26Z") },
];

// Real open issues (gh, 2026-09-28). `claim` = claim comment author, if any (sample). `size` = triage estimate (sample).
const backlog = [
  { repo: "eshu-hq/eshu", n: 7408, t: "perf(projector): claim supersede sweep costs about 0.84 ms per superseded row", size: "M" },
  { repo: "eshu-hq/eshu", n: 7401, t: "changed-since: built cmd/reducer link-against-prune race and full-size rebase timing before PR-3c and default-on", size: "L", claim: "another agent" },
  { repo: "eshu-hq/eshu", n: 7398, t: "retention: the runner sleeps a full poll interval after a pass that found candidates and pruned none", size: "S" },
  { repo: "eshu-hq/eshu", n: 7397, t: "retention: the general path keeps up to 100 scope rows locked while it prunes a few; generalise PR-3d's savepoint give-back", size: "M" },
  { repo: "eshu-hq/eshu", n: 7396, t: "retention: BatchRowLimit and the retention events do not count fifteen cascade children of scope_generations", size: "S" },
  { repo: "CigarsOnly/virtual-humidor", n: 785, t: "Uniform 404 for GET /public/v1/shares/{slug} on non-viewable states (share enumeration)", size: "S" },
];

const audit = [
  { at: ago(6 * 60), who: "fleet", what: "goal DONE on eshu#6696; GitHub still shows #6927 open with 2 failing checks", where: "mbp fleet:0", s: "g5" },
  { at: ago(35 * 60), who: "fleet", what: "auto-dispatched eshu#7407 to muse (behind pace, 2% used)", where: "allen fleet:2", s: "g4" },
  { at: ago(60 * 60), who: "fleet", what: "read usage: claude 81% · codex 42% · muse 2%", where: "probes" },
  { at: ago(80 * 60), who: "fleet", what: "auto-dispatched virtual-humidor#780 to muse", where: "allen fleet:1", s: "g3" },
  { at: ago(5 * 3600), who: "you", what: "dispatched eshu#7380 to claude", where: "omarchy 0:1", s: "g1" },
];

prs.forEach(p => { p.kind = "pr"; });
backlog.forEach(i => { i.kind = "issue"; });
