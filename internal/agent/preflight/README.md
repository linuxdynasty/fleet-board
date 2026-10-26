# Preflight (I1)

Agent-side read-only preflight for row 21 (`StartGoal` step 1; specs
[02h](../../../docs/specs/02h-goal-binding-and-pane-signals.md) and
[03c](../../../docs/specs/03c-goals.md)): five named checks, each
failure returned as its reason, nothing typed on any path.

## Layout

| File | Holds |
|---|---|
| `preflight.go` | `Checker`, `Facts`, the `Reader`/`Typer` seams, `Check` |
| `handler.go` | The `exec.Handler` binding (`Kind` `Preflight`, capability `preflight`) |

## Seams

`Config` carries what this lane does not own:

- `Reader Facts(ctx, spec)`: one read-only snapshot (checkout table,
  forge push access, the disk sample). Production
  (`cmd/fleet-agent/startgoalwire.go`) reads the checkout table, asks
  gh for push access, and samples the state volume; sign-in stays
  unknown (no agent-side source before a harness screen is read) and
  folder trust is false pre-pane (nothing open to prompt). Tests
  inject fakes.
- `Typer`: the only key-typing path the checker can see. `Check` never
  calls it; every test asserts the call count stays zero, which is the
  proof no refusal path types a key.

## Behavior

`Check` reports every failed check (not just the first) in fixed order —
sign-in, checkout, push access, folder trust, disk — with the retryable
bit from `command_goal.proto` (`DISK_LOW` retryable, the four durable
facts not). Unknown push-access or disk readings are skipped, never
failed. `DISK_LOW` fires under 5 GiB free (`defaults.md`, ADR 0024).
`would_reuse` and `goal_line_preview` stay unset until later row-21
slices land.
