// Mockup data, part 1: machines, accounts, integrations, settings, harness catalogs.
// Real: machines, RAM, tools, weekly usage (status-command probes 2026-09-28 ~20:35 EDT),
// PRs, issues, merges (fleet-data-work.js). Sample: everything marked `sample`.
const T0 = Date.now();
const ago = s => new Date(T0 - s * 1000);
const at = iso => new Date(iso).getTime();
const inDays = d => new Date(T0 + d * 86400e3);

// B1 MachineFacts + H4 ForgeCliStatus + L1 PolicyInfo + L2 EffectiveSettings.sources (sample where noted).
// `sess` lists the developer's own tmux sessions (E1); fleet-started windows live in `sessions` (fleet-data-work.js).
const machines = [
  { id: "mbp", name: "Linuxdynasty-MacBook-Pro-14", short: "mbp", user: "asanabria", os: "macOS 27 · arm64", ram: 64, hub: true,
    harnesses: ["claude", "codex", "muse", "opencode"], versions: { claude: "2.1.286", codex: "0.159.2", muse: "1.4.2", opencode: "1.18.34" },
    maxActive: 2, maxTotal: 4, repoRoot: "~/repos", tmuxVersion: "tmux 3.5a", defaultServer: "control", labels: ["laptop", "hub"],
    forge: [{ tool: "gh", host: "github.com", user: "linuxdynasty", access: [["eshu-hq/eshu", "ADMIN"], ["CigarsOnly/virtual-humidor", "WRITE"]] }],
    certExpires: inDays(61), policyHash: "a91f3c", policyChanged: false, agentVersion: "0.3.1", beat: ago(2), drained: false,
    sources: { max_active: "machine", max_total: "group laptop", default_server: "fleet default", cert_lifetime: "fleet default", local_approval: "local ceiling", idle_threshold: "fleet default" } },
  { id: "omarchy", name: "omarchy", short: "omarchy", user: "linuxdynasty", os: "Linux 7.2 · x86_64", ram: 62,
    harnesses: ["claude", "codex", "muse", "opencode"], versions: { claude: "2.1.285", codex: "0.159.2", muse: "1.4.1", opencode: "1.18.30" },
    maxActive: 2, maxTotal: 4, repoRoot: "~/repos", tmuxVersion: "tmux 3.4", defaultServer: "control", labels: ["always-on", "linux"],
    forge: [{ tool: "gh", host: "github.com", user: "linuxdynasty", access: [["eshu-hq/eshu", "ADMIN"]] }, { tool: "glab", host: "gitlab.com", user: "linuxdynasty", access: [["acme/infra", "Maintainer"]] }],
    certExpires: inDays(12), policyHash: "7be210", policyChanged: true, agentVersion: "0.3.1", beat: ago(4), drained: false,
    sources: { max_active: "group always-on", max_total: "group always-on", default_server: "local ceiling", cert_lifetime: "machine", local_approval: "fleet default", idle_threshold: "machine" } },
  { id: "allen", name: "allens-Apple-MacBook-Pro", short: "allen", user: "allen", os: "macOS 25.6 · arm64", ram: 128,
    harnesses: ["claude", "codex", "muse", "opencode"], versions: { claude: "2.1.286", codex: "0.159.2", muse: "1.4.2", opencode: "1.18.34" },
    maxActive: 4, maxTotal: 8, repoRoot: "~/repos", tmuxVersion: "tmux 3.5a", defaultServer: "control", labels: ["always-on", "big"],
    forge: [{ tool: "gh", host: "github.com", user: "linuxdynasty", access: [["eshu-hq/eshu", "ADMIN"], ["CigarsOnly/virtual-humidor", "WRITE"]] }],
    certExpires: inDays(88), policyHash: "a91f3c", policyChanged: false, agentVersion: "0.3.1", beat: ago(3), drained: false,
    sources: { max_active: "machine", max_total: "machine", default_server: "fleet default", cert_lifetime: "fleet default", local_approval: "fleet default", idle_threshold: "fleet default" } },
  { id: "m124", name: "ip-192-168-1-124", short: ".124", user: "linuxdynasty", os: "macOS 27 · arm64", ram: 64,
    harnesses: ["claude", "codex", "muse", "opencode"], versions: { claude: "2.1.286", codex: "0.157.1", muse: "1.4.2", opencode: "1.18.32" },
    maxActive: 2, maxTotal: 4, repoRoot: "~/repos", tmuxVersion: "tmux 3.2a", defaultServer: "observe", labels: ["laptop"],
    forge: [{ tool: "gh", host: "github.com", user: "linuxdynasty", access: [["eshu-hq/eshu", "ADMIN"]] }],
    certExpires: inDays(4), policyHash: "c0ffee", policyChanged: false, agentVersion: "0.3.0", beat: ago(6), drained: false,
    sources: { max_active: "group laptop", max_total: "group laptop", default_server: "local ceiling", cert_lifetime: "fleet default", local_approval: "local ceiling", idle_threshold: "fleet default" } },
];

