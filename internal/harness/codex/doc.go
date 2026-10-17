// Package codex reads Codex screens: the /status card with its weekly
// limit, shown as a percent left (01 D2), and the class of one slash
// command (01b C4, unknown is deny). It never runs the harness and
// never opens a credential file; it parses text it is given.
package codex
