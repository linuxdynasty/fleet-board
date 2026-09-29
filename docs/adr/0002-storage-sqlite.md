# 0002. Storage: embedded SQLite

- Status: Accepted
- Date: 2026-09-29
- Deciders: linuxdynasty

## Context

The hub stores machines, accounts, usage readings, goals, sessions, work items
mirrored from GitHub, commands (outbox), triage items and the audit log. Write
volume is a few events per second. Users should not have to run a database
server ([0001](0001-system-architecture.md)).

## Options

1. SQLite, embedded, one file.
2. Postgres, external or embedded.
3. Both from day one.

## Decision

SQLite, embedded, behind a `store` interface so Postgres can be added later
without touching callers.

- **Driver:** a pure-Go SQLite driver (no cgo), so `fleetd` cross-compiles to
  a static binary for every target. Confirm the driver choice and its WAL and
  busy-timeout behavior in a spike before committing to it.
- **Mode:** WAL journal, `busy_timeout` set, foreign keys on.
- **Writers:** exactly one writer goroutine owns all writes; readers use a
  separate connection pool and never block the writer under WAL. This removes
  `SQLITE_BUSY` from the write path instead of retrying around it.
- **Integrity backstops:** unique constraints where the scheduler's
  single-owner rule could ever be bypassed, e.g. one active goal per
  `(repo, issue)`.
- **Transactions:** short; no network calls inside a transaction.
- **Migrations:** numbered SQL files embedded in the binary, applied at start,
  forward-only.
- **Backup:** online backup to a file on a schedule and via `fleet backup`.
- **Retention:** raw usage-screen captures and pane captures are pruned by age;
  the audit log is kept.

## Consequences

- Zero-dependency install and trivial backup.
- One hub process; a second hub against the same file is refused (lock file).
- Adding Postgres later means implementing the `store` interface and running
  the same migrations in a Postgres dialect.

## Open questions

- Pure-Go driver performance and WAL behavior to be confirmed by the spike.
