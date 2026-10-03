# Agent orchestration and cost tiers

How work is split across agents and models without losing quality, and without
spending deep-model tokens on work a cheap model can do. The binding rules are in
[AGENTS.md](../../AGENTS.md); the role definitions are
[.agents/roles.json](../../.agents/roles.json).

## The principle

Quality does not come from prose. A model may skim any rule file. It comes from
(1) the local and CI gates that pass or fail the same way for every author, and
(2) scope boxing: a weaker model gets a narrow, pre-verified task and limited
tools, never open-ended trust. The skills hold the method; the role files only
bind role, access and model.

## Tiers and default model bindings

| Tier | Claude | Codex | Muse | Cost posture |
|---|---|---|---|---|
| fast | haiku | gpt-6-luna (high) | muse-spark-1.3 (low) | cheapest; scanning only |
| workhorse | sonnet | gpt-6-sol (medium) | muse-spark-1.3 (high) | the default for real work |
| deep | opus | gpt-6-sol (high) | muse-spark-1.3 (xhigh) | only on escalation |
| arbiter | fable | gpt-6-astra (high) | muse-spark-1.3 (xhigh) | only for rulings |

The manifest is the only place a tier's model changes. A subagent never changes
its own model, and OpenCode's model stays a session choice.

## Roles

| Role | Tier | Access | Skill | Job |
|---|---|---|---|---|
| `scan-fleet` | fast | read | - | bounded evidence scan with `file:line` citations; no verdicts or fixes |
| `develop-fleet` | workhorse | write | loads by surface | implement a scoped handoff test-first, run focused gates, commit; coordinator pushes |
| `develop-fleet-deep` | deep | write | same | concurrency, single-writer store, agent wire protocol, tmux control mode, or a fix the workhorse got wrong |
| `debug-fleet` | workhorse | read | `fleet-diagnostic-rigor` | root cause without editing, returns an implementation handoff |
| `debug-fleet-deep` | deep | read | same | cross-system or intermittent causes |
| `perf-fleet` | workhorse | read | `fleet-performance-rigor` | measurement and bottleneck, no editing |
| `perf-fleet-deep` | deep | read | same | hard performance causes and architecture decisions |
| `review-fleet` | workhorse | read | `fleet-code-review` | independent diff review and verdict |
| `review-fleet-deep` | deep | read | same | review of a difficult diff |
| `arbiter-fleet` | arbiter | read | - | binding ruling starting `RULING:` |

## Cost policy: start cheap, escalate on evidence

1. **Scan with `scan-fleet`** before reading widely yourself: file inventories,
   "where is X used", spec lookups, fixture lists. Its output is cited facts.
2. **Default to the workhorse** for implementation, diagnosis, measurement and
   review. Most work, including most Go, parsers and UI, ends here.
3. **Escalate to `-deep`** only on a trigger: the surface is concurrency, the
   single-writer store, the agent wire protocol (delivery, epochs, idempotency)
   or tmux control mode; or a workhorse attempt failed review or tests for a
   reason it could not fix; or the cause stayed unknown after a workhorse pass.
   Give the deep role the failed attempt's evidence, not a blank slate.
4. **Call `arbiter-fleet` rarely**: a design fork the docs do not settle, a
   disputed review finding, a waiver, or an irreversible act (merge, release,
   data change). One question per ruling, raw observations in the brief.
5. Do not run a deep role to double-check a workhorse result you can verify with
   a command. Verify with the command.
6. Keep handoffs small: the task, the files, the acceptance test, the skills.
   Context re-sent to every agent is the largest avoidable cost.

## Task-to-skill map

| Task | Skills |
|---|---|
| Go package, CLI, hub or agent logic | `fleet-go` |
| New or changed RPC, message, field | `fleet-contract-rigor`, then `fleet-go` |
| Hub storage, migrations, log persistence | `fleet-sqlite-store`, `fleet-go` |
| Screen parser, new harness version | `fleet-harness-adapter`, `fleet-live-probe` (if behavior unknown) |
| tmux observation or control | `fleet-tmux`, `fleet-security` |
| New metric, span, event kind | `fleet-telemetry-coverage` |
| Dashboard, Connect client, terminal attach | `fleet-frontend`, `fleet-contract-rigor` for API gaps |
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

## Dispatch rules

- Leaf roles (scan, develop, debug, perf, review) do not dispatch other agents.
- Read-only roles never edit, switch branches or publish. If a command is
  blocked for them, the coordinator supplies its exact output.
- Executors work in one worktree on one surface at a time; two executors never
  share a worktree.
- Only the orchestrator runs the full local gate (once, before push), pushes,
  opens and updates PRs, and merges. Stop leaf agents when their handoff is
  consumed.

## Running a role in each harness

- **Claude Code:** the generated `.claude/agents/<role>.md` subagents; invoke by
  name (the Agent tool with `subagent_type: <role>`).
- **Codex:** `.codex/agents/<role>.toml`, or
  `python3 scripts/agent-roles.py codex-exec <role> "<task>"`.
- **Muse:** `python3 scripts/agent-roles.py muse-exec <role> "<task>"`.
- **OpenCode:** `.opencode/agent/<role>.md`; pick its model in the session.

`codex-exec` and `muse-exec` run a role headless. That is allowed for developing
this repo only; the product never runs a harness headless (ADR 0024, AGENTS.md
principle 1).

Use `--dry-run` on the exec commands to print the command and prompt first.

## The gate floor

The constant floor is the same for every model: `gofumpt`, `go vet`, tests,
`golangci-lint`, `buf build` and `buf lint`, the file cap, the agent-canon
check, and (once it exists) CI. See AGENTS.md "Local Gates". Prose rules route
and box; the gates catch the mechanics.
