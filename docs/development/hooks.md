# Development hooks

Harness hooks that catch what CI cannot: they fire on an action, before a diff
exists. They are the project-level hooks for *developing* fleet-board, ported
from eshu's and trimmed to what fleet-board needs. They are separate from the
product's own goal hooks ([01d](../specs/01d-goal-hooks.md)), which fleet-board
installs at user level for the machines it manages; when that installer sees
this repo's project `Stop` hook it reports `repo_ships_own=true` and stays
inert here, so the two never fight.

Rules live once, in [AGENTS.md](../../AGENTS.md). A hook names a rule or a
skill and enforces it; it never restates the rule.

## The hooks

Scripts live in `.claude/hooks/` and are shared by all three harnesses; the
harness configs only point at them. The tier is set by the exit code: only the
three marked **blocking** can stop anything.

| Hook | Does | Tier |
|---|---|---|
| `skill-nudge.sh` | Refuses an edit to a path governed by a project skill until that skill is loaded this session | **blocking** (exit 2) |
| `skill-loaded.sh` | Records each skill load as a marker file; never blocks | side effect |
| `on-compact.sh` | After compaction or resume: clears this session's skill markers, then re-injects the AGENTS.md pointer, the active goal and the names of the skills that had been loaded | advisory |
| `doc-staleness.sh` | After an edit: Go package docs (doc.go, README.md, AGENTS.md) older than or missing next to changed source; a changed `.proto` whose contract specs have not changed | advisory |
| `goal-continue.sh` | Refuses to end a turn while this session's goal is unfinished | **blocking** (`decision: block`) |
| `goal-refresh.sh` | `/goal` producer; restates the active goal on every prompt | side effect |
| `scripts/goal-role-router.py` | Routes `/goal` prompts naming skills to roles in `.agents/roles.json` with the tier's model binding; on Claude `Agent` calls drops an unrequested model override | advisory / rewrite |
| `scripts/guard-codex-spawn.py` | Codex only: denies a spawn that is not a named role in `.agents/roles.json` | **blocking** (deny) |

### Wiring

| Event | Claude Code (`.claude/settings.json`) | Codex (`.codex/hooks.json`) | Muse (`.muse/hooks.json`) |
|---|---|---|---|
| PreToolUse | `Agent` router (`claude-dispatch`); `Edit\|MultiEdit\|Write` skill-nudge | `^collaborationspawn_agent$` spawn guard | `write_file`, `edit_file` skill-nudge |
| PostToolUse | `Edit\|MultiEdit\|Write` doc-staleness; `Skill` skill-loaded | `^apply_patch$` doc-staleness | `write_file`, `edit_file` doc-staleness; `read_skill` skill-loaded |
| SessionStart | `compact\|resume` on-compact | none | on-compact (a `startup` source is ignored) |
| PreCompact | none | none | on-compact |
| UserPromptSubmit | goal-refresh, then router | router | goal-refresh, then router |
| Stop | goal-continue | none | goal-continue |

Every command resolves the checkout with `${CLAUDE_PROJECT_DIR}` /
`${CODEX_PROJECT_DIR}` or `git rev-parse --show-toplevel`, so it works from the
main checkout and from any worktree, and exits 0 when the script is missing.
The Codex spawn guard uses `git rev-parse` only and fails **closed** (exit 2),
so a sibling checkout's `CODEX_PROJECT_DIR` cannot select the wrong guard.

Harness differences, all handled in the shared scripts:

- Muse names the edited file `tool_input.path` (maybe relative to the payload
  `cwd`), the skill `tool_input.name`, and its tools `write_file`, `edit_file`,
  `read_skill`. Muse hooks run with a cleared environment, so environment
  switches do not reach them; use the `.claude/hooks-off` file (below).
- Codex does not fire `UserPromptSubmit` for `/goal` lines (01d probe), so the
  router there sees only `GOAL:` prompts and the spawn guard is the working
  Codex control. Codex has no Skill-tool hook wired and its native `/goal`
  loop handles continuation, so skill-nudge and the Stop guard are not wired
  there; `apply_patch` payloads vary by version, which is why doc-staleness
  ignores the payload and reads `git status`.
- **Trust.** Muse loads project hooks only in a trusted workspace (a session in
  an untrusted workspace prints a warning and runs none; `muse exec
  --trust-workspace` trusts one run). Codex runs a new or changed hook only
  after approval in `/hooks`; agents never pass
  `--dangerously-bypass-hook-trust`. Claude Code applies project hooks from a folder the user trusted.
- Not live-probed in this repo: Muse `PreCompact` and `SessionStart` context
  injection, and `additionalContext` from PostToolUse on Codex and Muse. A
  harness that ignores them loses only the reminder.

## Skill nudge and the path map

`.agents/skill-paths.json` is the single data file. Edit it to change what is
governed; nothing else needs to change.

```json
{"skills": ["fleet-sqlite-store"],
 "globs": ["internal/**/store/**", "*sqlite*", "migrations", "*.sql"],
 "exclude": ["docs/**", "*.md"],
 "note": "shown in the refusal"}
```

- Rules are a **union**: every rule whose globs match adds its skills, so a
  file under `internal/store/` needs `fleet-go` and `fleet-sqlite-store`.
  Order does not matter.
