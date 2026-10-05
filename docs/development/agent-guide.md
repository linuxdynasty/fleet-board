# Agent engineering guide

What [AGENTS.md](../../AGENTS.md) does not say: recipes, traps, the role and
model table, the task-to-skill map, the handoff contract and how to run a role.
The rules themselves (git hygiene, PR and CI, evidence, escalation, security,
documentation, the local gates) are only in AGENTS.md.

## Operating standard

- Understand the flow before editing. Each workflow is told once, in
  [03-workflows.md](../specs/03-workflows.md) and its parts 03b to 03l.
- Fix root causes. A symptom fix that hides a race, swallows an error or
  serializes work is a failure.
- Account for invalid input, empty and stale state, partial failure, duplicates,
  retries, ordering, idempotency and a restart in the middle of a command.
- Product code is not written ahead of its row in
  [the v1 plan](../plan/v1-implementation-plan.md). Until a row lands, `docs/` and
  `api/` are the product.

## Worktrees and git recipes

- One worktree per task:
  `git worktree add ~/personal-repos/fleet-board-worktrees/<name> -b <branch> origin/main`.
  Verify `pwd` before any edit.
- Formatters, generators and `go mod tidy` run only in a worktree, including for
  diagnostics.
- With several worktrees, compare with `git diff`, `git show <ref>:<path>` or a
  throwaway worktree, never `git stash` (one shared stash stack).
- After a push, confirm it landed:
  `git ls-remote origin <branch>` equals `git rev-parse HEAD`.
- Never `git push --force`. Use `--force-with-lease` only for an authorized
  rebase of your own pushed branch.
- Isolate formatter drift into its own commit so the real diff stays reviewable.

## Prove the theory first

Before building on a theory about performance or behavior, run the cheapest
experiment that could falsify it, on representative data:

- store and durability: a program like `spikes/sqlite/` against the real driver,
  pragmas and filesystem;
- compression and volume: `spikes/zstdchunk/` on real terminal output;
- harness behavior: a throwaway-server probe (`fleet-live-probe`);
- code paths: a Go benchmark with `benchstat`.

[04-technology-review.md](../specs/04-technology-review.md) did exactly this and
records what is and is not proven. A diagnosis is a theory too: state a cause
only with the observation behind it, and never treat one passing run as proof of
an intermittent bug. A filtered test run can silently match nothing; confirm
tests ran (`go test -list '.*' ./pkg/...`, or `-v` and read the names). Mark
claims by evidence: observed (date and version), documented (source) or
proposed; probe results follow the shape in
[01d-goal-hooks.md](../specs/01d-goal-hooks.md).

## macOS and zsh notes

- The shell is zsh, which does not word-split an unquoted `$var`. Use an array
  (`files=(a b); cmd "${files[@]}"`), `${=var}`, or a `for` over a literal list.
- zsh has no `PIPESTATUS`; use `rc=$?` right after the command, or
  `set -o pipefail`.
- `/bin/bash` is 3.2. Scripts needing `declare -A` or other bash 4 features run
  with `/opt/homebrew/bin/bash` first on `PATH`. Repo scripts here avoid them.
- `sed` is BSD: in-place is `sed -i ''`, and a pattern with paths needs a
  non-`/` delimiter. Prefer the editor tool or a heredoc for multi-line edits.
- Format and lint with `make fmt` and `make lint`: they use the pinned
  `gofumpt` and `golangci-lint` in `.bin/` (`make tools`), the same builds CI
  uses. The copies in `~/go/bin` may be older and disagree with the gate;
  `benchstat`, `gosec` and `govulncheck` live there.
- Several agents on one host: separate `GOCACHE`, short `GOTMPDIR` outside the
  worktree, and no overlapping benchmark runs.

## Make targets and the pre-push guard

The `Makefile` is the one entry point; CI runs the same targets, one per step
(`.github/workflows/ci.yml`). Run `make tools gen` first in a new worktree
(`gen/` is never committed).

