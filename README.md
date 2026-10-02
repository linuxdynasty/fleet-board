# fleet-board

Hub and agents for managing Claude Code, Codex, Muse and OpenCode sessions running
in tmux across several machines: see every session, answer agent questions,
unstick stuck work, and assign issues and PRs to machines with free capacity.

- [docs/design.md](docs/design.md): architecture, views, dispatch rules,
  safety rails, open questions, milestones
- [docs/specs/](docs/specs/README.md): capabilities, contracts, workflows,
  technology review, insights and observability, every default in one table
- [docs/adr/](docs/adr/README.md): the architecture decisions (0001-0023) and
  which later one amends which earlier one
- [docs/flows/](docs/flows/README.md): the whole system on one page, and one
  flow per situation with the exact message names
- [api/](api/fleet/): the protobuf contracts (`fleet.type.v1`, `fleet.agent.v1`,
  `fleet.integration.v1`, `fleet.hub.v1`), checked with buf
- [spikes/](spikes/README.md): throwaway measurements behind the storage and
  compression choices
- [docs/plan/v1-implementation-plan.md](docs/plan/v1-implementation-plan.md):
  the ordered PR plan for v1, the read-only fleet
- [docs/development/](docs/development/README.md): how agents develop this
  repo: roles, cost tiers, skills and local gates (rules in
  [AGENTS.md](AGENTS.md))
- [mockup/](mockup/): clickable mockup of the seven views (Triage, Work,
  Session, Usage, Machines, Settings, Insights)

Status: design phase complete (contracts, specs, ADRs 0001-0023, flows, v1
plan) and mockup. No hub or agent code yet; implementation starts when the
owner approves the plan.
