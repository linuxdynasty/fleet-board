# internal/agent/conn

Read the root `AGENTS.md` first; it applies here. Load `fleet-go`,
`fleet-security` and `fleet-telemetry-coverage`. Rules for this package:

- Nothing goes on the network before the local checks pass, and the token
  is sent only after the TLS handshake verified the hub against the CA the
  token pins. Never add TOFU, system roots or a skip of that check. Any
  failed handshake is `ErrHubUntrusted`, never retried.
- A retry resends the same token, key and `install_id`, within the bounds in
  defaults.md; only a non-retryable reason is a refusal.
- Never log or print the token, the key, the salt or the facts. `Token` and
  `Identity` stay redacted in `%v`, `%+v`, `%#v` and slog; keep it that way.
- Every file in the state folder is written 0600 through `writePrivate`
  (temp, fsync, rename or link, folder fsync). The identity is written with
  a link and is never replaced.
- Never read or touch a harness's files or credentials from here.
- Text from the hub is trusted only after the pin, and still capped and
  stripped of control characters before it is saved or printed.
- A change here updates this README, `doc.go` and 03 W1 (and 02g if the
  enroll steps change) in the same commit.
- Tests use `conntest` (the real hub listener) or the fake hub in
  `fakehub_test.go`; a new refusal needs a test that proves nothing was sent
  or saved.
