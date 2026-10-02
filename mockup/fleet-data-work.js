// Mockup data, part 2: tmux windows (goal panes and the developer's own), prompts, harness conditions,
// change requests, backlog, audit and session history. Loaded after fleet-data.js.
//
// One entry per tmux window the agent sees (E1). `sess` is the tmux session name exactly as `tmux ls`
// shows it; `win` is the window name exactly as the status bar shows it. `yours` = the developer's own
// window (no FLEET_* tags); otherwise started by fleet and named by the template `{harness} {work}`.
// `display` is WorkItemRef.display; `provider` is ProviderKind. `pane` here is the screen (E5), `paneId` the tmux pane id.
const sessions = [
  // omarchy: fleet session + the developer's own "main"
  { id: "g1", m: "omarchy", harness: "claude", sess: "fleet", idx: 1, win: "claude eshu#7380", paneId: "%11", repo: "eshu-hq/eshu", provider: "github", issue: 7380, pr: 7412, display: "eshu#7380", branch: "perf/7380-entity-anchor-plans",
    line: "BLOCKED: waiting on CI for #7412 WATCH=48213", watchAlive: true, last: ago(40), started: ago(5 * 3600),
    tasks: [["Claim #7380", 1], ["Reproduce the slow anchor loop", 1], ["Replace with one label-set statement", 1], ["Benchmark before/after", 1], ["Open PR #7412", 1], ["CI green", 0], ["Review rounds", 0], ["Merge", 0]],
    pane: [["dim", "● Bash(gh pr checks 7412 --repo eshu-hq/eshu --watch)"], ["", "  ⎿  54 passing · 1 pending · 1 failing (retrying)"], ["", ""], ["warn", "● Goal: BLOCKED: waiting on CI for #7412 WATCH=48213"], ["dim", "  watcher 48213 alive · wakes on check completion"]] },
  { id: "g2", m: "omarchy", harness: "codex", sess: "fleet", idx: 2, win: "codex eshu#7251", paneId: "%12", repo: "eshu-hq/eshu", provider: "github", issue: 7251, pr: 7410, display: "eshu#7251", branch: "perf/7251-graph-summary",
    line: "Drive eshu-hq/eshu#7251 to merged", last: ago(15), started: ago(3 * 3600), cond: "c2",
    tasks: [["Claim #7251", 1], ["Aggregate Function degrees in Neo4j", 1], ["Open PR #7410", 1], ["Fix failing check", 0], ["Review rounds", 0], ["Merge", 0]],
    pane: [["", "• gh pr checks 7410: 1 failing (go-test-integration)"], ["", "• Reading failure log"], ["warn", "⚠ Rate limited (HTTP 429) · retrying in 30s · attempt 2/5"], ["hl", "• Working (1m 12s · esc to interrupt)"]] },
  // The developer's own "main" session on omarchy: base-index 1, eight windows as the real status line shows them.
  // `layout` + `panes` describe split windows (TmuxWindow.panes[]); pane 0 is the window's main pane (`pane`, `paneId`).
  { id: "s0", m: "omarchy", harness: "shell", yours: true, sess: "main", idx: 1, win: "btop", paneId: "%0", repo: "", line: null, last: ago(3), started: ago(3 * 86400), tasks: [],
    pane: [["hl", "btop 1.4.0 ─ omarchy ─ up 3d 02:11"], ["", "cpu  ▇▇▃▁▁▁▁▁▁▁▁▁▁▁▁▁ 18%   load 1.42 0.98 0.77"], ["", "mem  ▇▇▇▇▇▃▁▁▁▁▁▁▁▁▁▁ 9.1/62G"], ["", "net  ↓ 1.2 MiB/s  ↑ 212 KiB/s"], ["dim", "pid    user         cpu%  mem%  command"], ["", "41822  linuxdynasty  9.1   2.4   claude"], ["", "40118  linuxdynasty  6.3   1.9   codex"], ["", "  912  linuxdynasty  0.4   0.1   fleet-agent"]] },
  { id: "s7", m: "omarchy", harness: "opencode", yours: true, sess: "main", idx: 2, win: "opencode", paneId: "%7", repo: "eshu-hq/eshu", line: null, last: ago(55 * 60), started: ago(6 * 3600), tasks: [], idle: true, available: false,
    model: "Build · DeepSeek V3.2 · DeepSeek", acct: "deepseek-api",
    pane: [["dim", "OpenCode 1.18.30 · ~/repos/eshu"], ["", ""], ["", "> "], ["dim", "Build · DeepSeek V3.2 · DeepSeek"]] },
  { id: "s1", m: "omarchy", harness: "codex", yours: true, sess: "main", idx: 3, win: "codex prompt 1", paneId: "%1", repo: "eshu-hq/eshu", line: null, last: ago(26 * 60), started: ago(7 * 3600), tasks: [], idle: true, available: false,
    pane: [["dim", "› Codex 0.159.2 · gpt-5.3-codex · ~/repos/eshu"], ["", ""], ["", "> "], ["dim", "  idle 26m · no goal · not marked available"]],
    layout: "cols", panes: [{ cmd: "go", paneId: "%8", pane: [["", "linuxdynasty@omarchy ~/repos/eshu % go test ./go/internal/query/... -run Anchor"], ["ok", "ok      eshu/go/internal/query   4.218s"], ["", "linuxdynasty@omarchy ~/repos/eshu % go test -bench Anchor -run xxx ./go/internal/query/"], ["", "BenchmarkAnchorPlan-16    1203    948211 ns/op"], ["dim", "PASS · running…"]] }] },
  { id: "s8", m: "omarchy", harness: "codex", yours: true, sess: "main", idx: 4, win: "codex prompt 2", paneId: "%9", repo: "eshu-hq/eshu", line: null, last: ago(8), started: ago(2 * 3600), tasks: [],
    pane: [["dim", "› Codex 0.159.2 · gpt-5.3-codex · ~/repos/eshu"], ["", "• Reading go/internal/projector/supersede.go"], ["", "• Running go test ./go/internal/projector/..."], ["hl", "• Working (3m 02s · esc to interrupt)"]] },
  { id: "s9", m: "omarchy", harness: "codex", yours: true, sess: "main", idx: 5, win: "codex prompt 3", paneId: "%10", repo: "eshu-hq/eshu", line: null, last: ago(2 * 3600), started: ago(5 * 3600), tasks: [], idle: true, available: false,
    pane: [["dim", "› Codex 0.159.2 · gpt-5.3-codex · ~/repos/eshu"], ["", ""], ["", "> "], ["dim", "  idle 2h · no goal · not marked available"]] },
  { id: "s10", m: "omarchy", harness: "claude", yours: true, sess: "main", idx: 6, win: "claude w1", paneId: "%13", repo: "eshu-hq/eshu", line: null, last: ago(12), started: ago(8 * 3600), tasks: [],
    pane: [["dim", "Claude Code 2.1.285 · ~/repos/eshu · Claude Max"], ["", "● Read go/internal/retention/runner.go"], ["", "● Grep pattern: \"pollInterval\""], ["hl", "● Thinking (22s · esc to interrupt)"]],
    layout: "rows", panes: [{ cmd: "git", paneId: "%14", pane: [["", "linuxdynasty@omarchy ~/repos/eshu % git log --oneline -4"], ["", "7f1c2e9 retention: count cascade children in BatchRowLimit"], ["", "a04d9b1 projector: bound failure details"], ["", "c93e77a fix(storage): keep the prior failure on supersede"], ["", "11be0f4 perf(query): language-inventory aggregation"], ["", "linuxdynasty@omarchy ~/repos/eshu % "]] }] },
  { id: "s2", m: "omarchy", harness: "claude", yours: true, sess: "main", idx: 7, win: "claude w2", paneId: "%2", repo: "eshu-hq/eshu", line: null, last: ago(41 * 60), started: ago(9 * 3600), tasks: [], idle: true, available: true,
    pane: [["dim", "Claude Code 2.1.285 · ~/repos/eshu · Claude Max"], ["", ""], ["", "> "], ["ok", "  idle 41m · marked available for work on the board"]] },
  { id: "s11", m: "omarchy", harness: "shell", yours: true, sess: "main", idx: 8, win: "zsh", paneId: "%15", repo: "", line: null, last: ago(30), started: ago(3 * 86400), tasks: [],
    pane: [["", "linuxdynasty@omarchy ~ % tmux ls"], ["", "fleet: 2 windows (created Thu Oct  2 09:12:40 2026)"], ["", "main: 8 windows (created Mon Sep 29 07:40:03 2026) (attached)"], ["", "linuxdynasty@omarchy ~ % "]],
    layout: "main-vertical", panes: [
      { cmd: "tail", paneId: "%16", pane: [["", "linuxdynasty@omarchy ~ % tail -f ~/.local/state/fleet-agent/agent.log"], ["dim", "09:12:41 INFO  hello ok machine=omarchy hub=192.168.1.118"], ["dim", "09:12:41 INFO  control-mode attached server=default windows=10"], ["dim", "10:48:02 INFO  prompt detected pane=%12 id=p-… (none pending)"], ["dim", "11:03:19 INFO  heartbeat active=2/2 total=2/4"]] },
      { cmd: "htop", paneId: "%17", pane: [["hl", "htop ─ Tasks: 212, 1 running"], ["", "  PID USER        PRI  NI  CPU%  MEM%  Command"], ["", "41822 linuxdynasty 20   0   9.1   2.4  claude"], ["", "40118 linuxdynasty 20   0   6.3   1.9  codex"], ["", "  912 linuxdynasty 20   0   0.4   0.1  fleet-agent"]] }] },
  // allen: fleet session + the developer's own "work"
  { id: "g3", m: "allen", harness: "muse", sess: "fleet", idx: 1, win: "muse virtual-humidor#780", paneId: "%21", repo: "CigarsOnly/virtual-humidor", provider: "github", issue: 780, pr: 794, display: "virtual-humidor#780", branch: "fix/780-db-test-leak-detector",
    line: "BLOCKED: TEST_DATABASE_URL not reachable from this machine", watchAlive: false, last: ago(14 * 60 + 20), started: ago(80 * 60),
    tasks: [["Claim #780", 1], ["Write row-leak detector", 1], ["Open PR #794", 1], ["Run gated DB tests", 0], ["CI green", 0], ["Merge", 0]],
    pane: [["", "> go test ./humidor-service/... -run Leak"], ["bad", "    dial tcp 127.0.0.1:55432: connect: connection refused"], ["warn", "Goal: BLOCKED: TEST_DATABASE_URL not reachable from this machine"], ["dim", "(no watcher armed · no output for 14m)"]] },
  { id: "g4", m: "allen", harness: "muse", sess: "fleet", idx: 2, win: "muse eshu#7407", paneId: "%22", repo: "eshu-hq/eshu", provider: "github", issue: 7407, pr: null, display: "eshu#7407", branch: "fix/7407-failure-details-bound",
    line: "Drive eshu-hq/eshu#7407 to merged", last: ago(25), started: ago(35 * 60), perm: "p1",
    tasks: [["Claim #7407", 1], ["Find Fail-time writer", 1], ["Add size bound + test", 0], ["Open PR", 0], ["CI green", 0], ["Merge", 0]],
    pane: [["", "> Bash: rm -rf go/internal/queue/testdata/fixtures"], ["", ""], ["bad", " Allow this command?"], ["", " ❯ 1. Allow once"], ["", "   2. Allow for this session"], ["", "   3. Deny"]] },
  { id: "g9", m: "allen", harness: "claude", sess: "fleet", idx: 3, win: "claude OPS-142", paneId: "%23", repo: "eshu-hq/eshu", provider: "jira", issue: "OPS-142", pr: null, display: "OPS-142", branch: "ops/OPS-142-runner-idle-alert", sample: true,
    line: "Drive OPS-142 to merged", last: ago(50), started: ago(22 * 60),
    tasks: [["Move OPS-142 to In Progress", 1], ["Find the retention runner's idle pass", 1], ["Add alert + test", 0], ["Open PR", 0], ["CI green", 0], ["Merge", 0]],
    pane: [["dim", "● Jira OPS-142 → github.com/eshu-hq/eshu (ticket in one system, code in another)"], ["", "● Read go/internal/retention/runner.go"], ["hl", "● Working (48s · esc to interrupt)"]] },
  { id: "s3", m: "allen", harness: "shell", yours: true, sess: "work", idx: 0, win: "zsh", paneId: "%3", repo: "", line: null, last: ago(2 * 3600), started: ago(2 * 86400), tasks: [], pane: [["dim", "allen@allens-Apple-MacBook-Pro ~ %"]] },
  { id: "s4", m: "allen", harness: "opencode", yours: true, sess: "work", idx: 1, win: "opencode", paneId: "%4", repo: "eshu-hq/eshu", line: null, last: ago(9 * 60), started: ago(4 * 3600), tasks: [], idle: true, available: false,
    model: "Build · Kimi K3 Moonshot AI · high", acct: "moonshot-api",
    pane: [["dim", "OpenCode 1.18.34 · ~/repos/eshu"], ["", ""], ["", "> "], ["dim", "Build · Kimi K3 Moonshot AI · high"]] },
  // mbp (hub): fleet session + "main"
  { id: "g5", m: "mbp", harness: "claude", sess: "fleet", idx: 0, win: "claude eshu#6696", paneId: "%31", repo: "eshu-hq/eshu", provider: "github", issue: 6696, pr: 6927, display: "eshu#6696", branch: "feat/6696-service-singular",
    line: "DONE", last: ago(6 * 60), started: ago(9 * 3600),
    tasks: [["Claim #6696", 1], ["Rename service leaf", 1], ["Open PR #6927", 1], ["CI green", 1], ["Merge", 1]],
    pane: [["ok", "● Goal marked DONE"], ["dim", "  #6927 merged, #6696 closed"]] },
  { id: "g7", m: "mbp", harness: "muse", sess: "fleet", idx: 1, win: "muse eshu#7398", paneId: "%32", repo: "eshu-hq/eshu", provider: "github", issue: 7398, pr: null, display: "eshu#7398", branch: "fix/7398-retention-idle-sleep",
    line: "Drive eshu-hq/eshu#7398 to merged", last: ago(60), started: ago(62 * 60), perm: "p2",
    tasks: [["Claim #7398", 1], ["Reproduce the idle sleep", 1], ["Fix the poll loop", 1], ["Open PR", 0], ["CI green", 0], ["Merge", 0]],
    pane: [["", "> Bash: git fetch https://x-access-token:$(gh auth token)@github.com/eshu-hq/eshu"], ["", ""], ["bad", " Allow this command?"], ["", " ❯ 1. Allow once"], ["", "   2. Allow for this session"], ["", "   3. Deny"]] },
  { id: "s5", m: "mbp", harness: "shell", yours: true, sess: "main", idx: 0, win: "zsh", paneId: "%5", repo: "", line: null, last: ago(120), started: ago(86400), tasks: [], pane: [["dim", "asanabria@mbp ~/repos/fleet-board %"]] },
  // .124: fleet session + "dev"
  { id: "g6", m: "m124", harness: "codex", sess: "fleet", idx: 0, win: "codex eshu#7411", paneId: "%41", repo: "eshu-hq/eshu", provider: "github", issue: null, pr: 7411, display: "eshu#7411", branch: "codex-agent-spawn-guard",
    line: "BLOCKED: waiting for review on #7411 WATCH=9120", watchAlive: true, last: ago(18 * 60), started: ago(6 * 3600),
    tasks: [["Guard Codex spawns with named roles", 1], ["Open PR #7411", 1], ["CI green", 1], ["Review", 0], ["Merge", 0]],
    pane: [["", "• PR #7411: 83 passing · 1 pending"], ["warn", "Goal: BLOCKED: waiting for review on #7411 WATCH=9120"]] },
  { id: "g8", m: "m124", harness: "muse", sess: "fleet", idx: 1, win: "muse eshu#7397", paneId: "%42", repo: "eshu-hq/eshu", provider: "github", issue: 7397, pr: null, display: "eshu#7397", branch: "fix/7397-savepoint-giveback", sample: true,
    line: "Drive eshu-hq/eshu#7397 to merged", last: ago(7), started: ago(48 * 60), cond: "c1",
    tasks: [["Claim #7397", 1], ["Generalise the savepoint give-back", 0], ["Open PR", 0], ["CI green", 0], ["Merge", 0]],
    pane: [["", "> Edit go/internal/retention/prune.go"], ["bad", "⚠ Meta Model API server error (HTTP 500) · retrying in 7s · attempt 6/10"], ["dim", "  goal stays set · harness retries on its own"]] },
  { id: "s6", m: "m124", harness: "shell", yours: true, sess: "dev", idx: 0, win: "zsh", paneId: "%6", repo: "", line: null, last: ago(5 * 3600), started: ago(3 * 86400), tasks: [], pane: [["dim", "linuxdynasty@ip-192-168-1-124 ~ %"]] },
];
sessions.forEach(s => { s.target = `${s.sess}:${s.idx}`; });

