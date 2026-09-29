// Mockup data. Real: machines, RAM, installed tools, Codex usage, PRs and issues.
// Sample: sessions, tasks, questions, Claude usage, audit history.
const T0 = Date.now();
const ago = s => new Date(T0 - s * 1000);

const machines = [
  { id: "mbp", name: "Linuxdynasty-MacBook-Pro-14", short: "mbp", ip: "192.168.1.118", user: "asanabria", os: "macOS 27 · arm64", ram: 64, hub: true,
    tools: { tmux: true, claude: true, codex: true, muse: true }, slots: 2, repoRoot: "~/repos", tmux: "fleet", beat: ago(2), drained: false },
  { id: "omarchy", name: "omarchy", short: "omarchy", ip: "192.168.1.150", user: "linuxdynasty", os: "Linux 7.2 · x86_64", ram: 62,
    tools: { tmux: true, claude: true, codex: true, muse: true }, slots: 2, repoRoot: "~/repos", tmux: "0", beat: ago(4), drained: false },
  { id: "allen", name: "allens-Apple-MacBook-Pro", short: "allen", ip: "192.168.1.232", user: "allen", os: "macOS 25.6 · arm64", ram: 128,
    tools: { tmux: true, claude: true, codex: true, muse: true }, slots: 4, repoRoot: "~/repos", tmux: "fleet", beat: ago(3), drained: false },
  { id: "m124", name: "ip-192-168-1-124", short: ".124", ip: "192.168.1.124", user: "linuxdynasty", os: "macOS 27 · arm64", ram: 64,
    tools: { tmux: true, claude: true, codex: true, muse: true }, slots: 2, repoRoot: "~/repos", tmux: "fleet", beat: ago(6), drained: false },
];

// Usage is per account, not per machine: every machine on the same account shares one pool.
const agents = [
  { id: "claude", label: "Claude Code", real: false, source: "sample · planned source: statusline / usage events",
    windows: [{ label: "5-hour", used: 42, resets: T0 + 2.6 * 3600e3 }, { label: "weekly", used: 63, resets: T0 + 3.2 * 86400e3 }] },
  { id: "codex", label: "Codex", real: true, source: "real · rate_limits in ~/.codex/sessions rollout, 2026-09-27",
    windows: [{ label: "weekly", used: 81, resets: 1791046709000 }] },
  { id: "muse", label: "Muse", real: false, source: "no usage signal found yet · schema mentions quota", windows: [] },
];

const settings = {
  repos: [{ repo: "eshu-hq/eshu", on: true }, { repo: "CigarsOnly/virtual-humidor", on: false }],
  headroomMin: 25, // percent left before an agent is held back from new work
};

