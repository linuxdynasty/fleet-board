// Package policy is L1 of the agent (01f, 03e): the local policy file
// policy.yaml in the config folder, which is the machine's ceiling (ADR 0009).
// The owner edits it by hand; the hub never writes it and a change takes
// effect at the next restart.
//
// [Parse] reads the bytes strictly (unknown keys, duplicate keys, wrong types,
// values outside their range and a second document are a [Problem]); the
// reason is built from a line number, a key name and a fixed phrase, never
// from the file's text. A key with no value or an absent key is no local
// opinion (a nil pointer or list in [Policy]); an explicit empty list is an
// opinion. [Load] reads the file once and returns a [Manager]: a missing file
// is a normal first run, an invalid or unreadable one is safe mode. The
// Manager reports PolicyInfo ([Manager.Info], [Manager.Snapshot]), re-hashes the
// file on [Manager.Recheck] and on a directory watch ([Manager.Watch], fsnotify,
// a 500 ms settle after the last event, no polling), and hands each hash change
// to [Options.OnChange] (the seam row 7's policy_changed event reads) after it
// counts it, logs it and writes the local audit row.
//
// [WriteStarter] writes the starter file `fleet-agent enroll` offers, never
// over an existing one. [DefaultDir], [ResolveDir] and [EnsureDir] name and
// make the config folder.
//
// The tighten-only merge with the hub's settings is internal/agent/settings;
// the command executor reads [Manager.SafeMode] through it.
package policy
