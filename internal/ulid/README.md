# internal/ulid

The hub's id minter: a lowercase Crockford ULID of 26 characters, the format
[ids.proto](../../api/fleet/type/v1/ids.proto) gives every hub-assigned id.

```go
id, err := ulid.New(now, rand.Reader) // "01arz3ndek041061050r3gg28a"
```

- 48 bits of Unix milliseconds, then 80 bits from the random source the
  caller passes (`crypto/rand` in production).
- Lowercase Crockford base 32 (`0-9a-z` without `i`, `l`, `o`, `u`), so an
  id is safe as a tmux name, a file name and a certificate subject.
- Ids from later milliseconds sort later as strings; within one millisecond
  the order is random (no monotonic counter).
- A time before 1970 or past the 48-bit range, or a random source that runs
  dry, is an error.
- Standard library only; no new dependency (ADR 0020).

Tests: `CGO_ENABLED=0 go test ./internal/ulid/`. They check the ULID spec's
example time and hand-worked entropy vectors, the ids.proto pattern, and the
time ordering.
