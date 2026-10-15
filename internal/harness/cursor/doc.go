// Package cursor reads cursor-agent screens: the /usage dialog with its
// monthly plan buckets (01b D2, 03b "Monthly windows"), the ready prompt
// with the trust box and the input line, the account identity from
// `status` and `about` (01b C2), and the class of one slash command (01b
// C4, unknown is deny). It never runs the harness and never opens
// a credential file; it parses text it is given.
package cursor