// E6 PromptDetected. `destructive` prompts default to Deny and are never auto-answered. p2 is the real prompt from the
// 2026-10-01 measured goal: a command line that embeds a credential.
const permissions = [
  { id: "p1", promptId: "p-4c1e", s: "g4", text: "Allow: rm -rf go/internal/queue/testdata/fixtures", options: ["1 · Allow once", "2 · Allow for this session", "3 · Deny"],
    keys: ["1", "2", "3"], def: "3 · Deny", destructive: true, reason: "deletes files", source: "screen pattern", confidence: 96 },
  { id: "p2", promptId: "p-7f3a", s: "g7", text: "Allow: git fetch https://x-access-token:$(gh auth token)@github.com/eshu-hq/eshu", options: ["1 · Allow once", "2 · Allow for this session", "3 · Deny"],
    keys: ["1", "2", "3"], def: "3 · Deny", destructive: true, reason: "command line embeds a credential ($(gh auth token) inside the URL)", source: "screen pattern", confidence: 93 },
];

// E12 HarnessCondition: neither a prompt nor normal work. Distinct from "stuck": the harness is retrying on its own.
const conditions = [
  { id: "c1", s: "g8", kind: "provider error", retrying: true, attempt: 6, max: 10, http: 500, nextRetry: ago(-7), since: ago(4 * 60), text: "Meta Model API server error (HTTP 500) · retrying in 7s · attempt 6/10", sample: true },
  { id: "c2", s: "g2", kind: "rate limited", retrying: true, attempt: 2, max: 5, http: 429, nextRetry: ago(-30), since: ago(50), text: "Rate limited (HTTP 429) · retrying in 30s · attempt 2/5", sample: true },
  { id: "c3", s: "s4", kind: "sign-in required", retrying: false, http: 401, since: ago(9 * 60), text: "Moonshot AI: API key rejected (HTTP 401) · run /connect to sign in again", sample: true },
];

