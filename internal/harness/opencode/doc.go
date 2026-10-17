// Package opencode reads OpenCode screens: the `stats` spend table, the
// `providers list` account output and the TUI model line (01 D2, C2), and
// the class of one slash command (01b C4, unknown is deny). It never runs
// the harness and never opens a credential file; it parses text it is
// given. OpenCode shows no usage window and no /goal (01b).
package opencode
