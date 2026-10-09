# internal/agent/conn

The agent's link to the hub. So far: enrollment
([02g A1](../../../docs/specs/02g-agent-security.md),
[03 W1](../../../docs/specs/03-workflows.md#w1-enrollment-and-first-connect)),
run by `fleet-agent enroll --hub <host[:port]> --token <token>`.

```go
res, err := conn.Enroll(ctx, conn.Options{Dir: dir, Hub: "hub.local", Token: tok, Version: v, Telemetry: tel})
id, err := conn.LoadIdentity(dir)  // ErrNotEnrolled when there is none
addr, err := conn.LoadHubAddress(dir)
```

`Enroll`, in order, stopping at the first failure:

1. **Nothing leaves before the local checks.** The token must parse
   (`ErrTokenMalformed`), a hub address must be given (`ErrNoHub`: discovery
   comes later in plan row 4), and the state folder must be private.
2. **Never replace an identity.** An enrolled folder is refused
   (`ErrAlreadyEnrolled`, naming the machine) before any network call; the
   identity file is also written with a hard link, which cannot overwrite.
3. **Same key on a retry.** The Ed25519 key is kept in `enroll.key` (0600)
   until Enroll succeeds, and `install_id` comes from `agent.db`, so running
   enroll again after a lost reply gets the hub's stored answer
   (`Result.Replayed`), not a second machine.
4. **Pinned TLS, no client certificate.** The handshake passes only if the
   hub's chain holds the CA whose SHA-256 is in the token and its certificate
   chains to that CA for the address's host. Otherwise it fails before the
   request is written (`ErrHubUntrusted`): an impostor gets neither the token
   nor the CSR. No system roots and no proxy are used.
5. **Check, then save.** The response must carry the pinned CA and
   fingerprint, one client certificate for this key that chains to the CA and
   names the machine id (common name and `fleet://machine/<id>`), a fleet id
   and a salt (`ErrBadResponse` otherwise; nothing saved, key kept). A hub
   refusal is a `*RefusedError` with the `ErrorDetail` reason.
6. The hub address goes to `hub-address`, the identity to `identity.json`
   (temp file, fsync, link, folder fsync), then `enroll.key` is removed.

**State folder** (04 Q9): `~/.local/state/fleet-agent/` unless
`--state-dir`; created 0700, and a folder or file with any group or other
bit, or another owner, is refused (`ErrLooseMode`), never re-moded.

| File | Holds |
|---|---|
| `identity.json` | machine id and name, fleet id, CA PEM and fingerprint, client certificate and its expiry, key, account salt and version, the hub's configured addresses |
| `hub-address` | last good `host:port` |
| `enroll.key` | the key, only between a first try and success |
| `agent.db` | `install_id` and the rest ([ring](../ring/README.md)) |

**Signals.** Span `agent.enroll`. Logs: `enroll.completed{machine_id,
replayed}`, `enroll.refused{reason, peer, retryable}`,
`enroll.hub_untrusted{peer}`, `enroll.failed{step, err_kind}` with `step`
one of `token`, `hub_address`, `state`, `already_enrolled`, `key`, `call`,
`response`, `save`, `cleanup`. No line holds the token, the key, the salt or
the facts; `Token` and `Identity` print redacted in every format.

Tests run against the real hub listener (`conntest`, on a real `state.db`):
enroll end to end with modes and a certificate that chains to the CA; a
token naming another CA refused with zero requests at the hub; four
impostor chains that receive no byte; expired and burned tokens; a dropped
reply recovered as a replay with the same certificate; an enrolled folder
left alone; no hub and a bad token sending nothing; a loose folder; and ten
tampered responses against a fake hub, each saving nothing.
