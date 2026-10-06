# internal/hub/ca

The fleet CA and the certificates it signs
([02g](../../../docs/specs/02g-agent-security.md#hub-identity-and-restore)).

```go
auth, err := ca.New(now, rand.Reader)                    // Ed25519 CA, 10 years
sc, err := auth.IssueServer(names, now, rand.Reader)     // Ed25519 server cert, 90 days
names := ca.DefaultNames(hostname)                      // host, host.local, localhost, 127.0.0.1, ::1
if ca.NeedsRenewal(leaf, now) || !ca.SameNames(leaf, names) { /* re-issue */ }
tok, err := ca.NewToken(caDER, rand.Reader)              // fbe1_ enroll token; tok.SecretHash is what state.db keeps
```

- **Enroll tokens** (02g, plan row 2b): `NewToken` returns `fbe1_` plus the
  unpadded base64url of 65 bytes: `TokenVersion` (1), SHA-256 of the CA
  certificate DER, and 32 secret bytes from the random source. `Token`
  carries `SecretHash` (SHA-256 of the 32 secret bytes) and `CAFingerprint`;
  the text comes out only through `Reveal`, and `String`, `GoString` and
  `LogValue` print `fbe1_[redacted]`. Lifetimes: `DefaultTokenTTL` 15 min,
  `MaxTokenTTL` 24 h (defaults.md); the caller checks them.

- **Keys** are Ed25519 for the CA and the server; keys travel as PKCS #8 DER,
  certificates as DER. Every certificate starts 5 minutes before "now" so a
  peer whose clock is a little behind accepts it.
- **Lifetimes** (defaults.md): the CA 10 years (`CAYears`), the server
  certificate 90 days (`ServerLifetime`), capped at the CA's own expiry and
  re-issued at two thirds (day 60).
- **Default names**: the hostname lower-cased without a trailing `.local`,
  then `<host>.local`, `localhost`, `127.0.0.1`, `::1`, without duplicates. IP
  addresses go to the IP SANs and the rest to the DNS SANs.
- No I/O and no logging: `statedb` stores the result, `gateway` serves it.
  Nothing here prints a key.

Tests: `CGO_ENABLED=0 go test ./internal/hub/ca/`. They verify issued
certificates with `crypto/x509` against the CA for every name, and check the
two-thirds boundary to the second. Token tests decode the text by hand and
compare each part with SHA-256 computed in the test, and check that `fmt` and
`slog` never print the secret or its hash.
