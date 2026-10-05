# Architecture decision records

Each ADR records one decision: the context, the options considered, what was
decided, and what follows from it. ADRs are not edited after they are
accepted; a later ADR supersedes an earlier one and says so. The Relations
column mirrors the headers of the later ADRs, so a reader of an old ADR sees
what changed it; read the later ADR before relying on the old text.

When documents disagree, the order of authority is on [design.md](../design.md).
The owner's decisions of 2026-09-28 to 2026-10-05 are recorded in
[0022](0022-drift-audit-decisions.md), [0024](0024-pre-build-owner-decisions.md),
[0025](0025-v1-includes-dispatch.md), [0026](0026-one-home-per-fact.md) and, newest,
[0027](0027-cursor-first-class-copilot-and-messaging-later.md) (2026-10-05).

| ADR | Title | Status | Relations |
|---|---|---|---|
| [0001](0001-system-architecture.md) | System architecture and dependencies | Accepted | Confirmed by 0014; package list amended by 0022 |
| [0002](0002-storage-sqlite.md) | Storage: embedded SQLite | Accepted | Confirmed by 0014 |
| [0003](0003-transport-grpc.md) | Transport and contract: gRPC with protobuf | Accepted | Superseded in part by 0017; amended by 0018, 0019; library question resolved by 0016 |
| [0004](0004-agent-connectivity.md) | Agent connectivity and command model | Accepted | Superseded in part by 0011; amended by 0018, 0019, 0022, 0024 (harness config) |
| [0005](0005-github-app.md) | GitHub integration through a GitHub App | Accepted | Amended by 0012, 0022, 0024 (App only, no token fallback) |
| [0006](0006-tmux-sessions.md) | tmux: fleet-owned servers and user-created sessions | Accepted | Superseded in part by 0011; amended by 0024 (no folder pre-trust) |
| [0007](0007-web-ui-react.md) | Web UI: React, TypeScript and Vite, embedded in the hub | Accepted | Open question resolved by 0016; the four views it names have grown ([ui-contract-map](../specs/ui-contract-map.md)) |
| [0008](0008-goal-consent-and-account-modes.md) | Configurable goal consent and interactive-only accounts | Accepted | The ready-for-human-merge rule confirmed by 0024 |
| [0009](0009-machine-groups-and-layered-settings.md) | Machine groups and layered settings | Accepted | Superseded in part by 0011 |
| [0010](0010-session-logs.md) | Session logs and retention | Accepted | Superseded in part by 0011, 0015; amended by 0013, 0019 |
| [0011](0011-contract-amendments.md) | Contract amendments to 0004, 0006, 0009 and 0010 | Accepted | Amended by 0012, 0018 |
| [0012](0012-integration-model.md) | Integration model: work trackers and code hosts | Accepted | - |
| [0013](0013-live-state-retention.md) | Live-state retention: latest pane summary only | Accepted | Amended by 0015 |
| [0014](0014-storage-confirmed.md) | Storage confirmed: embedded SQLite, settings from step 4 | Accepted | - |
| [0015](0015-session-logs-in-sqlite.md) | Session logs stored in logs.db | Accepted | - |
| [0016](0016-browser-transport.md) | Browser transport: Connect, and WebSocket for terminal attach | Accepted | Amended by 0019 |
| [0017](0017-contract-packages.md) | Contract packages and names | Accepted | Amended by 0018, 0019 |
| [0018](0018-hub-api-and-contract-amendments.md) | Hub API, principals and devices, routines, profiles | Accepted | Amended by 0019, 0022 |
| [0019](0019-pre-implementation-contract-decisions.md) | Pre-implementation contract decisions: reuse, sign-in, freeze, log access | Accepted | Amended by 0022; decision 9 (the freeze) amended by 0024 |
| [0020](0020-dependencies.md) | Languages, frameworks and libraries | Accepted | Amended by 0021 (OTLP exporters), 0022, 0023 |
| [0021](0021-insights-and-observability.md) | Insights from the fleet's own data, and observability of the fleet itself | Accepted | Questions 1 to 5 answered by 0022 (question 1 in its 2026-10-02 addendum); amends 0020's table |
| [0022](0022-drift-audit-decisions.md) | Drift audit decisions: pane context, hub runtime, dependencies, GitHub App | Accepted | Answers 0021; superseded in part by 0023 (decision 3); decision 9 (flows in the order of authority) superseded in part by 0026; addendum of 2026-10-02: owner decisions, the v1 command path (changes decision 5), open questions; its questions 1 to 11 answered by 0024 |
| [0023](0023-additional-libraries.md) | Additional libraries: mDNS and system sampling | Accepted | Builds on 0020; supersedes in part 0022 |
| [0024](0024-pre-build-owner-decisions.md) | Pre-build owner decisions: observed goals, who merges, harness config, machine admin, size dropped, freeze scope, install, backups | Accepted | Amends 0004, 0005, 0006, 0019, 0022; answers 0022's questions; three decisions deferred; amended by 0025 (decisions 8, 20, 21, 23) |
| [0025](0025-v1-includes-dispatch.md) | v1 includes dispatch: v2 merged into v1, no v2 label, watch first then dispatch, `v0.1` freezes all of v1 | Accepted | Supersedes the release split in design milestones and 0022's v2 placements; amends 0024 decisions 8, 20, 21, 23; amended by 0027 (Cursor joins the harnesses) |
| [0026](0026-one-home-per-fact.md) | One home per fact: each kind of fact lives once, flows retired, review files dropped, tables and issue bodies generated | Accepted | Supersedes the flows' place in the order of authority (0022 addendum) |
| [0027](0027-cursor-first-class-copilot-and-messaging-later.md) | Cursor is a first-class harness in v1; Copilot and sending messages into a session come later | Accepted | Amends 0025 (the harness list); builds on 0024 decision 3 |

## Template

```markdown
# NNNN. Title

- Status: Proposed | Accepted | Superseded by NNNN
- Date: YYYY-MM-DD
- Deciders: ...
- Supersedes, in part: NNNN (what) | Amends: NNNN (what) | Confirms: NNNN |
  Builds on: NNNN | Resolves: NNNN (the open question)   (only the lines that apply)

## Context
## Options
## Decision
## Consequences
## Open questions
```
