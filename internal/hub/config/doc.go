// Package config loads and checks fleetd.yaml (docs/specs/02j-hub-runtime.md,
// "Configuration file").
//
// [Load] reads the file strictly: an unknown key or a second YAML document is
// refused. Keys not written keep the 02j defaults ([Default]); a key written
// with no value (empty, ~ or null) is refused, and so is a list item or key
// name with no value; a leading ~/ in state_dir
// becomes the home folder. Every refusal is a *[Error] naming the
// key. In local_trusted mode listen.api (and listen.metrics, when set) must
// be exactly 127.0.0.1 or ::1, with no override. Keys this build does not
// implement yet (mode authenticated, listen.pprof, the authenticated-mode TLS
// and allow-list keys, OTLP export) are refused as "not built yet";
// discovery.mdns, rate_limits, timing and github are loaded and checked but
// have no effect until the plan rows 02k names. state_dir is refused when <state_dir>/admin.sock
// would not fit in sun_path (104 bytes on darwin, 108 on Linux). The github
// section is checked for shape only; its key files are never opened here.
package config
