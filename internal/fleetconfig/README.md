# FleetConfig YAML codec

`Encode(*hubv1.FleetConfig)` and `Decode([]byte)` are pure offline functions for
the future `fleet export` and `fleet import` commands. They render protobuf JSON
field values as YAML. The root `format_version` is first; other mapping keys
are sorted. Lists keep their order, 64-bit integers stay quoted, and optional
false or zero values stay present. The parser accepts protobuf field names and
JSON aliases, with one spelling per field.

Trailing comments are allowed after the single document.
The parser accepts one mapping document and rejects unknown fields, duplicate
fields, unsupported YAML tags, anchors, aliases, and YAML-specific number
forms. The [limits](../../docs/specs/defaults.md#offline-fleetconfig-yaml)
bound input and output bytes before or after rendering, and check depth and
node count after YAML parsing. Errors name a category and,
when available, a known schema path without copying scalar values from the
document.

`FleetConfig` still embeds hub ID wrappers in some specs. This codec rejects
any present wrapper, even `{}`, on both paths. #48 must define portable
references and translate them at the hub before ID-bearing exports can use
this codec. The package has no CLI, network, store, or credential access.

Tests: `CGO_ENABLED=0 go test -shuffle=on -count=3 ./internal/fleetconfig`.