// G1 GoalHooksState per machine and harness (01d: user-level hooks, inert outside fleet goals). Sample.
// Fields use the proto names so the "details" toggle can show them as they are; `why` is mock-only explanation.
const hk = o => ({ installed: true, verified: true, trust: "TRUSTED", repo_ships_own: false, hook_version: "0.3.1", hook_hash: "5d2e9a", ...o });
const CLAUDE_HOOKS = "~/.claude/settings.json", CODEX_HOOKS = "~/.codex/hooks.json", MUSE_HOOKS = "muse plugin fleet-goal-hooks";
// GoalHooksState.trust_approve_command (harness.proto): what the owner types to approve a pending hook definition.
const TRUST_APPROVE = { codex: "/hooks", muse: "muse plugins approve fleet-goal-hooks" };
const goalHooks = {
  mbp: { claude: hk({ trust: "NOT_REQUIRED", config_path: CLAUDE_HOOKS }), codex: hk({ config_path: CODEX_HOOKS, hook_hash: "b41c07", trust: "PENDING" }), muse: hk({ config_path: MUSE_HOOKS, hook_hash: "b41c07", trust: "PENDING" }) },
  omarchy: { claude: hk({ trust: "NOT_REQUIRED", config_path: CLAUDE_HOOKS, repo_ships_own: true }), codex: hk({ config_path: CODEX_HOOKS, hook_hash: "b41c07" }), muse: hk({ config_path: MUSE_HOOKS, hook_hash: "b41c07" }) },
  allen: { claude: hk({ trust: "NOT_REQUIRED", config_path: CLAUDE_HOOKS }), codex: hk({ config_path: CODEX_HOOKS, hook_hash: "b41c07", trust: "PENDING" }), muse: hk({ config_path: MUSE_HOOKS, hook_hash: "b41c07", trust: "PENDING" }) },
  m124: { claude: hk({ trust: "NOT_REQUIRED", config_path: CLAUDE_HOOKS }), codex: hk({ installed: false, verified: false, hook_version: "", hook_hash: "", config_path: CODEX_HOOKS, why: "codex 0.157.1 is older than the supported versions; update Codex" }), muse: hk({ config_path: MUSE_HOOKS, hook_hash: "b41c07" }) },
};

