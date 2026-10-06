# fleet-board

Hub and agents for managing Claude Code, Codex, Muse, Cursor and OpenCode sessions running
in tmux across several machines: see every session, answer agent questions,
unstick stuck work, and assign issues and PRs to machines with free capacity.

- [docs/features.md](docs/features.md): every feature fleet-board will support, by
  release (v1, v3, later), with where it is specified and its issue; start here
- [docs/design.md](docs/design.md): the system picture, the glossary, the order of
  authority and where each fact lives
- [docs/specs/](docs/specs/README.md): capabilities, contracts, workflows,
  technology review, insights and observability, every default in one table
- [docs/adr/](docs/adr/README.md): the architecture decisions and which later
  one amends which earlier one
- [api/](api/fleet/): the protobuf contracts (`fleet.type.v1`, `fleet.agent.v1`,
  `fleet.integration.v1`, `fleet.hub.v1`), checked with buf
- [spikes/](spikes/README.md): throwaway measurements behind the storage and
  compression choices
- [docs/plan/v1-implementation-plan.md](docs/plan/v1-implementation-plan.md):
  the ordered PR plan for v1: watching the fleet first, then dispatch
- [docs/development/](docs/development/README.md): how agents develop this
  repo: roles, cost tiers, skills and local gates (rules in
  [AGENTS.md](AGENTS.md))
- [mockup/](mockup/): clickable mockup of the seven views (Triage, Work,
  Session, Usage, Machines, Settings, Insights)

Status: design phase complete (contracts, specs, ADRs, v1 plan) and mockup. The owner approved the plan; implementation follows it. No
hub or agent code is merged yet.