// Change requests. Real GitHub PRs (gh, 2026-09-28); one sample GitLab MR so the board shows mixed providers.
const prs = [
  { repo: "eshu-hq/eshu", n: 7412, t: "perf(query): replace the entity-context anchor loop with one label-set statement on Neo4j", branch: "perf/7380-entity-anchor-plans", issues: [7380], ci: { pass: 54, fail: 1, pend: 1 }, state: "open" },
  { repo: "eshu-hq/eshu", n: 7411, t: "chore(agents): guard Codex spawns with named roles", branch: "codex-agent-spawn-guard", issues: [], ci: { pass: 83, fail: 0, pend: 1 }, state: "open" },
  { repo: "eshu-hq/eshu", n: 7410, t: "#7251 Aggregate graph-summary Function degrees in Neo4j", branch: "perf/7251-graph-summary", issues: [7251], ci: { pass: 75, fail: 1, pend: 0 }, state: "open" },
  { repo: "eshu-hq/eshu", n: 6927, t: "refactor(6696): de-stutter service leaf to collector/cloud/aws/service", branch: "feat/6696-service-singular", issues: [6696], ci: { pass: 65, fail: 2, pend: 0 }, state: "open" },
  { repo: "eshu-hq/eshu", n: 7414, t: "fix(storage): keep the prior failure when a supersede overwrites a work row", branch: "", issues: [7320], ci: { pass: 0, fail: 0, pend: 0 }, state: "merged", mergedAt: at("2026-09-28T21:49:23Z") },
  { repo: "eshu-hq/eshu", n: 7421, t: "perf(query): reduce language-inventory aggregation cost", branch: "", issues: [7247], ci: { pass: 0, fail: 0, pend: 0 }, state: "merged", mergedAt: at("2026-09-29T01:26:26Z") },
  { repo: "acme/infra", host: "gitlab.com", provider: "gitlab", n: 12, display: "infra!12", t: "ci: pin runner image digests and drop the nightly rebuild", branch: "ci/pin-runner-digests", issues: [], ci: { pass: 12, fail: 0, pend: 1 }, state: "open", sample: true },
];