- A glob with a `/` is anchored to the repo-relative path (`**/` is zero or
  more directories). A glob without `/` matches any one path component, like
  `.gitignore` (`*sqlite*`, `go.mod`, `migrations`).
- `exempt` lists skills with no characteristic path (code review, diagnostics,
  performance, issue driver, live probe) with a reason.
- `scripts/verify-agent-canon.sh` fails if a rule names a missing skill, a
  skill is neither mapped nor exempt, or the JSON is invalid.

Current map (the exact globs are in `.agents/skill-paths.json`):
`api/**/*.proto`, `buf*.yaml` to `fleet-contract-rigor`; `*.go`, `go.mod`,
`go.sum`, `go.work` to `fleet-go`; `internal/**/store/**`, `*sqlite*`, `migrations`,
`*.sql` to `fleet-sqlite-store`; `internal/harness/**` and harness fixtures to
`fleet-harness-adapter`; `internal/**/tmux/**` to `fleet-tmux`; `web/**`,
`mockup/**` to `fleet-frontend`; `**/telemetry/**` and `**/observability/**` to
`fleet-telemetry-coverage`; auth, scope and credential paths to
`fleet-security`.

A marker is `<state dir>/fleet-skill-loaded-<session id prefix>-<skill>`
(state dir: `$FLEET_HOOK_STATE_DIR`, else `$TMPDIR`, else `/tmp`). The
requirement is per session and skill, so loading `fleet-go` once covers every
later Go edit. Compaction clears the markers (`on-compact.sh`), because a
resume keeps the session id but discards skill content.

## Goal protocol

`goal-refresh.sh` writes `.claude/active-goal.<session_id>` (gitignored; the
same path for all three harnesses) when a prompt starts with `/goal ` or
`GOAL: `. The first line after the metadata is the state:

| First line | Stop hook | Meaning |
|---|---|---|
| goal text | refuses the stop | working |
| `DONE` | allows | goal met |
| `BLOCKED: <reason> WATCH=<pid>` | allows while the pid is alive; a dead pid refuses | waiting on outside work |
| `BLOCKED: <reason> OWNER=<who> ACTION=<what>` | allows | a named person must act; the hub turns it into one Triage item ([design](../design.md)) |
| `BLOCKED: <reason>` | refuses, tells the agent to escalate to `arbiter-fleet` | stuck |

`stop_hook_active: true` always allows the stop: that is the loop breaker, in
Claude Code and in Muse (probed, 01d). Every parse failure also allows it. A
`CONSENT: <acts>` line records a grant the owner already gave; the hooks echo
it so the agent does not stop to ask again, and nothing verifies it, so each
honoured grant is also printed to stderr.

Commands: `/goal <text>`, `/goal done` (or `clear`), `/goal consent <acts>`,
`/goal consent <acts> -- <text>` (start a goal with the grant attached, the
form a dispatch uses), `/goal revoke-consent`. Consent and retire only edit a
file this session owns; an unheaded file is the owner's hand-written goal and
is never amended, and the machine-wide `~/.claude/active-goal` is never
written. `/goal <path>/goal.md` reads a prepared goal file inside the
workspace.

## Turning hooks off locally

| Switch | Scope |
|---|---|
| `FLEET_HOOKS_OFF=1` in the environment | every dev hook (not the Codex spawn guard) |
| an empty `.claude/hooks-off` file (gitignored) | every dev hook, including under Muse, whose hooks see no environment |
| `CLAUDE_GOAL_OFF=1` | goal-refresh and goal-continue only |
| `touch <state dir>/fleet-skill-override-<session id prefix>` | skill-nudge for one session (the refusal prints the exact command; use it only for a skill that truly cannot load, and say so) |
| `CLAUDE_GOAL_FILE`, `CLAUDE_GOAL_CONSENT` | name the goal file; grant consent before the first prompt |
| `"disableAllHooks": true` in `.claude/settings.local.json` | Claude Code, all hooks |
| remove the entry from `.codex/hooks.json` or leave it unapproved in `/hooks` | Codex |

Nothing relaxes on its own: a silent fallback would hide the case worth
knowing about.

## Tests

```bash
scripts/test-dev-hooks.sh              # hooks, wiring in all three configs, router, spawn guard
python3 scripts/test-goal-role-router.py
python3 scripts/test-codex-spawn-guard.py
scripts/verify-agent-canon.sh          # map names real skills; configs reference real scripts
```

`test-dev-hooks.sh` feeds synthetic payloads to the real scripts and runs each
config's own command string (Muse with a cleared environment). It runs on the
macOS system bash 3.2, like every hook script; none needs Homebrew bash.
`python3` is required by the hooks (3.9 is enough) and every hook fails open
without it.

## Not ported

- eshu's transcript-based no-progress budget for the Stop hook. `stop_hook_active`
  bounds the loop to one refusal per turn instead; if that proves too weak, a
  progress budget is the place to add back.
- eshu's `guard-live-gate` (blocks a second live gate on shared ports). fleet-board
  has no live gates yet. When live probes run routinely, a Bash PreToolUse guard
  for "one live harness probe at a time" (a second `tmux -L` probe server, or a
  probe while another holds the harness) belongs here, next to `fleet-live-probe`.
- eshu's golden-corpus, Postgres and Cypher arms in the skill map, and its
  `.eshu-doc-state/` snapshot file (doc-staleness reports to the agent instead).