// Subscription accounts (BUDGET_KIND_SUBSCRIPTION_PERCENT). `shown` mirrors the harness's own screen (D2 "as shown");
// `buckets` is the standardized half the scheduler uses. Emails are sample and hidden unless settings.showEmails.
const accounts = [
  { id: "claude-max", harness: "claude", label: "Claude Code", plan: "Claude Max account", email: "owner@example.com", reserve: 25, source: "/usage", sourceKind: "probe",
    consent: ["push", "pr-open"], mode: "fleet",
    shown: [["Current session", "10% used", "Resets 11:40pm"], ["Current week (all models)", "81% used", "Resets Sep 30 at 10pm"], ["Current week (Fable)", "53% used", "Resets Sep 30 at 10pm"]],
    buckets: [{ key: "weekly_all", label: "all models", used: 81 }, { key: "weekly_fable", label: "Fable", used: 53 }],
    short: { label: "session", used: 10, resets: at("2026-09-28T23:40:00-04:00") },
    resets: at("2026-09-30T22:00:00-04:00"), windowDays: 7, read: ago(10 * 60), passive: "You've used 81% of your weekly limit · resets 10pm (America/New_York)" },
  { id: "chatgpt-pro", harness: "codex", label: "Codex", plan: "Account: Pro", email: "(not shown by Codex; mapped on the hub)", reserve: 10, source: "/status", sourceKind: "probe",
    consent: ["push", "pr-open", "merge"], mode: "fleet",
    shown: [["Weekly limit", "58% left", "Resets Oct 4 at 9:29pm"], ["Account", "Pro", ""], ["Model", "gpt-5.3-codex · high", ""]],
    buckets: [{ key: "weekly", label: "weekly", used: 42, remaining: true }], short: null,
    resets: at("2026-10-04T21:29:00-04:00"), windowDays: 7, read: ago(12 * 60) },
  { id: "muse-power", harness: "muse", label: "Muse", plan: "Muse Code Power Usage", email: "owner@example.com", reserve: 10, source: "/usage", sourceKind: "passive",
    consent: ["push", "pr-open", "merge"], mode: "fleet",
    shown: [["Current", "0% used", "Resets 9:06pm"], ["Weekly", "2% used", "Resets Oct 4 at 8pm"], ["Session", "41.2k in · 12.8k cached · 6.1k out", "17 turns · 2 subagents"]],
    buckets: [{ key: "weekly", label: "weekly", used: 2 }], short: { label: "current", used: 0, resets: at("2026-09-28T21:06:00-04:00") },
    resets: at("2026-10-04T20:00:00-04:00"), windowDays: 7, read: ago(3 * 60), passive: "status line: weekly 2%" },
];

// API-key accounts reached through OpenCode (BUDGET_KIND_DOLLAR_CAP). Manual dispatch only, never auto. Sample.
const apiAccounts = [
  { id: "deepseek-api", harness: "opencode", provider: "DeepSeek", label: "DeepSeek (API key)", capUsd: 20, spentUsd: 6.4, source: "opencode stats", read: ago(25 * 60), mode: "fleet", budgetKind: "dollar_cap", sample: true },
  { id: "moonshot-api", harness: "opencode", provider: "Moonshot AI", label: "Moonshot (API key)", capUsd: 15, spentUsd: 11.2, source: "opencode stats", read: ago(25 * 60), mode: "fleet", budgetKind: "dollar_cap", sample: true },
];

// ADR 0012 integrations. GitHub is connected (ADR 0005); the rest are planned adapters with their declared capabilities.
const integrations = [
  { id: "github-com", provider: "GitHub", status: "connected", how: "GitHub App · read-only", roles: ["work tracker", "code host"], claims: ["comment", "assignee", "label"], sync: "poll (conditional, ETags)", checks: true, reviews: true, merge: true, watched: ["eshu-hq/eshu", "CigarsOnly/virtual-humidor"] },
  { id: "gitlab", provider: "GitLab", status: "planned", roles: ["work tracker", "code host"], claims: ["comment", "assignee", "label"], sync: "poll (conditional) · webhook", checks: true, reviews: true, merge: true, note: "nested groups, self-hosted; MRs shown as !12" },
  { id: "jira", provider: "Jira", status: "planned", roles: ["work tracker"], claims: ["status (In Progress)", "assignee"], sync: "poll", checks: false, reviews: false, merge: false, note: "tickets only; code lives on a separate host" },
  { id: "bitbucket", provider: "Bitbucket", status: "planned", roles: ["code host"], claims: ["assignee"], sync: "poll", checks: true, reviews: true, merge: true },
  { id: "linear", provider: "Linear", status: "planned", roles: ["work tracker"], claims: ["status"], sync: "webhook", checks: false, reviews: false, merge: false },
];

