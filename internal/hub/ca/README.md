# internal/hub/ca

The fleet CA and the certificates it signs
([02g](../../../docs/specs/02g-agent-security.md#hub-identity-and-restore)).

```go
auth, err := ca.New(now, rand.Reader)                    // Ed25519 CA, 10 years
sc, err := auth.IssueServer(names, now, rand.Reader)     // Ed25519 server cert, 90 days
names := ca.DefaultNames(hostname)                      // host, host.local, localhost, 127.0.0.1, ::1
if ca.NeedsRenewal(leaf, now) || !ca.SameNames(leaf, names) { /* re-issue */ }
```

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
two-thirds boundary to the second.