| Target | What it does |
|---|---|
| `tools` | builds the pinned `golangci-lint` and `gofumpt` into `.bin/`; rebuilt when a pin or the Go version changes |
| `gen` | removes `gen/`, then `buf generate` into it (Go and TypeScript), through `go run` at the pinned buf |
| `build`, `smoke` | `CGO_ENABLED=0 go build ./...`; builds each `cmd/<name>` and checks that `--version` prints its name |
| `test` | `go vet ./...`, then `go test` and `go test -race` on the changed packages |
| `lint` | the pinned `golangci-lint` on the same packages |
| `proto-check`, `ts-check` | `buf build` and `buf lint`; the generated TypeScript compiles (`tools/tscheck`) |
| `fmt`, `fmt-check` | the pinned `gofumpt` formats, or only checks, every Go file (the same file set) |
| `mod-check`, `actionlint` | `go mod tidy -diff`; the workflow files |
| `selftest` | the seeded RED/GREEN cases of the gate scripts, including the pre-push hook |
| `diff-check` | `git diff --check` against the merge base, and a refusal of any file named like a git ref (`HEAD`, `main`, ...) |
| `doc-names` | every backticked proto name in the checked docs is in `api/fleet` (hooks.md, "Doc staleness and the folder map") |
| `docs-stale-ci`, `docs-stale` | a change in `BASE_REF..HEAD` to a mapped folder changes one of its specs or a commit carries `No-Doc-Change: <reason>`, and changed Go packages have their three docs; `docs-stale` only prints the hook reminders |
| `gates` | all of the above (not `docs-stale`) plus the file cap, agent canon and roles check; prints the tool versions it used first |
| `hooks` | turns on the pre-push guard (`core.hooksPath` is `.githooks`; once per clone, and all its worktrees share it) |