const settings = {
  repos: [
    { repo: "eshu-hq/eshu", host: "github.com", provider: "github", on: true, hooks: "eshu" },
    { repo: "CigarsOnly/virtual-humidor", host: "github.com", provider: "github", on: false, hooks: "auto" },
    { repo: "acme/infra", host: "gitlab.com", provider: "gitlab", on: true, hooks: "generic", sample: true },
  ],
  consent: ["push", "pr-open", "merge"],
  autoDispatch: false,   // the approved default (defaults.md); the owner turns it on
  showEmails: false,
  windowTemplate: "{harness} {work}",
  idleMinutes: 2,
  // Prefix the browser terminal emulates in the mock; the real build forwards raw keys, so the developer's own prefix applies.
  tmuxPrefix: "C-b",
};

// C4 harness command catalogs (01b, observed 2026-09-30/10-01), classified by the adapter.
// DENY-class commands are never listed here: the hub never sends them.
const CATALOG = {
  claude: { hash: "3f9c1a", observe: ["/status", "/usage", "/context", "/diff", "/tasks", "/recap"], session: ["/compact", "/model", "/effort", "/fast", "/plan", "/rename", "/resume", "/fork"],
    goal: ["/goal"], review: ["/security-review"], destructive: ["/clear", "/rewind"] },
  codex: { hash: "b7d204", observe: ["/status", "/usage", "/diff", "/ps", "/recap"], session: ["/compact", "/model", "/fast", "/plan", "/rename", "/resume", "/stop", "/fork", "/new"],
    goal: ["/goal"], review: ["/review"], destructive: ["/clear", "/delete", "/archive", "/exit"] },
  muse: { hash: "e15a77", observe: ["/status", "/usage", "/tasks", "/recap", "/hud"], session: ["/compact", "/model", "/effort", "/rename", "/resume", "/stop", "/fork", "/new"],
    goal: ["/goal"], review: ["/code-review"], destructive: ["/clear", "/rewind", "/delete", "/quit"] },
  opencode: { hash: "90ab4e", observe: ["/status", "/diff", "/sessions"], session: ["/models", "/new", "/editor"], goal: [], review: ["/review"], destructive: ["/exit"] },
};
const CLASSES = ["observe", "session", "goal", "review", "destructive"];

// 01b goal subcommands per harness (probed 2026-10-01). Claude has no pause/resume/edit.
// The hub keeps no per-harness table: it reads MachineHarness.goal_control (harness.proto GoalControl: subcommands,
// pause_mode, resume_mode, resume_text). This is a sample of what each machine reports. Claude: SET and CLEAR only;
// pause is Interrupt, resume types "continue".
const GOAL_CONTROL = {
  claude: { subs: ["set", "clear"], pause: "interrupt", resume: "type_text", resumeText: "continue" },
  codex: { subs: ["set", "clear", "edit", "pause", "resume"], pause: "subcommand", resume: "subcommand" },
  muse: { subs: ["set", "edit", "clear", "pause", "resume"], pause: "subcommand", resume: "subcommand" },
  opencode: { subs: [], pause: "none", resume: "none" },
};
const GOAL_SUBS = Object.fromEntries(Object.entries(GOAL_CONTROL).map(([h, c]) => [h, c.subs.filter(x => x !== "set")]));
const GOAL_SYNTAX = { claude: "/goal [<condition> | clear]", codex: "/goal [<objective>|clear|edit|pause|resume]", muse: "/goal [<objective>|edit <objective>|clear|pause|resume]", opencode: "no /goal (needs the generic goal protocol)" };
const RESUME = { claude: "claude --continue", codex: "codex resume --last", muse: "muse resume --last", opencode: "opencode" };
