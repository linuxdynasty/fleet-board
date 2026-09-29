# 0001. System architecture and dependencies

- Status: Accepted
- Date: 2026-09-29
- Deciders: linuxdynasty

## Context

fleet-board is meant to be self-hosted by individuals and small groups, and
possibly open-sourced. The design ([../design.md](../design.md)) sets the
constraints:

- Scale is small: roughly 2–50 machines, up to a few hundred live goals,
  thousands of issues, a few events per second.
- It must never collect, store, copy or proxy provider credentials (see
  [../research/terms-of-service.md](../research/terms-of-service.md)).
- The hub may be a laptop that sleeps and changes networks; workers may be
  behind NAT, in containers, or asleep.
- Work runs in tmux, as unmodified harness binaries.

Every extra service (a database server, a cache, a broker) is something each
user must install, upgrade, back up and debug before seeing any value.

## Options

1. Single hub binary with embedded storage, one agent binary per machine.
2. Hub plus Postgres and Redis (queue, pub/sub, locks).
3. Hub plus a graph database (Neo4j, NornicDB) for the relationship model.
4. Hub plus a message broker (NATS, MQTT) between hub and agents.

## Decision

Option 1. Three Go binaries built from one repo:

| Binary | Runs on | Role |
|---|---|---|
| `fleetd` | one machine (the hub) | API, scheduler, GitHub sync, verifier, triage, embedded web UI, agent gateway |
| `fleet-agent` | every worker, as the logged-in user | tmux driver, goal-file watcher, usage prober, preflight, local command log |
| `fleet` | anywhere | CLI over the hub API, `--json` output as the stable interface |

Plus goal-protocol hook scripts per harness, installed by the agent.

**Dependencies at runtime:** none beyond the binaries. Storage is embedded
SQLite ([0002](0002-storage-sqlite.md)). No Redis: pub/sub to the UI, the
dispatch queue and mutual exclusion are in-process at this scale. No graph
database: the data is a shallow, fixed shape (account → machine → session →
goal → PR → issue) that relational joins handle directly. No broker: agents
hold one gRPC stream each ([0003](0003-transport-grpc.md),
[0004](0004-agent-connectivity.md)).

**Language:** Go. Static binaries for macOS and Linux on amd64 and arm64,
one toolchain for hub, agent and CLI, strong gRPC and OTEL support.

**Concurrency principle:** one goroutine owns each kind of decision (a single
DB writer, a single scheduler loop); database unique constraints back those
owners up. There are no distributed locks. Transactions never span a network
call; GitHub and agent round-trips happen outside transactions and their
results are written afterwards.

**Observability:** OTEL traces and metrics from hub and agents from the first
commit (scheduler decisions, dispatch latency, agent connection state,
usage-probe parse failures, GitHub sync lag).

**Future integrations** are optional and behind interfaces: Postgres (for more
than one hub), Eshu (to detect issues that touch the same code), notifications
(ntfy, Pushover, Slack), Tailscale (private remote access).

## Repository layout

```
cmd/fleetd  cmd/fleet-agent  cmd/fleet
api/fleet/v1/*.proto                  # the contract
internal/hub/{gateway,store,scheduler,githubsync,verifier,triage,web}
internal/agent/{tmux,goalfile,probe,preflight,cmdlog}
internal/harness/{claude,codex,muse}  # launch, resume, usage parsers + fixtures
hooks/                                # generic goal protocol per harness
web/                                  # UI source; build output embedded in fleetd
docs/adr/
```

## Consequences

- Install is "download one binary per role and run it".
- One hub instance only. High availability would need Postgres and leader
  election; not planned.
- Anything needing a graph query (code-area conflicts) goes through an
  optional integration, not the core store.
