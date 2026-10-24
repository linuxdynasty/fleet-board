# 0020. Languages, frameworks and libraries

- Status: Accepted
- Date: 2026-10-02
- Deciders: linuxdynasty
- Builds on: [0001](0001-system-architecture.md) (Go, single binaries),
  [0003](0003-transport-grpc.md), [0007](0007-web-ui-react.md),
  [0014](0014-storage-confirmed.md), [0016](0016-browser-transport.md),
  [0019](0019-pre-implementation-contract-decisions.md) (sign-in)

## Context

Earlier ADRs fixed the languages and the main frameworks, but left smaller
library choices open: WebSocket, PTY, CLI, YAML, GitHub client, charts, test
tools. Development will be done mostly by AI agents; an open choice gets made
differently by each agent and each PR. This ADR closes them so every agent
uses the same set. Versions and maintenance status were checked against the
Go module proxy, the npm registry and the GitHub API on 2026-10-02.

## Decision

**Languages.** Go for `fleetd`, `fleet-agent` and `fleet` (one module, no
cgo, static binaries for darwin and linux, amd64 and arm64); TypeScript
(strict) for the dashboard; protobuf for the contracts. Python stays out of
the product; small repo tooling scripts may use it.

**Toolchain.** Go 1.27; Node 24 LTS to build the UI only (never needed to
run the hub); `buf` v1.73 for contracts; `golangci-lint` v2 and `gofumpt` for
Go; generated code via `buf generate` (`protoc-gen-go`, `protoc-gen-connect-go`,
`protoc-gen-es`).

### Go libraries

| Need | Library | Notes |
|---|---|---|
| Agent ↔ hub wire | `google.golang.org/grpc`, `google.golang.org/protobuf` | ADR 0003/0004 |
| Hub API for browser and CLI | `connectrpc.com/connect` | ADR 0016 |
| Store | `modernc.org/sqlite` | pure Go; ADR 0014 |
| Log compression | `github.com/klauspost/compress/zstd` | ADR 0015 |
| WebSocket (browser terminal) | `github.com/coder/websocket` | context-aware, `net/http` friendly; fallback `gorilla/websocket` |
| PTY (agent runs `tmux attach`) | `github.com/creack/pty` | pure Go, darwin and linux |
| CLI | `github.com/spf13/cobra` | subcommands for `fleet`, `fleetd`, `fleet-agent` |
| `fleet.yaml` | `go.yaml.in/yaml/v3` | `gopkg.in/yaml.v3` is archived; v4 is still a release candidate |
| Goal-file watching | `github.com/fsnotify/fsnotify` | |
| Passkeys | `github.com/go-webauthn/webauthn` | still v0: pin and read release notes before each upgrade |
| GitHub sign-in | `golang.org/x/oauth2` | ADR 0019 |
| GitHub App tokens | `github.com/bradleyfalzon/ghinstallation/v2` | mints installation tokens only |
| GitHub REST/GraphQL calls | a small typed wrapper over `net/http` in the GitHub adapter | `google/go-github` releases a new major every few weeks and `ghinstallation` pins an older one; the adapter needs few endpoints and must control ETags and rate-limit headers itself |
| Telemetry | `go.opentelemetry.io/otel` SDK (traces, metrics) | stable v1 |
| `/metrics` endpoint | `go.opentelemetry.io/otel/exporters/prometheus` | v0; pinned. OTLP export to a collector is optional |
| Logs | stdlib `log/slog` (JSON) | the OTel slog bridge is v0; optional, off by default |
| Tests | stdlib `testing` + `github.com/google/go-cmp` | no assertion framework |

### TypeScript packages

| Need | Package | Notes |
|---|---|---|
| UI | `react` 19, `react-dom` 19 | ADR 0007 |
| Language | `typescript` 7 | new major; fall back to 6.0.x if a build tool lags |
| Build | `vite` 8, `@vitejs/plugin-react` | |
| API client | `@connectrpc/connect`, `@connectrpc/connect-web`, `@bufbuild/protobuf` (same version as `protoc-gen-es`) | generated from `fleet.hub.v1` |
| Server state | `@tanstack/react-query` | caching, refetch on `RESYNC_REQUIRED` |
| Terminal | `@xterm/xterm`, `@xterm/addon-fit` | scoped packages, not the old `xterm` |
| Passkeys | `@simplewebauthn/browser` | new major; fall back to `navigator.credentials` |
| Charts | `uplot` (burn-down and time series) | small and fast; thin React wrapper |
| Tests | `vitest`, `@testing-library/react`, `@playwright/test` | `vitest` 5 is new; fall back to 4.x |

### Runtime prerequisites (the owner's own tools)

`tmux` 3.2 or newer, `git`, the forge CLI for each code host (`gh`,
`glab`), and the harness binaries. fleet-board never installs or upgrades
these (pass-through, ADR 0011).

## Rules for changing dependencies

- Pin exact versions (`go.mod`/`go.sum`, `package-lock.json`); upgrade in
  dedicated PRs, one ecosystem at a time, with the release notes linked.
- Libraries still on v0 or with a new major (go-webauthn, the Prometheus
  exporter, TypeScript 7, Vite 8, Vitest 5, simplewebauthn 14) are upgraded
  only after reading their migration notes.
- A new runtime dependency needs a line in this table or a superseding ADR;
  licenses must be permissive (MIT, BSD, Apache-2.0, ISC). Tools such as
  `golangci-lint` (GPL-3.0) are used only as binaries, never linked.
- No cgo, no runtime service dependencies (ADR 0001).

## Consequences

- Every agent and every PR uses the same libraries; reviews can reject an
  unlisted dependency.
- The GitHub adapter owns its HTTP calls, which makes conditional requests
  and rate-limit handling explicit (W4) at the cost of writing small types.
- Logs work without any collector; OTLP export is opt-in.
