# internal/hub/ca

Read the root `AGENTS.md` first; it applies here. Load `fleet-go` and
`fleet-security`. Rules for this package:

- Ed25519 only, for the CA, the server and (row 3) agent certificates. A key
  type, lifetime or renewal change updates 02g and defaults.md in the same PR.
- Never log, print or return a key in an error message. Keys leave this
  package only as PKCS #8 DER for `statedb`.
- An enroll token's text leaves `Token` only through `Reveal`; keep the
  redacting `String`, `GoString` and `LogValue`. A token format change
  updates 02g in the same PR.
- No I/O, no clock and no global state: callers pass `now` and the random
  source, so tests set both.
- Tests verify with `crypto/x509` against the CA and compare with values
  worked out by hand (lifetimes, the day-60 boundary, SAN lists).
- A change here updates this README and `doc.go` in the same commit.
