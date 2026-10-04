# Developing fleet-board with agents

fleet-board is built by AI agents under an owner who keeps cost low: cheap
models scan, a workhorse model does the everyday work, and the expensive deep
and arbiter models are used only when the work earns them. This directory is the
detail behind the short root [AGENTS.md](../../AGENTS.md).

| Doc | What it covers |
|---|---|
| [hooks.md](hooks.md) | Development hooks for Claude, Codex and Muse: skill nudge and its path map, goal protocol and Stop guard, doc staleness, role router, spawn guard, how to turn each off, tests |
| [agent-guide.md](agent-guide.md) | Recipes and traps AGENTS.md does not carry: worktree and git recipes, prove the theory first, macOS and zsh notes, roles and models per harness, the task-to-skill map, the handoff contract, dispatch and consent rules, how to run a role |

## The setup in one screen

```text
.agents/roles.json            single source: model tiers per harness + roles
.agents/skills/<name>/        project skills (SKILL.md, "Use when ...")
scripts/agent-roles.py        renders the role files below; --check detects drift
scripts/verify-agent-canon.sh checks the rendered files, skill links, frontmatter,
                              the skill path map and the hook configs
.agents/skill-paths.json      path-to-skill map read by the skill-nudge hook
.claude/hooks/                development hooks shared by Claude, Codex and Muse
.claude/settings.json, .codex/hooks.json, .muse/hooks.json   hook wiring
scripts/test-dev-hooks.sh     self-test for the hooks (hooks.md)
scripts/owner-setup.sh        walks the owner through owner-only setup steps (plan section 3)
scripts/test-owner-setup.sh   self-test for owner-setup.sh, run with each bash on the machine
Makefile, tool-versions.env   make targets and the one file of pinned tool versions (agent-guide.md)
.githooks/pre-push            refuses a push to main, runs make gates before a branch push
.github/workflows/ci.yml      CI: one step per gate, the same targets as make gates
scripts/check-versions.sh     keeps the copies of a pin (.node-version, go.mod, buf.gen.yaml,
                              tools/tscheck) equal to tool-versions.env
scripts/smoke-binaries.sh     builds each cmd/<name> and checks that --version prints its name
scripts/changed-go-packages.sh   the Go packages a change affects, for make test and make lint
scripts/check-gofumpt.sh      fails on a Go file gofumpt would change
scripts/test-check-versions.sh, test-changed-go-packages.sh, test-check-gofumpt.sh,
  test-pre-push.sh            seeded RED/GREEN self-tests for the four above; make selftest runs them
scripts/test-verify-agent-canon.sh, test-smoke-binaries.sh, test-make-base-ref.sh   seeded cases for the canon script
  checks, the smoke and the quoting of BASE_REF (also in make selftest)
tools/tscheck/                compiles the generated TypeScript (typescript 7, strict)
scripts/plan-issues.py        writes each plan row's GitHub issue body from the plan
                              (--print, --check, --apply; ADR 0026)
scripts/test-plan-issues.py   self-test for plan-issues.py, no network; test-dev-hooks.sh runs it
.claude/agents/*.md           generated (Claude Code subagents)
.codex/agents/*.toml          generated (Codex custom agents)
.opencode/agent/*.md          generated (OpenCode agents)
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
