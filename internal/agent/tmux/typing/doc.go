// Package typing builds every tmux send-keys line the agent types into a
// pane (plan row 7, #140; 01f F3). It is the only place such a line is
// made, so the rules that keep a line to exactly what its caller meant live
// in one small package.
//
// A [Line] is one send-keys command. Its fields are unexported, so code
// outside the package gets a Line that types something only from a New…
// constructor: [NewLiteral] for text, typed with `-l --` inside single
// quotes so tmux reads no key name, flag, variable, format or second command
// in it, and [NewKeys] for keys from the closed [Key] set. Each constructor
// takes the pane's whole fleet.type.v1.PaneKey and a stated [Purpose] (the
// row-7 ruling, fork 11), and refuses with a [RefusedError] (counted and
// logged by its [Recorder]) a missing purpose, a PaneKey without a known
// server, a start id or a "%<n>" pane id, empty input, text over 4 KiB
// (02e), a newline or carriage return, any other control character, or text
// that is not UTF-8. The Line keeps the server and start id, so the client
// can refuse a line built for another server or an earlier start of it (a
// restarted server reuses pane ids). A refused constructor returns the zero Line; [Line.IsZero]
// reports it and [Line.Command] refuses it with [ErrZeroLine], so a Line
// nobody built types nothing.
//
// Lint rules in .golangci.yml let only internal/agent/control, start and
// probe import this package and call a New… function, and the tmux client
// package import it to name the Line type. A test fails if the package
// exports a function not named New…, a variable or constant of type Line, or
// a method that returns a Line.
//
// The package checks what a line is, not whether the pane may take it now:
// the gate each purpose names (a prompt still showing, an empty input line)
// is the caller's (02h). [Recorder.Sent] logs typing.sent for a line the
// client sent.
package typing
