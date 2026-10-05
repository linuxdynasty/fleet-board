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

Scripts live in `.claude/hooks/` and are shared by all four harnesses; the
harness configs only point at them (Cursor through the adapter, below). The tier is set by the exit code: only the
three marked **blocking** can stop anything.

| Hook | Does | Tier |
|---|---|---|
| `skill-nudge.sh` | Refuses an edit to a path governed by a project skill until that skill is loaded this session | **blocking** (exit 2) |
| `skill-loaded.sh` | Records each skill load as a marker file; never blocks | side effect |
| `on-compact.sh` | After compaction or resume: clears this session's skill markers, then re-injects the AGENTS.md pointer, the active goal and the names of the skills that had been loaded | advisory |
| `doc-staleness.sh` | After an edit: Go package docs (doc.go, README.md, AGENTS.md) older than or missing next to changed source; a change in a folder mapped in `.agents/doc-paths.json` (a `.proto` included) none of whose specs has changed. `make docs-stale-ci` and CI run the same script with `--ci`, which blocks ([Doc staleness and the folder map](#doc-staleness-and-the-folder-map)) | advisory |
| `goal-continue.sh` | Refuses to end a turn while this session's goal is unfinished | **blocking** (`decision: block`) |
| `goal-refresh.sh` | `/goal` producer; restates the active goal on every prompt | side effect |
| `scripts/goal-role-router.py` | Routes `/goal` prompts naming skills to roles in `.agents/roles.json` with the tier's model binding; on Claude `Agent` calls drops an unrequested model override | advisory / rewrite |
| `scripts/cursor-hook.py` | Cursor only: the adapter `.cursor/hooks.json` runs; maps a Cursor event to the Claude-shaped payload a script expects and the script's answer back to Cursor JSON | adapter |
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

### Cursor

Cursor (IDE and `cursor-agent`) runs `.cursor/hooks.json` natively, so the dev
hooks do not depend on Cursor's "Include third-party Plugins, Skills, and other
configs" setting, which is what loads `.claude/settings.json` into Cursor.
Every command is `python3 "${CURSOR_PROJECT_DIR:-.}/scripts/cursor-hook.py"
<event> <hook script>`. The adapter reads the Cursor payload, builds the
Claude-shaped payload the script already understands (`conversation_id` is the
session id), runs it, and prints exactly one JSON object. Fields are from
cursor.com/docs/hooks (fetched 2026-10-04).

| Claude hook | Cursor event (matcher) | Result for Cursor |
|---|---|---|
| `skill-nudge.sh` (PreToolUse Edit, MultiEdit, Write) | `preToolUse` (`Write`), `failClosed` | exit 2 becomes `{"permission": "deny", "user_message", "agent_message"}`, else `{"permission": "allow"}` |
| `doc-staleness.sh` (PostToolUse) | `postToolUse` (`Write`) | the reminder becomes `additional_context` |
| `goal-continue.sh` (Stop) | `stop` | a refusal becomes `followup_message`; `loop_count > 0` is `stop_hook_active`, so it refuses once per user turn, as in Claude (cursor-agent resets `loop_count` on each prompt, so Cursor's `loop_limit` of 5 is never reached); a turn whose `status` is not `completed` (aborted, error) is never refused, as Claude's Stop hook does not fire then |
| `on-compact.sh` (SessionStart `compact\|resume`) | `preCompact` | clears this session's skill markers; the re-injection text goes to `user_message` (the owner sees it, the agent does not). The `resume` half is **not portable**: Cursor has no resume event, so on a resumed conversation the skill markers outlive the discarded skill content and skill-nudge can wave through an edit |
| `goal-refresh.sh` (UserPromptSubmit) | `beforeSubmitPrompt` | writes the goal file for `/goal` lines, always plain `{"continue": true}` (Cursor shows `user_message` only when `continue` is false, so the restated goal is dropped) |
| `skill-loaded.sh` (PostToolUse Skill) | `postToolUse` (`Read`) | **approximation**: Cursor has no Skill tool, so reading `<project>/.agents/skills`, `.claude/skills` or `.cursor/skills` `/<name>/SKILL.md` records the marker (symlinks that leave the project, and any other read, do nothing) |
| `goal-role-router.py claude-dispatch` (PreToolUse Agent) | none | **not wired**: a rewrite is possible, since `preToolUse` with matcher `Task` supports a documented `updated_input` ("Modified tool input", cursor.com/docs/hooks), but the generated `.cursor/agents/*.md` already say `model: inherit`, so there is nothing to route. (`subagentStart` could not do it: its output only allows or denies.) |
| `goal-role-router.py claude` (UserPromptSubmit) | none | **not wired**: it only adds context, and Cursor's docs list no context field for `beforeSubmitPrompt` (only `continue` and `user_message`). The cursor-agent 2026.10.01 bundle (`index.js`, `case"beforeSubmitPrompt"`) accepts `additional_context` without documenting it; we do not rely on undocumented behaviour, and that bundle behaviour is NOT_CHECKED live. |
| goal-refresh and on-compact context injection | none | **not wired**: `additional_context` is documented on `sessionStart` and `postToolUse` only, so the restated goal is dropped (see `beforeSubmitPrompt` above; the same NOT_CHECKED bundle behaviour applies) and the compaction text reaches the owner only |

Notes on the choices:

- **`postToolUse`, not `afterFileEdit`, for doc-staleness.** `afterFileEdit` has
  no output field, so its reminder could never reach the agent, and the hook
  prints each reminder once: running it on both events would spend the one
  reminder on the event whose output is dropped. The adapter still maps
  `afterFileEdit` (and `sessionStart`) so either can be wired by editing
  `.cursor/hooks.json`; neither is wired.
- **`sessionStart` is not wired.** `on-compact.sh` is silent for a startup
  source, and the Cursor `sessionStart` payload names no resume or compact
  source, so the hook would never speak.
- **Skill loads.** Without the `Read` mapping, `skill-nudge.sh` would refuse
  every governed edit in Cursor with no way to satisfy it. An agent that loads a
  skill some other way still has the per-session override the refusal prints.
- **Tool input field names.** Cursor's docs do not name the fields inside a
  `Write` or `Read` `tool_input`. In cursor-agent 2026.10.01 (read from its
  bundle) `Write` is `{file_path, content}` and `Read` is `{file_path}`. The
  adapter tries `file_path` first, then `path`, `target_file`, `filePath`,
  `file` and the payload's own `file_path`, as insurance against other
  versions. With none present it allows the edit and says so on stderr rather
  than block on a guessed path.
- **The refusal text.** The skill-nudge refusal says to load the skill "with the
  skill tool"; Cursor has none, so the adapter appends the files to read
  (`.agents/skills/<name>/SKILL.md`) to the `agent_message` only.
- **Failure modes.** The one guard (`preToolUse`, `failClosed: true` and the
  adapter's own deny) refuses when the script is missing, crashes, takes longer
  than the timeout or the payload is unreadable. Every other hook fails open with
  a note on stderr. The timeout (`TIMEOUT_SECONDS`, overridable with
  `FLEET_CURSOR_HOOK_TIMEOUT`) lives in `scripts/cursor-hook.py` and stays below
  the 10 seconds in `.cursor/hooks.json`; messages are cut at 20000 characters.
- **Third-party loading.** While that Cursor setting is on, Cursor runs both the
  Claude-compatible hooks and these native ones, so a hook can fire twice
  with the same answer. Turn the setting off once `.cursor/hooks.json` is in
  place.
- **Trust.** Cursor runs project hooks from the workspace root with
  `CURSOR_PROJECT_DIR` set. The off switches below apply unchanged, because the
  scripts read them.
- Not live-probed in a real Cursor session: whether `cursor-agent` passes `/goal`
  lines to `beforeSubmitPrompt`, and whether `postToolUse` `additional_context`
  is shown to the agent. The `tool_input` shapes come from reading the
  cursor-agent 2026.10.01 bundle, not from a probe.

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
`*.sql`, and the packages that write the databases (`internal/hub/{ingest,outbox,syncer,pacing,insights,scheduler,goals,verifier,approvals,routines,logstore}`,
`internal/agent/{ring,goals,logship}`) to `fleet-sqlite-store`; `internal/harness/**` and harness
fixtures (and `internal/agent/{probe,hooks}/**`) to `fleet-harness-adapter`;
`internal/**/tmux/**`, `internal/agent/probe/**` and `internal/agent/{control,start,attach}/**` to `fleet-tmux`; `web/**`,
`mockup/**` to `fleet-frontend`; `**/telemetry/**` and `**/observability/**` to
`fleet-telemetry-coverage`; auth, scope and credential paths and the
trust-boundary packages (`internal/hub/{ca,gateway,api,integration,config,approvals,attach}`,
`internal/agent/{conn,policy,exec,control,start,hooks,attach}`, `cmd/fleetd`, `cmd/fleet-agent`) to
`fleet-security`.

A marker is `<state dir>/fleet-skill-loaded-<session id prefix>-<skill>`
(state dir: `$FLEET_HOOK_STATE_DIR`, else `$TMPDIR`, else `/tmp`). The
requirement is per session and skill, so loading `fleet-go` once covers every
later Go edit. Compaction clears the markers (`on-compact.sh`), because a
resume keeps the session id but discards skill content.

## Doc staleness and the folder map

`.agents/doc-paths.json` maps folders to the specs that describe them; the
globs work like `skill-paths.json` above. Each rule names its globs and its
specs; a change under the globs (and none of the rule's `exclude` globs) is
covered when ANY ONE of its specs changes too. A rule may name a folder that
does not exist yet, but every spec must exist: `check-docs-stale.py
--check-map` checks that, and `verify-agent-canon.sh` runs it. A code folder
maps to the specs that define its capabilities or name the folder, never to
ADRs; each rule's note cites the section. `unmapped` lists only the folders no
spec defines, with the reason.

`scripts/check-docs-stale.py` has two modes on that one map:

- **Hook mode** (no flags): the reminder above. It reads `git status`, never
  fails, and compares file times only here.
- **`--ci --range A..B`** (`make docs-stale-ci` with `"$BASE_REF"..HEAD`; CI
  with the PR's base and head): exit 1 when a mapped folder changed in the
  range and none of its specs did, or when a changed Go package lacks
  doc.go, README.md or AGENTS.md; exit 2 when git fails, the range does not
  resolve or the map is invalid. A commit message line
  `No-Doc-Change: <reason>` in the range covers every mapped folder (not the
  package docs). A blank reason or a placeholder (TODO, TBD, n/a, none, a
  dash, the word `reason`, or `<reason>` copied as is) does not count, and a
  PR body never does.

`scripts/check-doc-names.py` (`make doc-names`) is the other doc-drift gate:
every backticked proto name in the checked docs must exist in `api/fleet`,
read from the pinned buf's compiled image through `scripts/proto_image.py`.
Its docstring lists the checked docs and the token rules; tokens that are not
proto names on purpose are in `.agents/doc-names-allow.json`, one entry each
with a reason and, when only one doc may use the token, a `file`; an entry no
doc uses any more (or that its file no longer uses) fails.

`scripts/gen-doc-tables.py` (`make doc-tables` runs `--check`) is the third:
the release tables in 02k, the scope tables in 02d, the delivery table in 02i
and the capability map in 02 are generated from the proto comment tags
(02e, "Comment tags") between the script's begin and end markers. A
tag that breaks the rules, a hand edit inside a block, or a tag change without
`--write` fails it. `scripts/report-capability-tests.py`
(`make capability-tests`) lists the v1 capabilities in 01 that no product
test names: a Go `*_test.go` outside `scripts/` and every `testdata/` folder,
or a web test under `web/`. The gate self-tests in `scripts/` name ids only as
fixture data, so they never count. Until plan row 17 it reports and exits 0,
and fails only when it cannot read its input.

## Goal protocol

`goal-refresh.sh` writes `.claude/active-goal.<session_id>` (gitignored; the
same path for all four harnesses) when a prompt starts with `/goal ` or
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
| remove the entry from `.cursor/hooks.json` | Cursor |

Nothing relaxes on its own: a silent fallback would hide the case worth
knowing about.

## Tests

```bash
scripts/test-dev-hooks.sh              # hooks, wiring in the Claude, Codex and Muse configs, router, spawn guard
python3 scripts/test-goal-role-router.py
python3 scripts/test-codex-spawn-guard.py
python3 scripts/test-cursor-hook.py    # Cursor adapter and .cursor/hooks.json, Cursor-shaped payloads (make selftest)
python3 scripts/test-cursor-hook-edges.py  # timeouts, bad output, deep JSON, stop status, skill reads (make selftest)
scripts/verify-agent-canon.sh          # maps name real skills and specs; configs reference real scripts
python3 scripts/test-check-docs-stale.py   # hook mode, --ci, the trailer, --check-map (make selftest)
python3 scripts/test-check-doc-names.py    # the doc-name gate against a fixture contract (make selftest)
python3 scripts/test-gen-doc-tables.py     # tag rules and generated blocks against a fixture tree (make selftest)
python3 scripts/test-report-capability-tests.py  # the capability report against a fixture tree (make selftest)
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
