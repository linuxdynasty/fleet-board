// Package claude reads Claude Code screens: the /usage dialog with its
// session, weekly and per-model limits (01 D2), the weekly-limit banner
// out of panes the agent already observes (01 D3), and the class of one
// slash command (01b C4, unknown is deny). It never runs the harness and
// never opens a credential file; it parses text it is given.
package claude
