// Package claude reads Claude Code screens: the /usage dialog with its
// session, weekly and per-model limits (01 D2), and the weekly-limit
// banner out of panes the agent already observes (01 D3). It never runs
// the harness and never opens a credential file; it parses text it is
// given.
package claude
