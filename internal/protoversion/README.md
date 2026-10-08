# internal/protoversion

The agent wire's proto version range, in one place that `fleetd` and
`fleet-agent` both import ([02l](../../docs/specs/02l-versioning-errors-and-decisions.md#versioning-and-capability-negotiation)).

```go
v, ok := protoversion.Negotiate(req.GetProtoMin(), req.GetProtoMax())
if !ok { /* ENROLL_PROTO_UNSUPPORTED, or Bye{PROTO_UNSUPPORTED} on Connect */ }
```

- `Min` and `Max` are both 1: the v1 wire is proto version 1, and 0 means
  unset.
- `Negotiate` refuses an unset bound (0), a range whose min is above its max,
  and a range that misses `[Min, Max]`; otherwise it returns the highest
  version both sides speak.
- The hub's `Enroll` uses it today (plan row 3); `Hello` and the agent use it
  from rows 4 and 5.

Tests: `CGO_ENABLED=0 go test ./internal/protoversion/`.
