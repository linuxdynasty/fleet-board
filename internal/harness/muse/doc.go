// Package muse reads Muse screens: the /usage box with its
// current-window and weekly limits plus the session block (01 D2),
// and the class of one slash command (01b C4, unknown is deny). It
// never runs the harness and never opens a credential file; it parses
// text it is given.
package muse
