# internal/hostname

The rule for comparing hostnames at enroll
([03 W1](../../docs/specs/03-workflows.md#w1-enrollment-and-first-connect)),
in one place that `fleetd` and `fleet-agent` both import.

```go
if hostname.Mismatch(expected, reported) { /* flag the machine, warn the owner */ }
```

- Both names are compared lower case, without a trailing `.` and then a
  trailing `.local`, so `Laptop.local.` matches `laptop`.
- An empty reported hostname (or one that is only `.` or `.local`) is a
  mismatch.
- The caller decides what an empty expected name means: the hub compares
  with the machine name, and the agent checks only when the hub sent one.

Tests: `CGO_ENABLED=0 go test ./internal/hostname/`.
