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
internal/hub/{config,lock,statedb,daemon,gateway,ca,ingest,outbox,pacing,integration/github,syncer,api,system,insights}
internal/hub/{scheduler,goals,verifier,approvals,routines,logstore,attach}   # dispatch rows
internal/agent/{conn,ring,tmux,probe,goals,policy,exec,doctor}
internal/agent/{control,start,worktree,hooks,logship,attach}                # dispatch rows
internal/harness/{claude,codex,muse,opencode}   # adapters, parsers, fixtures
internal/seed                                   # test seed: recorded agent stream into a hub store
hooks/  web/  api/  docs/                       # hooks/: goal-hook definition and Muse plugin (v1)
```

The dispatch packages (the lines marked "dispatch rows") are v1 too (ADR 0025):
they arrive with plan rows 18 to 29, after the watching rows. There is no
separate triage package: triage items are raised and retired by `ingest` (in the
event transaction), `system`, and the dispatch packages that own each source.
The command path (`outbox`) and probe assignment (`pacing/probe`) come with the
watching rows: ADR 0022 addendum.

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

1. **Name the seams first.** A seam is the package's public interface, where a
   test observes behavior without reaching inside. List the seams under test in
   the handoff before writing any test; a deep package (much behavior behind a
   small interface) needs few seams.
2. **One slice at a time:** one failing test at a seam, watched failing for the
   intended reason, then the smallest change that passes, then the next test.
   Writing every test first tests imagined behavior. Refactor after green, one
   step at a time, with the tests green after each step.
3. A bug fix needs a regression test that failed before the fix.

Tests exercise the production path, not a copy of it. A test that breaks when
the code is refactored without a behavior change is coupled to the
implementation (a private function, an internal collaborator, or a side channel
such as reading rows a store's interface should return); test through the
interface instead. An assertion that recomputes the expected value the way the
code does proves nothing; assert a value worked out by hand or from the spec.
Tests of the schema, pragmas, constraints and migrations assert on the database
file itself (`fleet-sqlite-store`). Prefer table-driven tests with named cases
(`t.Run(tc.name, ...)`) and `t.Parallel()` where the case has no shared state.
Use real tmux in a throwaway server (`-L`) for tmux tests and captured fixtures
for screen parsing; never mock the thing under test. Cover every dispatch
variant of a shared helper. Confirm filtered tests ran (`go test -list '.*'
./pkg/...`).

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
make fmt                                       # the pinned gofumpt on every Go file; make fmt-check only checks
go vet ./...
CGO_ENABLED=0 go test ./path/to/changed/...    # add -race for concurrent code; make test does both
make lint                                      # the pinned golangci-lint on changed packages, not the one on PATH
go build ./...
```

On macOS zsh keep any file list in an array (`files=(a.go b.go);
cmd "${files[@]}"`); an unquoted `$files` is one argument.
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
