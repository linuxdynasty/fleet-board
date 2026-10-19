# internal/fleetconfig

Read the root `AGENTS.md` first. This package owns the offline `FleetConfig`
YAML mapping used by the future CLI. Read [02m](../../docs/specs/02m-hub-api-actions-and-audit.md#config-export-and-import)
and [defaults](../../docs/specs/defaults.md#offline-fleetconfig-yaml) before changing it.

- Keep `Encode` and `Decode` pure. Test both through their public functions.
- Use protobuf JSON mapping for field values. Keep the YAML parser bounded and
  return safe errors that do not include document scalar values.
- Reject every present hub ID wrapper until the #48 portable-reference contract
  and hub translation are implemented and tested together.
- Update this package's README and `doc.go` with a behavior change. Keep every
  file below 500 lines.