const sessions = [
  { id: "s1", m: "omarchy", agent: "claude", target: "0:1", repo: "eshu", branch: "perf/7380-entity-anchor-plans",
    task: "Fix the failing check on #7412", state: "working", last: ago(12), started: ago(47 * 60),
    tasks: [["Read failing check log", 1], ["Reproduce locally", 1], ["Find the regression in entity_context.go", 1], ["Fix label-set query", 1], ["Add regression test", 1], ["Run go test ./...", 1], ["Run the Neo4j integration suite", 0], ["Push and re-run CI", 0], ["Update PR description", 0]],
    pane: [["dim","● Bash(gh pr checks 7412 --repo eshu-hq/eshu)"],["bad","  ⎿  1 failing · 1 pending · 54 passing"],["dim","● Read(go/internal/query/entity_context.go)"],["",""],["hl","✻ Running the Neo4j integration suite… (38s · esc to interrupt)"]] },
  { id: "s2", m: "omarchy", agent: "codex", target: "0:2", repo: "dotfiles", branch: "main",
    task: "Add fleet status to tmux status-right", state: "waiting", last: ago(95), started: ago(20 * 60), q: "q2",
    tasks: [["Read ~/.tmux.conf", 1], ["Draft status-right snippet", 1], ["Decide where the snippet lives", 0], ["Reload tmux and verify", 0]],
    pane: [["","• Read ~/.tmux.conf (142 lines)"],["","• Draft: status-right runs `fleet status --short` every 10s"],["",""],["warn","▌ Question: overwrite ~/.tmux.conf or write ~/.tmux.fleet.conf and source it?"],["dim","  Continuing with default in 7m unless answered."]] },
  { id: "s3", m: "allen", agent: "muse", target: "fleet:1", repo: "virtual-humidor", branch: "fix/780-db-test-leak-detector",
    task: "Get #794 green: row-leak detector tests", state: "stuck", last: ago(14 * 60 + 20), started: ago(80 * 60),
    tasks: [["Read CI failure", 1], ["Start local Postgres", 1], ["Run leak detector tests", 1], ["Fix connection setup", 0], ["Re-run tests", 0], ["Push", 0], ["Re-run CI", 0]],
    pane: [["","> run tests: go test ./humidor-service/... -run Leak"],["dim","=== RUN   TestRowLeakDetector"],["","    waiting for TEST_DATABASE_URL connection"],["",""],["dim","(no output for 14m)"]] },
  { id: "s4", m: "omarchy", agent: "shell", target: "0:0", repo: "btop", branch: "",
    task: "System monitor", state: "idle", last: ago(3), started: ago(3 * 86400), tasks: [],
    pane: [["dim","btop — cpu 18%  mem 9.1/31.2G  load 1.42 0.98 0.77"]] },
  { id: "s5", m: "mbp", agent: "claude", target: "fleet:0", repo: "fleet-board", branch: "mock/ui",
    task: "Build hub API: /events, /state, SSE stream", state: "waiting", last: ago(40), started: ago(35 * 60), q: "q1",
    tasks: [["Sketch event schema", 1], ["Write hub/store.go", 1], ["Pick state storage", 1], ["Write hub/sse.go", 1], ["Wire /events endpoint", 0], ["Load test with 4 collectors", 0]],
    pane: [["dim","● Write(hub/store.go)"],["","  ⎿  Wrote 118 lines"],["",""],["warn","● fleet ask \"Store hub state in SQLite or keep it in memory?\" --default sqlite"],["dim","  ⎿  question q1 posted · continuing with default"],["hl","✻ Writing hub/sse.go… (12s)"]] },
  { id: "s6", m: "mbp", agent: "claude", target: "cigarsonly:1", repo: "CigarsOnly", branch: "chore/compose-reset",
    task: "Reset local compose stack", state: "waiting", last: ago(22), started: ago(6 * 60), q: "q3",
    tasks: [["Stop running containers", 1], ["Check disk usage", 1], ["Remove volumes", 0], ["Rebuild images", 0], ["Seed database", 0]],
    pane: [["","● Bash(docker compose -p cigarsonly-local down -v)"],["",""],["bad"," Do you want to proceed?"],[""," ❯ 1. Yes"],["","   2. Yes, and don't ask again for docker compose commands"],["","   3. No, and tell Claude what to do differently (esc)"]] },
  { id: "s7", m: "m124", agent: "codex", target: "fleet:0", repo: "infra-notes", branch: "main",
    task: "Summarize Tailscale ACL options", state: "done", last: ago(9 * 60), started: ago(40 * 60),
    tasks: [["Read Tailscale ACL docs", 1], ["Compare 3 layouts", 1], ["Write docs/tailscale-acl.md", 1]],
    pane: [["ok","• Wrote docs/tailscale-acl.md (3 options compared)"],["dim","Turn complete."]] },
];

const questions = [
  { id: "q1", s: "s5", kind: "text", text: "Store hub state in SQLite or keep it in memory?", options: ["sqlite", "memory"],
    def: "sqlite", why: "Survives a hub restart; one file, no server.", deadline: T0 + 8 * 60e3, sendable: true },
  { id: "q2", s: "s2", kind: "text", text: "Overwrite ~/.tmux.conf or write ~/.tmux.fleet.conf and source it?", options: ["separate file", "overwrite"],
    def: "separate file", why: "Keeps your existing config untouched.", deadline: T0 + 7 * 60e3, sendable: false,
    blocked: "Send is off for Codex until we can tell when it is waiting. Accept the default or answer in the pane." },
  { id: "q3", s: "s6", kind: "perm", text: "Allow: docker compose -p cigarsonly-local down -v", options: ["1 · Yes", "2 · Yes, don't ask again", "3 · No"],
    keys: ["1", "2", "3"], def: "3 · No", why: "Deletes volumes. Destructive prompts always default to No and never auto-apply.", deadline: null, sendable: true },
];

