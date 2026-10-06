# internal/hub/config

Read the root `AGENTS.md` first; it applies here. Load `fleet-go` and
`fleet-security`. Rules for this package:

- 02j is the source of every key and default. A new key, a new default or a
  new refusal changes 02j (and defaults.md for a value) in the same PR, and
  `TestEveryDefaultMatches02j` with a value written out by hand.
- Strict always: never turn off `KnownFields`. A key whose feature is not
  built is in one of the two lists of 02k ("Out-of-release behaviour"): refused
  with `NotBuilt`, or loaded and checked with no effect until the plan row
  named there (`discovery.mdns`, `rate_limits.*`, `timing.*`, `github`). Never
  a third, unlisted way; moving a key between the lists changes 02k.
- The loopback rule of 02f is exact (`127.0.0.1` or `::1`); never add an
  override flag or accept a name.
- Never open, read or log a secret file named in the config (`github` key
  files). Shape checks only.
- Library: `go.yaml.in/yaml/v3` (ADR 0020).
- A change here updates this README and `doc.go` in the same commit.