// Work items. Real open GitHub issues (gh, 2026-09-28); `claim` and `size` are sample. One sample Jira ticket whose
// code lives on GitHub (ADR 0012: a ticket in one system drives code on another).
const backlog = [
  { repo: "eshu-hq/eshu", n: 7408, t: "perf(projector): claim supersede sweep costs about 0.84 ms per superseded row", size: "M" },
  { repo: "eshu-hq/eshu", n: 7401, t: "changed-since: built cmd/reducer link-against-prune race and full-size rebase timing before PR-3c and default-on", size: "L", claim: "another agent" },
  { repo: "eshu-hq/eshu", n: 7398, t: "retention: the runner sleeps a full poll interval after a pass that found candidates and pruned none", size: "S" },
  { repo: "eshu-hq/eshu", n: 7397, t: "retention: the general path keeps up to 100 scope rows locked while it prunes a few; generalise PR-3d's savepoint give-back", size: "M" },
  { repo: "eshu-hq/eshu", n: 7396, t: "retention: BatchRowLimit and the retention events do not count fifteen cascade children of scope_generations", size: "S" },
  { repo: "CigarsOnly/virtual-humidor", n: 785, t: "Uniform 404 for GET /public/v1/shares/{slug} on non-viewable states (share enumeration)", size: "S" },
  { repo: "eshu-hq/eshu", provider: "jira", project: "OPS", n: "OPS-142", display: "OPS-142", kindLabel: "ticket", targetRepo: "github.com/eshu-hq/eshu", t: "Alert when the retention runner idles a full poll interval with candidates pending", size: "S", sample: true },
];