// Real, from `gh pr list --author @me` on 2026-09-28. ci = check counts.
const prs = [
  { repo: "eshu-hq/eshu", n: 7412, t: "perf(query): replace the entity-context anchor loop with one label-set statement on Neo4j", branch: "perf/7380-entity-anchor-plans", issues: [7380], ci: { pass: 54, fail: 1, pend: 1 }, at: "2026-09-28T19:46:26Z" },
  { repo: "eshu-hq/eshu", n: 7411, t: "chore(agents): guard Codex spawns with named roles", branch: "codex-agent-spawn-guard", issues: [], ci: { pass: 83, fail: 0, pend: 1 }, at: "2026-09-28T19:26:45Z" },
  { repo: "eshu-hq/eshu", n: 7410, t: "#7251 Aggregate graph-summary Function degrees in Neo4j", branch: "perf/7251-graph-summary", issues: [], ci: { pass: 75, fail: 1, pend: 0 }, at: "2026-09-28T18:59:44Z" },
  { repo: "eshu-hq/eshu", n: 6927, t: "refactor(6696): de-stutter service leaf to collector/cloud/aws/service", branch: "feat/6696-service-singular", issues: [], ci: { pass: 65, fail: 2, pend: 0 }, at: "2026-09-25T04:35:13Z" },
  { repo: "CigarsOnly/virtual-humidor", n: 798, t: "fix(deploy): provide ANALYTICS_ID_SECRET for the local and VPS stacks", branch: "fix/local-analytics-id-secret", issues: [797], ci: { pass: 0, fail: 1, pend: 0 }, at: "2026-09-24T00:48:21Z" },
  { repo: "CigarsOnly/virtual-humidor", n: 796, t: "feat(frontend): virtualize the humidor collection grid", branch: "feat/546-humidor-grid-virtualization", issues: [795], ci: { pass: 0, fail: 6, pend: 0 }, at: "2026-09-24T01:03:26Z" },
  { repo: "CigarsOnly/virtual-humidor", n: 794, t: "test(humidor-service): add a DB row-leak detector for TEST_DATABASE_URL-gated tests", branch: "fix/780-db-test-leak-detector", issues: [780], ci: { pass: 0, fail: 6, pend: 0 }, at: "2026-09-24T00:30:28Z" },
  { repo: "CigarsOnly/virtual-humidor", n: 793, t: "fix(frontend): resolve WCAG findings F-006, F-007, F-008, F-011", branch: "a11y-open-findings-546", issues: [792], ci: { pass: 0, fail: 6, pend: 0 }, at: "2026-09-23T23:32:23Z" },
];

// Real open issues with no linked PR, from `gh issue list` on 2026-09-28.
const backlog = [
  { repo: "eshu-hq/eshu", n: 7408, t: "perf(projector): claim supersede sweep costs about 0.84 ms per superseded row" },
  { repo: "eshu-hq/eshu", n: 7407, t: "queue: failure_details has no size bound at the Fail-time writer" },
  { repo: "eshu-hq/eshu", n: 7398, t: "retention: the runner sleeps a full poll interval after a pass that found candidates and pruned none" },
  { repo: "CigarsOnly/virtual-humidor", n: 785, t: "Uniform 404 for GET /public/v1/shares/{slug} on non-viewable states (share enumeration)" },
  { repo: "CigarsOnly/virtual-humidor", n: 781, t: "Add regression guards for #661 explanation-coverage and price-blind match% guarantees" },
];

// Every state-changing action lands here: who, what, where, when.
const audit = [
  { at: ago(9 * 60), who: "codex", what: "finished Summarize Tailscale ACL options", where: ".124 fleet:0", s: "s7" },
  { at: ago(35 * 60), who: "you", what: "started claude on fleet-board · Build hub API", where: "mbp fleet:0", s: "s5" },
  { at: ago(47 * 60), who: "you", what: "assigned claude to eshu#7412 · Fix failing checks", where: "omarchy 0:1", s: "s1" },
  { at: ago(80 * 60), who: "you", what: "assigned muse to virtual-humidor#794 · Fix failing checks", where: "allen fleet:1", s: "s3" },
];

prs.forEach(p => { p.kind = "pr"; });
backlog.forEach(i => { i.kind = "issue"; });
