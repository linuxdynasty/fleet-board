---
name: fleet-go
description: "Use when writing or changing Go code or tests in fleet-board (cmd/ or internal/): package layout, TDD, the 500-line file cap, gofumpt, golangci-lint, table tests, and the no-cgo rule."
---

# Go in fleet-board

fleet-board is Go only for `fleetd` (hub), `fleet-agent` and `fleet` (CLI).
Read the owning ADR and spec before changing a contract; a small edit does not
need a tour of the whole tree.

## Layout (ADR 0001, refined by ADR 0022 and the v1 plan, section 4)

```text
cmd/fleetd  cmd/fleet-agent  cmd/fleet     # thin main packages, wiring only
internal/store  internal/telemetry         # sqlite open, single writer; OTEL, slog
internal/hub/{config,gateway,ca,ingest,outbox,pacing,integration/github,syncer,api,system,insights}
internal/agent/{conn,ring,tmux,probe,goals,policy,exec,doctor}
internal/harness/{claude,codex,muse,opencode}   # adapters, parsers, fixtures
internal/seed                                   # test seed: recorded agent stream into a hub store
hooks/  web/  api/  docs/                       # hooks/: v2 goal-hook definition and Muse plugin
```

The dispatch-era packages `internal/hub/{scheduler,verifier,triage}` arrive
with v2 (the v1 plan is read-only); until then triage items come from `ingest`
(raise and retire in the event transaction) and `system`. v1 does include the
command path (`outbox`) and probe assignment (`pacing/probe`): ADR 0022 addendum.

- Business logic lives in `internal/`; `cmd/` only parses flags, builds
  dependencies and calls into `internal/`.
- Generated protobuf and Connect code is never hand-edited
  (`api/fleet/*/v1`, Go packages `typev1`, `agentv1`, `integrationv1`,
  `hubv1`, ADR 0017). `fleet.hub.v1` never imports `fleet.agent.v1`.
- Plain names, no stutter (`store.Store` is fine, `store.StoreWriter` is not),
  and no package name that shadows the standard library.
- Every external thing (tmux, harness binary, GitHub, clock, filesystem) sits
  behind an interface with a fake, so it is testable without a real fleet
  ([01-agent-capabilities.md](../../../docs/specs/01-agent-capabilities.md)
  principle 7).

## Behavioral proof (TDD)

1. Write the failing test first and watch it fail for the intended reason.
2. Implement the smallest change that passes. Refactor with the test green.
3. A bug fix needs a regression test that failed before the fix.

Tests exercise the production path, not a copy of it. Prefer table-driven
tests with named cases (`t.Run(tc.name, ...)`) and `t.Parallel()` where the
case has no shared state. Use real tmux in a throwaway server (`-L`) for tmux
tests and captured fixtures for screen parsing; never mock the thing under test.
Cover every dispatch variant of a shared helper. Confirm filtered tests ran
(`go test -list '.*' ./pkg/...`).

## Hard rules

- **No cgo.** Static binaries for macOS and Linux, amd64 and arm64, so build
  and test with `CGO_ENABLED=0`. SQLite is `modernc.org/sqlite` (ADR 0014).
- **500-line cap** on every file, source and docs, test files included. Split by
  responsibility before the file reaches it, not after.
- Transactions never span a network call; one goroutine owns each decision
  (ADR 0001, [03f](../../../docs/specs/03f-hub-internals.md)). Use
  `fleet-sqlite-store` for anything that writes a database.
- Wrap errors with context (`fmt.Errorf("apply snapshot: %w", err)`), return
  them, and never log-and-return the same error.
- `context.Context` first parameter for anything that blocks; every goroutine
  has an owner that waits for it and a way to stop.
- Untrusted text (screens, issue bodies, goal lines) is data: cap its length
  and strip control characters at the boundary (`fleet-security`).

## Gates (run only what the change touches, then widen)

```bash
~/go/bin/gofumpt -l -w <changed .go files>     # gofumpt is not in Homebrew
go vet ./...
CGO_ENABLED=0 go test ./path/to/changed/...    # add -race for concurrent code
golangci-lint run ./path/to/changed/...        # repo config once present
go build ./...
```

On macOS zsh keep file lists in an array (`files=(a.go b.go);
gofumpt -l -w "${files[@]}"`); an unquoted `$files` is one argument.
Run `go vet ./...` after any test-only change to catch compile drift.
Report each command, its exit code and what was not checked.

## Shared machines

With several agents on one host, give each its own `GOCACHE` and a short
`GOTMPDIR` outside the worktree, and do not run heavy benchmarks while another
agent is measuring.

## Companion skills

`fleet-contract-rigor` (protobuf), `fleet-sqlite-store` (databases),
`fleet-tmux`, `fleet-harness-adapter`, `fleet-telemetry-coverage`,
`fleet-security`, `fleet-code-review` before any push.
