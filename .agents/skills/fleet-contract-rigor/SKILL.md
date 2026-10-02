---
name: fleet-contract-rigor
description: "Use when a .proto under api/fleet/ changes or code depends on a contract name: buf build and lint, naming conventions, the unfrozen-until-v0.1 rule, then additive-only changes."
---

# Contract rigor

The contract is `api/fleet/{type,agent,integration,hub}/v1/*.proto`, checked
with buf (module `api`, lint `STANDARD`, breaking `WIRE_JSON`; `buf.yaml`).
Names are part of every gRPC path and every Connect JSON client, so a rename
is a breaking change for browsers and the CLI even when the binary wire is
unchanged ([ADR 0017](../../../docs/adr/0017-contract-packages.md)).

## Every contract change

```bash
go run github.com/bufbuild/buf/cmd/buf@v1.73.0 build
go run github.com/bufbuild/buf/cmd/buf@v1.73.0 lint
```

Both must pass before commit. Pin the version in the command; `@latest` makes
results depend on the day.

## Where a message lives (ADR 0017, [02-contracts.md](../../../docs/specs/02-contracts.md#package-layout))

- `fleet.type.v1`: only types used by two or more protocols (counting the hub
  API). It imports no other fleet package.
- `fleet.agent.v1`: the agent wire (`AgentService`). `fleet.integration.v1`:
  provider interface and work model. `fleet.hub.v1`: UI, CLI and phone API.
- `fleet.hub.v1` never imports `fleet.agent.v1`; the UI never sees agent wire
  types. Moving a type between packages later changes its full name, so count
  the hub API's needs before placing it.

## Naming and shape

- Services end in `Service`. Every RPC has its own `<Rpc>Request` and
  `<Rpc>Response`; stream bodies that already have a name wrap it as one field.
- Paging: `page_size`, `page_token`, `next_page_token`.
- Hub resources ([02b-hub-api.md](../../../docs/specs/02b-hub-api.md#conventions)):
  field 1 id, 2 `version`, 3 `create_time`, 4 `update_time`, resource fields
  from 10. Updates take a required `update_mask`; empty is `INVALID_ARGUMENT`.
  Mutations take `request_id`; actions that depend on state take
  `expected_version`.
- Agent field numbers are grouped with gaps (commands 10-19 probes, 20-39 tmux,
  40-49 goals, 50-59 attach, 60-69 logs, 70-79 self-management) so additions
  land in their group.
- A percent that can be unknown uses the explicit-unknown `Percent`, never 0.
- Untrusted text fields are marked in the proto comment and capped (see the
  size limits table in 02-contracts.md).

## Frozen or not

- **Until v0.1** the contracts are unfrozen (ADR 0019): renames and retypes are
  allowed, and `buf build` plus `buf lint` must pass on every change. Do not
  add `reserved` lines for history; git keeps it.
- **From the `v0.1` tag** ADR 0003's additive-only rule applies again: add
  fields and messages, never renumber, retype, rename or remove, and run
  `buf breaking --against '.git#tag=v0.1'` (or the latest tag) in CI. A break
  needs a new `v2` package and a superseding ADR.
- Whichever phase applies, say which one in the PR.

## Wire and behavior rules a change must keep

- Delivery classes ([02-contracts.md](../../../docs/specs/02-contracts.md#delivery-semantics)):
  adding an event kind means choosing shed, coalesce or never-dropped, and
  adding its counter (`fleet-telemetry-coverage`).
- New agent behavior needs a `Capability{name, version}` and a `requires` on
  its command, so an old agent answers `UNSUPPORTED` instead of misreading it.
- Commands stay typed. No kind carries a shell string, a tmux command string
  or a credential (`fleet-security`). `exec.raw` is reserved and never
  advertised.
- Error reasons use the tables in 02-contracts.md and 02b; add a reason, do not
  reuse one with a new meaning.

## Spec and generated code

Update the spec table that lists the message (02-contracts.md, 02b, or
`ui-contract-map.md` for UI-visible changes) in the same commit; a message with
no capability behind it is removed. Generated Go and TypeScript are
regenerated, never edited. A UI change that needs a new field starts here, not
in the client.

## Report

State the phase (unfrozen or additive-only), the two buf commands with exit
codes, and which spec rows changed.
