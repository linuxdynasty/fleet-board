// Package harness is what every harness adapter shares: the strict version
// check of 01 C1, the conformance test of plan row 8 and the reset-time
// helpers of the usage parsers.
//
// A version command runs through the one child-process helper (01 principle
// 8) and its first line must hold a dotted number, or there is no version:
// not_on_path, empty_version_output or invalid_version_output. See
// [Detector] and [ParseVersionOutput].
//
// Every adapter's test calls [Conform] over every fixture of every version
// it supports (row 30's Cursor adapter too). Conform refuses contradicting
// claims before release: a feature marked supported with no fixture of that
// version, a version marked SUPPORTED with any feature not supported, a
// fixture that neither parses nor cleanly yields unknown, and declared
// support that differs from the committed golden table.
//
// Reset texts have no year and are local (01 D2): [NextMonthDayTime] and
// [NextTimeOfDay] resolve one against the capture time in a stated zone,
// and an impossible day stays unknown rather than guessed.
package harness