const audit = [
  { at: ago(6 * 60), who: "fleet", what: "goal DONE on eshu#6696; GitHub still shows #6927 open with 2 failing checks", where: "mbp fleet:0", s: "g5" },
  { at: ago(10 * 60), who: "fleet", what: "probe /usage: claude 81% all models · 53% Fable · session 10%", where: "fleet-probe" },
  { at: ago(22 * 60), who: "you", what: "dispatched OPS-142 (Jira → github.com/eshu-hq/eshu) to claude", where: "allen fleet:3", s: "g9" },
  { at: ago(35 * 60), who: "fleet", what: "auto-dispatched eshu#7407 to muse (behind pace, 2% used)", where: "allen fleet:2", s: "g4" },
  { at: ago(41 * 60), who: "you", what: "marked claude w2 available for work", where: "omarchy main:7", s: "s2" },
  { at: ago(80 * 60), who: "fleet", what: "auto-dispatched virtual-humidor#780 to muse", where: "allen fleet:1", s: "g3" },
  { at: ago(5 * 3600), who: "you", what: "dispatched eshu#7380 to claude", where: "omarchy fleet:1", s: "g1" },
];

// ADR 0010 session history (v2): sample timeline for the replay placeholder. Sizes use the measured numbers from
// 01c (26.9 MB/h raw, 0.23 MB/h zstd-3 per busy pane).
const history = {
  g1: [["session started", "EnsureWindow fleet:1 · claude"], ["goal typed", "StartGoal step type_goal · goal file seen 4s later"], ["PR opened", "#7412 · 54 checks queued"], ["BLOCKED", "waiting on CI · WATCH=48213 armed"]],
  g7: [["session started", "EnsureWindow fleet:1 · muse"], ["goal typed", "/goal consent … (eshu hook convention)"], ["prompt detected", "p-7f3a · destructive: credential in command line"]],
};
const RAW_MB_PER_H = 26.9, STORED_MB_PER_H = 0.23;

prs.forEach(p => { p.kind = "pr"; p.provider ??= "github"; p.host ??= "github.com"; p.display ??= `${p.repo.split("/")[1]}#${p.n}`; });
backlog.forEach(i => { i.kind = "issue"; i.provider ??= "github"; i.host ??= "github.com"; i.display ??= `${i.repo.split("/")[1]}#${i.n}`; });
