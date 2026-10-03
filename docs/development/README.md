# Developing fleet-board with agents

fleet-board is built by AI agents under an owner who keeps cost low: cheap
models scan, a workhorse model does the everyday work, and the expensive deep
and arbiter models are used only when the work earns them. This directory is the
detail behind the short root [AGENTS.md](../../AGENTS.md).

| Doc | What it covers |
|---|---|
| [agent-orchestration.md](agent-orchestration.md) | Roles, cost tiers and model bindings per harness, task-to-skill map, handoff contract, how to run a role in each harness |
| [hooks.md](hooks.md) | Development hooks for Claude, Codex and Muse: skill nudge and its path map, goal protocol and Stop guard, doc staleness, role router, spawn guard, how to turn each off, tests |
| [agent-guide.md](agent-guide.md) | Working rules in detail: worktrees and git hygiene, evidence capture, prove the theory first, escalation, macOS and zsh notes, documentation workflow |

## The setup in one screen

```text
.agents/roles.json            single source: model tiers per harness + roles
.agents/skills/<name>/        14 project skills (SKILL.md, "Use when ...")
scripts/agent-roles.py        renders the role files below; --check detects drift
scripts/verify-agent-canon.sh checks the rendered files, skill links, frontmatter,
                              the skill path map and the hook configs
.agents/skill-paths.json      path-to-skill map read by the skill-nudge hook
.claude/hooks/                development hooks shared by Claude, Codex and Muse
.claude/settings.json, .codex/hooks.json, .muse/hooks.json   hook wiring
scripts/test-dev-hooks.sh     self-test for the hooks (hooks.md)
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
