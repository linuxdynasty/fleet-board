# internal/hub/secret

The local approval secret of [ADR 0032](../../docs/adr/0032-local-trusted-approval-secret.md):
in `local_trusted` mode any process on the hub machine counts as the owner,
so `DecideApproval` and the `SETTINGS`/`ADMIN` calls need this secret as
well and fail without it, even on loopback.

- `Ensure` creates `<state dir>/approval_secret` (32 random bytes, mode
  0600) once; `fleetd init` shows it once at the terminal. The first start
  of an older store creates it the same way and prints it once to stderr.
- `Check` compares a presented hex value in constant time; `Reveal` reads
  it back for that one showing. Neither ever logs it.
- `CheckMode` fails closed on a file others could read.

The dashboard keeps the secret in the login session, never in the shell or
a file; `fleet` prompts for it at the terminal. The exact call list is in
[02d](../../docs/specs/02d-hub-authz.md); the check itself runs in
`internal/hub/api` before the scoped call.

Tests: `CGO_ENABLED=0 go test ./internal/hub/secret/`.
