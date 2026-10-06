# Developing fleet-board with agents

fleet-board is built by AI agents under an owner who keeps cost low: cheap
models scan, a workhorse model does the everyday work, and the expensive deep
and arbiter models are used only when the work earns them. This directory is the
detail behind the short root [AGENTS.md](../../AGENTS.md).

| Doc | What it covers |
|---|---|
| [hooks.md](hooks.md) | Development hooks for Claude, Codex, Muse and Cursor: skill nudge and its path map, goal protocol and Stop guard, doc staleness, role router, spawn guard, how to turn each off, tests |
| [agent-guide.md](agent-guide.md) | Recipes and traps AGENTS.md does not carry: worktree and git recipes, prove the theory first, macOS and zsh notes, CI runs and the minutes budget, roles and models per harness, the task-to-skill map, the handoff contract, dispatch and consent rules, how to run a role |

## The setup in one screen

```text
.agents/roles.json            single source: model tiers per harness + roles
.agents/skills/<name>/        project skills (SKILL.md, "Use when ...")
scripts/agent-roles.py        renders the role files below; --check detects drift
scripts/verify-agent-canon.sh checks the rendered files, skill links, frontmatter,
                              the skill path map and the hook configs
.agents/skill-paths.json      path-to-skill map read by the skill-nudge hook
.agents/doc-paths.json        folder-to-spec map read by check-docs-stale.py (hooks.md)
.agents/doc-names-allow.json  backticked doc tokens that are not proto names on purpose
.claude/hooks/                development hooks shared by Claude, Codex, Muse and Cursor
.claude/settings.json, .codex/hooks.json, .muse/hooks.json, .cursor/hooks.json   hook wiring
scripts/cursor-hook.py        adapter: runs those hook scripts from Cursor's native hooks (hooks.md)
scripts/cursor_hook_family.py helper-agent links and the opt-in payload log; the adapter needs it next to
                              it (without it the guards deny and the rest fail open, hooks.md)
scripts/test-dev-hooks.sh     self-test for the hooks (hooks.md)
scripts/test-cursor-hook.py, test-cursor-hook-edges.py, test-cursor-hook-family.py,
test-cursor-hook-worktrees.py, cursor_hook_testkit.py
                              self-test for the Cursor adapter and .cursor/hooks.json (make selftest)
scripts/owner-setup.sh        walks the owner through owner-only setup steps (plan section 3)
scripts/test-owner-setup.sh   self-test for owner-setup.sh, run with each bash on the machine
Makefile, tool-versions.env   make targets and the one file of pinned tool versions (agent-guide.md)
.githooks/pre-push            refuses a push to main; before a branch push, fetches origin main
                              and runs make gates
.github/workflows/ci.yml      CI on PRs and main pushes: one step per gate, the same targets as
                              make gates (agent-guide.md, "CI runs and the minutes budget")
.github/workflows/pr-meta.yml the quick PR checks: the base is main, no AI attribution in the
                              title and body
scripts/check-pr-base.sh      fails unless a PR targets main (pr-meta.yml, and first in the ci.yml gates job)
scripts/check-versions.sh     keeps the copies of a pin (.node-version, go.mod, buf.gen.yaml,
                              tools/tscheck) equal to tool-versions.env
scripts/smoke-binaries.sh     builds each cmd/<name> and checks that --version prints its name
scripts/changed-go-packages.sh   the Go packages a change affects, for make test and make lint
scripts/check-gofumpt.sh      fails on a Go file gofumpt would change
scripts/test-check-versions.sh, test-changed-go-packages.sh, test-check-gofumpt.sh,
  test-pre-push.sh, test-pre-push-fetch.sh, test-check-pr-base.sh
                              seeded RED/GREEN self-tests for the scripts and hook above; make
                              selftest runs them
scripts/test-verify-agent-canon.sh, test-smoke-binaries.sh, test-make-base-ref.sh,
  test-make-tools.sh, test-make-gates.sh, test-make-test-flags.sh   seeded cases for the canon
  script checks, the smoke, the quoting of BASE_REF, the tools/gen rebuild rules, the
  ref-named-file guard, the wiring of make gates and the shuffle and repeat flags of make test
  (also in make selftest)
tools/tscheck/                compiles the generated TypeScript (typescript 7, strict)
scripts/plan-issues.py        writes each plan row's GitHub issue body from the plan
                              (--print, --check, --apply; ADR 0026)
scripts/test-agent-roles.py   self-test for agent-roles.py (make selftest)
scripts/test-plan-issues.py   self-test for plan-issues.py, no network; test-dev-hooks.sh runs it
scripts/check-docs-stale.py   doc-staleness reminders (hook mode) and the blocking --ci check
                              (make docs-stale-ci); validates doc-paths.json (--check-map)
scripts/check-doc-names.py    every backticked proto name in the docs is in api/fleet (make doc-names)
scripts/gen-doc-tables.py     writes the spec tables (02k, 02d, 02i, 02) from the proto comment
                              tags, between generated markers (--write; make doc-tables is --check)
scripts/report-capability-tests.py   v1 capabilities in 01 that no product test names (gate
                              self-tests do not count); a report until plan row 17
                              (make capability-tests)
scripts/proto_image.py        loads the contract from the pinned buf's compiled image, for the
                              doc-drift scripts (names, field types, comments)
scripts/proto_tags.py         reads and checks the comment tags (02e, "Comment tags")
scripts/test-check-docs-stale.py, test-check-doc-names.py, test-proto-image.py,
  test-gen-doc-tables.py, test-report-capability-tests.py,
  proto_image_testkit.py      seeded cases for the scripts above (make selftest)
.claude/agents/*.md           generated (Claude Code subagents)
.codex/agents/*.toml          generated (Codex custom agents)
.opencode/agent/*.md          generated (OpenCode agents)
.cursor/agents/*.md           generated (Cursor subagents; model: inherit)
.claude/skills/, .codex/skills/   symlinks to .agents/skills/<name>
AGENTS.md                     the rules every harness reads (no CLAUDE.md)
```

Muse has no project agent files; `scripts/agent-roles.py muse-exec <role>
"<task>"` runs a role there with the tier's model and the role's access. The
`codex-exec` and `muse-exec` helpers run headless, for developing this repo only;
the product never runs a harness headless (ADR 0024).

## Change the setup

1. Edit `.agents/roles.json` (a role, a tier's model) or a `SKILL.md`.
2. `python3 scripts/agent-roles.py generate`.
3. `python3 scripts/agent-roles.py --check && scripts/verify-agent-canon.sh`.
4. A new skill needs its `.claude/skills/<name>` and `.codex/skills/<name>`
   symlinks (`../../.agents/skills/<name>`) and a row in the AGENTS.md skill
   table; the verifier fails without them.

`scripts/verify-agent-canon.sh` runs on the macOS system bash 3.2 as well as
Homebrew bash; it uses no bash 4 features.
