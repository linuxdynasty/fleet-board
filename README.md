# fleet-board

Hub and agents for managing Claude Code, Codex, Muse and OpenCode sessions running
in tmux across several machines: see every session, answer agent questions,
unstick stuck work, and assign issues and PRs to machines with free capacity.

- [docs/design.md](docs/design.md): architecture, views, dispatch rules,
  safety rails, verified facts, open questions, milestones
- [docs/development/](docs/development/README.md): how agents develop this
  repo: roles, cost tiers, skills and local gates (rules in
  [AGENTS.md](AGENTS.md))
- [mockup/](mockup/): clickable mockup of the six views (Triage, Work,
  Session, Usage, Machines, Settings)

Status: design and mockup only. No hub or agent code yet.
