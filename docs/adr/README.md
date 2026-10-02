# Architecture decision records

Each ADR records one decision: the context, the options considered, what was
decided, and what follows from it. ADRs are not edited after they are
accepted; a later ADR supersedes an earlier one and says so.

| ADR | Title | Status |
|---|---|---|
| [0001](0001-system-architecture.md) | System architecture and dependencies | Accepted |
| [0002](0002-storage-sqlite.md) | Storage: embedded SQLite | Accepted |
| [0003](0003-transport-grpc.md) | Transport and contract: gRPC with protobuf | Accepted |
| [0004](0004-agent-connectivity.md) | Agent connectivity and command model | Accepted |
| [0005](0005-github-app.md) | GitHub integration through a GitHub App | Accepted |
| [0006](0006-tmux-sessions.md) | tmux: fleet-owned servers and user-created sessions | Accepted |
| [0007](0007-web-ui-react.md) | Web UI: React, TypeScript and Vite, embedded in the hub | Accepted |
| [0008](0008-goal-consent-and-account-modes.md) | Configurable goal consent and interactive-only accounts | Accepted |
| [0009](0009-machine-groups-and-layered-settings.md) | Machine groups and layered settings | Accepted |
| [0010](0010-session-logs.md) | Session logs and retention | Accepted |
| [0011](0011-contract-amendments.md) | Contract amendments to 0004, 0006, 0009 and 0010 | Accepted |
| [0012](0012-integration-model.md) | Integration model: work trackers and code hosts | Accepted |
| [0013](0013-live-state-retention.md) | Live-state retention: latest pane summary only | Accepted |
| [0014](0014-storage-confirmed.md) | Storage confirmed: embedded SQLite, settings from step 4 | Accepted |
| [0015](0015-session-logs-in-sqlite.md) | Session logs stored in logs.db | Accepted |
| [0016](0016-browser-transport.md) | Browser transport: Connect, and WebSocket for terminal attach | Accepted |
| [0017](0017-contract-packages.md) | Contract packages and names | Accepted |
| [0018](0018-hub-api-and-contract-amendments.md) | Hub API, principals and devices, routines, profiles | Accepted |
| [0019](0019-pre-implementation-contract-decisions.md) | Pre-implementation contract decisions: reuse, sign-in, freeze, log access | Accepted |
| [0020](0020-dependencies.md) | Languages, frameworks and libraries | Accepted |
| [0021](0021-insights-and-observability.md) | Insights from the fleet's own data, and observability of the fleet itself | Accepted |

## Template

```markdown
# NNNN. Title

- Status: Proposed | Accepted | Superseded by NNNN
- Date: YYYY-MM-DD
- Deciders: ...

## Context
## Options
## Decision
## Consequences
## Open questions
```
