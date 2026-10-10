// Package statusfile writes the two small files `fleet-agent doctor` reads
// while the agent is down or beside it: status.json, the running agent's
// last-known state, and enroll.json, the code of the last failed enroll
// (plan row 6, 03 W1 "Persistence").
//
// [Start] returns a [Writer] that keeps status.json current: it writes at
// start, at most once a second after [Writer.Notify], at least once a
// minute (the defaults row "Agent status file rewrite"), and once more in
// [Writer.Stop], which the caller runs after the connection loop's Run
// returned so the last write carries the end the loop stopped on. Each
// write is a temp file, fsync, rename and folder fsync, so a kill leaves
// the previous file whole. [RecordEnroll] does the same for enroll.json
// and removes it after a good enroll. [Read] and [ReadEnroll] are for
// doctor: they ignore keys they do not know, refuse another layout version,
// and clean every string again. A value never set is left out of the file
// (never written as 0), and a reader says unknown.
//
// The files hold closed words, times, numbers and hub addresses. Never a
// token, an error text, a message or a path.
package statusfile