"Changed packages" come from `scripts/changed-go-packages.sh`: the files that
differ from the merge base with `BASE_REF` (default `origin/main`; CI uses the
PR's base), plus untracked files, plus every package that imports one of them.
A change to `go.mod`, `go.sum`, the Makefile, `tool-versions.env`,
`.golangci.yml` or a contract (`api/`, buf config) runs every package. The race
detector needs cgo on Linux but not on macOS, so `make test` sets
`CGO_ENABLED=1` for the `-race` run on Linux only; shipped binaries stay
cgo-free.

`.githooks/pre-push` is the guard for `main` (ADR 0024, decision 2: the repo is
private on a plan without branch protection). After `make hooks`, a push that
updates or deletes `main` (or `master`) on any remote is refused, and a push of
a branch runs `make gates` first and is refused if it fails. Deleting another
branch and pushing a tag run no gate.

The guard is opt-in per clone: until you run `make hooks`, git does not run it.
The gate checks the working tree, so the hook makes sure the tree is exactly the
commit being pushed. It refuses a push of any commit that is not the checked-out
`HEAD` (check out the branch you push, then push again), and a push while any
file is modified, staged or untracked and not ignored (`gen/`, `.bin/` and
`node_modules` are ignored). It lists up to ten paths to commit or remove. That
is the only reason the gate can vouch for the committed `HEAD`. The hook checks
again after the gate passes: if the tree or `HEAD` changed while it ran, the push
is refused ("run it again").

The guard stops mistakes: an accidental push to `main`, a push that was never
gated, a gate that an ambient variable (a parent make's `MAKEFLAGS`, a stray
`BASE_REF` from a shell profile) quietly turned into a no-op. It does not stop a
person or a process that controls the shell, and it is not a lock. To keep the
gate honest, before `make` the hook unsets `BASE_REF`, every `FLEET_*` variable,
`MAKEFLAGS`, `MFLAGS`, `MAKEOVERRIDES`, `MAKELEVEL`, `MAKEFILES`, `GNUMAKEFLAGS`,
`GIT_DIR`, `GIT_WORK_TREE` and `GIT_INDEX_FILE` (`MAKEFLAGS=n` only prints the
recipes, `=i` ignores failures, `=t` touches targets), runs make with
`GOFLAGS=-count=1` (harmless, but a push runs the tests without the test cache; it
is non-empty so it beats a `GOFLAGS` in the `go.env` config file, which an empty
value would not), and names the base itself (`make BASE_REF=origin/main gates`;
the command line beats the environment inside make).

Trust boundary. The hook trusts the local environment and git configuration:
`BASH_ENV`, `git update-index --assume-unchanged` and `.git/info/exclude` (they
hide changes from the clean-tree check), `GIT_CONFIG_*` and `core.hooksPath`,
`PATH`, the tools in `.bin`, and the local `origin/main` ref, which the gate takes
as the base (a moved `origin/main` makes the change look empty to `make test` and
`make lint`). Using any of these to get around the gate is a deliberate bypass,
and the owner's rules forbid it, as they forbid skipping hooks. The backstops are
that CI runs the same gates on every pull request against the PR's real base, and
that the owner merges every pull request (ADR 0024).
`scripts/test-pre-push.sh` drives the real hook with a real `git push` to a
throwaway bare repository, never to `origin`, with a fake `make` first on
`PATH` that records what the gate was given (and exits 0 or 1).

## Pinned tool versions

`tool-versions.env` is the one file that holds every pinned version: Go, Node
major, `golangci-lint`, `buf`, `gofumpt`, `typescript` and the `protoc-gen-es`
version. The Makefile includes it and CI sources it, so neither spells out a
version. `make tools` builds `golangci-lint` and `gofumpt` with the local Go
into `.bin/` (gitignored); the `golangci-lint` on `PATH` is built with an older
Go and refuses a `go 1.27` module, so the Makefile never uses it. Four copies
that other tools force on us (`.node-version`, the `go` line of `go.mod`,
`buf.gen.yaml`, `tools/tscheck/package.json`) are kept equal by
`scripts/check-versions.sh`. To upgrade a tool, change the file in a dedicated
PR (ADR 0020), run that script, and read the release notes.

## Tiers, roles and models

Quality comes from the gates, which pass or fail the same way for every author,
and from scope boxing: a weaker model gets a narrow, pre-verified task and
limited tools. The skills hold the method; the role files only bind role,
access and model. Roles and models are defined once in
[.agents/roles.json](../../.agents/roles.json), the only place a tier's model
changes. A subagent never changes its own model. OpenCode's and Cursor's models
stay a session choice: the tier table below does not apply to them, and a
Cursor role runs on the session's model.

| Tier | Claude | Codex | Muse | Cost posture |
|---|---|---|---|---|
| fast | haiku | gpt-6-luna (high) | muse-spark-1.3 (low) | cheapest; scanning only |
| workhorse | sonnet | gpt-6-sol (medium) | muse-spark-1.3 (high) | the default for real work |
| deep | opus | gpt-6-sol (high) | muse-spark-1.3 (xhigh) | only on escalation |
| arbiter | fable | gpt-6-astra (high) | muse-spark-1.3 (xhigh) | only for rulings |

| Role | Tier | Access | Skill | Job |
|---|---|---|---|---|
| `scan-fleet` | fast | read | - | bounded evidence scan with `file:line` citations; no verdicts or fixes |
| `develop-fleet` | workhorse | write | loads by surface | implement a scoped handoff test-first, run focused gates, commit; coordinator pushes |
| `develop-fleet-deep` | deep | write | same | concurrency, single-writer store, agent wire protocol, tmux control mode, or a fix the workhorse got wrong |
| `debug-fleet` | workhorse | read | `fleet-diagnostic-rigor` | root cause without editing, returns an implementation handoff |
| `debug-fleet-deep` | deep | read | same | cross-system or intermittent causes |
| `perf-fleet` | workhorse | read | `fleet-performance-rigor` | measurement and bottleneck, no editing |
| `perf-fleet-deep` | deep | read | same | hard performance causes and architecture decisions |
| `review-fleet` | workhorse | read | `fleet-code-review` | independent diff review; the coordinator runs the spec and standards axes as two reviewers in parallel and writes one verdict |
| `review-fleet-deep` | deep | read | same | review of a difficult diff |
| `arbiter-fleet` | arbiter | read | - | binding ruling starting `RULING:` |

**Start cheap, escalate on evidence.** Scan with `scan-fleet` before reading
widely yourself. Default to the workhorse. Escalate to `-deep` on a trigger: the
surface is concurrency, the single-writer store, the agent wire protocol
(delivery, epochs, idempotency) or tmux control mode; a workhorse attempt failed
review or tests for a reason it could not fix; or the cause stayed unknown after
a workhorse pass. Give the deep role the failed attempt's evidence, not a blank
slate. Do not run a deep role to double-check a result you can verify with a
command. Keep handoffs small: the task, the files, the acceptance test, the
skills. Context re-sent to every agent is the largest avoidable cost.

## Task-to-skill map

Load the skills for the surface you touch, in this order (the one-line "use
when" of each skill is in AGENTS.md "Skill Routing").

| Task | Skills |
|---|---|
| Go package, CLI, hub or agent logic | `fleet-go` |
| New or changed RPC, message, field | `fleet-contract-rigor`, then `fleet-go` |
| Hub storage, migrations, log persistence | `fleet-sqlite-store`, `fleet-go` |
| Screen parser, new harness version | `fleet-harness-adapter`, plus `fleet-live-probe` when the behavior is unknown |
| tmux observation or control | `fleet-tmux`, `fleet-security` |
| New metric, span, event kind | `fleet-telemetry-coverage` |
| Dashboard, Connect client, terminal attach | `fleet-frontend`, plus `fleet-contract-rigor` for API gaps |
| Auth, scopes, dispatch gate, prompts | `fleet-security` |
| Unknown cause | `fleet-diagnostic-rigor` |
| Speed, load, disk, memory | `fleet-performance-rigor` |
| Before push or merge | `fleet-code-review` |
| Issue to merged PR | `fleet-issue-driver` |
| Anything the owner reads (replies, PR and issue text, summaries) | `fleet-humanizer` |

## The handoff contract

A dispatch to a leaf role states: the goal in one sentence; the worktree path
and branch; the acceptance test or evidence wanted; the files in scope and out
of scope; the skills to load; the gates to run; what to return. Every return
lists commands with exit codes (`PASS`, `FAIL`, `NOT_CHECKED`), `file:line`
evidence, remaining uncertainty and the next action. The orchestrator
re-verifies a subagent's finding itself before acting on it or passing it on as
verified. Commit before dispatching a reviewer so a stray edit is recoverable.
Read-only roles never edit, switch branches or publish; if a command is blocked
for them, the coordinator supplies its exact output.

## Dispatch and escalation rules

- Executors work in one worktree on one surface at a time; two executors never
  share a worktree.
- Stop leaf agents when their handoff is consumed.
- Ask `arbiter-fleet` one question per ruling, with the raw observations in the
  brief; record the `RULING:` in the goal file or PR.
- The active goal authorizes the acts needed to complete it. Scope approval is
  not permission for unrelated acts. `CONSENT:` lines record grants and are
  never invented or widened ([hooks.md](hooks.md)).

## Running a role in each harness

- **Claude Code:** the generated `.claude/agents/<role>.md` subagents; invoke by
  name (the Agent tool with `subagent_type: <role>`).
- **Codex:** `.codex/agents/<role>.toml`, or
  `python3 scripts/agent-roles.py codex-exec <role> "<task>"`.
- **Muse:** `python3 scripts/agent-roles.py muse-exec <role> "<task>"`.
- **OpenCode:** `.opencode/agent/<role>.md`; pick its model in the session.
- **Cursor:** `.cursor/agents/<role>.md`. Each role uses `model: inherit`, so it
  runs on the session's model (Auto, when the session is on Auto). Cursor also
  loads the `.claude/` and `.codex/` copies, which pin Claude and OpenAI models;
  the `.cursor/` copy wins because it has the same name. Read-only roles set
  `readonly: true` (no file edits and no state-changing shell commands; such a
  role asks the coordinator for command output it cannot produce).

`codex-exec` and `muse-exec` run a role headless. That is allowed for developing
this repo only; the product never runs a harness headless (ADR 0024, AGENTS.md
principle 1). Use `--dry-run` to print the command and prompt first.
