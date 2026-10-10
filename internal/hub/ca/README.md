# internal/hub/ca

The fleet CA and the certificates it signs
([02g](../../../docs/specs/02g-agent-security.md#hub-identity-and-restore)).

```go
auth, err := ca.New(now, rand.Reader)                    // Ed25519 CA, 10 years
sc, err := auth.IssueServer(names, now, rand.Reader)     // Ed25519 server cert, 90 days
names := ca.DefaultNames(hostname)                      // host, host.local, localhost, 127.0.0.1, ::1
target := ca.ServiceTarget(hostname)                    // fleetd-host.local, the mDNS SRV target ("" if hostname is no DNS name)
names = ca.AgentNames(hostname, configured)             // configured (or the defaults) plus target, deduplicated
if ca.NeedsRenewal(leaf, now) || !ca.SameNames(leaf, names) { /* re-issue */ }
tok, err := ca.NewToken(caDER, rand.Reader)              // fbe1_ enroll token; tok.SecretHash is what state.db keeps
tok, err := ca.ParseToken(req.GetToken())                // ErrTokenMalformed, or CAFingerprint + SecretHash
csr, err := ca.ParseCSR(req.GetCsrPem())                 // ErrCSRInvalid, or the Ed25519 key + KeyHash
ac, err := auth.IssueAgent(machineID, csr.Key, now, rand.Reader) // client cert, 90 days
serial := ca.SerialHex(ac.Serial)                        // lowercase hex, as cert_serials keeps it
```

- **Enroll tokens** (02g, plan row 2b): `NewToken` returns `fbe1_` plus the
  unpadded base64url of 65 bytes: `TokenVersion` (1), SHA-256 of the CA
  certificate DER, and 32 secret bytes from the random source. `Token`
  carries `SecretHash` (SHA-256 of the 32 secret bytes) and `CAFingerprint`;
  the text comes out only through `Reveal`, and `String`, `GoString` and
  `LogValue` print `fbe1_[redacted]`. Lifetimes: `DefaultTokenTTL` 15 min,
  `MaxTokenTTL` 24 h (defaults.md); the caller checks them. `ParseToken`
  takes the text apart again (exact length, the prefix, strict unpadded
  base64url, version 1) and keeps only the CA fingerprint and the secret's
  hash; anything else is `ErrTokenMalformed`, whose text never echoes the
  input.
- **Enroll CSRs** (02g, plan row 3): `ParseCSR` allows at most 4 KiB
  (`MaxCSRBytes`), exactly one `CERTIFICATE REQUEST` PEM block with only
  whitespace after it, a valid self-signature, an Ed25519 key, an empty
  subject and no DNS, IP, URI or email SANs; other requested extensions are
  ignored. `KeyHash` is SHA-256 of the SubjectPublicKeyInfo DER. Every
  failure is `ErrCSRInvalid`.
- **Agent certificates** (`IssueAgent`): the CSR's key, CN = machine id, URI
  SAN `fleet://machine/<id>` (`MachineURI`), client auth only, digital
  signature, not a CA, a random 128-bit serial, `AgentLifetime` (90 days)
  from 5 minutes before now and never past the CA. `SerialHex` is the
  serial as `cert_serials` and `revoked-serials` keep it.

- **Keys** are Ed25519 for the CA, the server and agents; keys travel as PKCS #8 DER,
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
`slog` never print the secret or its hash. CSR tests build each forbidden
case with `crypto/x509` (another key type, a subject, each SAN kind, a broken
signature, two blocks, trailing text); agent certificate tests verify as a
client certificate of the CA, fail as a server certificate, and check the
lifetime and the clamp to the CA by hand.
