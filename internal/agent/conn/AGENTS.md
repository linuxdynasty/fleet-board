# internal/agent/conn

Read the root `AGENTS.md` first; it applies here. Load `fleet-go`,
`fleet-security` and `fleet-telemetry-coverage`. Rules for this package:

- Nothing goes on the network before the local checks pass, and the token
  is sent only after the TLS handshake verified the hub against the CA the
  token pins. Never add TOFU, system roots or a skip of that check. A
  handshake the peer fails (the pin, the name, the key proof, or a non-TLS
  answer) is `ErrHubUntrusted` and never retried. A connection that drops,
  resets or times out during the handshake is a lost connection, retried
  within "Enroll tries"; `transportError` sorts the two by error shape.
- A retry resends the same token, key and `install_id`, within the bounds in
  defaults.md; only a non-retryable reason is a refusal.
- Never log or print the token, the key, the salt or the facts. `Token` and
  `Identity` stay redacted in `%v`, `%+v`, `%#v` and slog; keep it that way.
- Every file in the state folder is written 0600 through `writePrivate`
  (temp, fsync, rename or link, folder fsync). The identity is written with
  a link and is never replaced or rewritten, renewal included: a renewed
  pair goes to `cert.pem` (replace), and `renew.key` is written before
  `RenewCert` is sent and removed only after `cert.pem` is saved and the
  pair swapped. Never delete an invalid `cert.pem` at start; log it and fall
  back. A renewal replaces it. Never replace a `renew.key` the agent cannot
  use; log the file, what is wrong and the fix at each check.
- An mDNS answer is untrusted data: it only proposes a candidate, which
  passes the same pinned-CA handshake as any address, named by its SRV
  target. Enroll sends nothing but that handshake to an answer until it
  passes, and nothing at all when none does. Never trust a TXT record or a
  name in place of the pin. Read a library answer only after the query
  that produced it returned (the library writes it until then), and never
  dial an answer that points at this machine.
- `hub-addresses` is written through `writePrivate` (replace) only when a
  HelloAck's non-empty list differs from the list in use; a damaged one is
  never repaired, only ignored for the identity's list.
- Never read or touch a harness's files or credentials from here.
- Text from the hub is trusted only after the pin, and still capped and
  stripped of control characters before it is saved or printed.
- A change here updates this README, `doc.go` and 03 W1 and 03n W2 (and 02g if
  the enroll or renewal steps change, 02i if seq or epoch handling does) in
  the same commit.
- The loop ends on its own only for the four `End` values; anything else
  is a reconnect with backoff. Adding an end needs a spec change (03 W1).
  Revoked, unknown machine and fleet mismatch are written to `ended.json`
  (through `writePrivate`, replace) before `Run` returns, and `CheckEnded`
  refuses a run for that machine before anything touches the network;
  never move that check after a dial, and never record `cert_expired`.
- One goroutine sends on a stream (Run's); the receiver only hands
  messages over. The heartbeat is a ticker case in `serve`'s select, never
  a goroutine of its own, so nothing lands between a snapshot and its
  results. A snapshot belongs to its stream and is never carried to
  the next. No agent.db write waits on the network.
- A renewal never ends the loop, never sends on the stream, and never
  outlives its session: the session cancels and waits for it before the
  connection closes. The client certificate is read at each handshake
  (`GetClientCertificate` on the atomic pair); do not set
  `tls.Config.Certificates` or a session cache on the link.
- Tests use `conntest` (the real hub listener), the fake hub in
  `fakehub_test.go`, or `connecthub_test.go` for Connect; a new refusal
  needs a test that proves nothing was sent or saved.
