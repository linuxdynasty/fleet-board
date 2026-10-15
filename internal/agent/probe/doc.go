// Package probe runs the agent's usage-probe sessions (plan row 8, issue
// #36): one idle harness session per assigned account on the fleet-probe
// tmux server, in the fleet-owned probe folder (01 C3).
//
// A pass reads every screen before any key: a trust screen reports
// FOLDER_UNTRUSTED on the command and STARTUP_PROMPT_SEEN with kind
// trust on health, update and release-notes screens report
// STARTUP_PROMPT_SEEN, and only a proven idle prompt with an empty input
// line is typed into, with the one status command (/usage, or /status on
// Codex) and nothing else. Every pass emits ProbeHealth; a UsageReading
// goes out only when a usage screen was read.
//
// Concurrency: one goroutine owns every probe decision (ADR 0001). The
// hub's commands arrive through the executor on command goroutines; the
// handlers below only forward a request and wait for its reply. Run's
// loop is the single owner of the probe table: requests, due probes and
// passes all run there, one at a time, so two passes never overlap and no
// lock guards the table.
//
//	hub --Command--> exec.Executor --Run--> Handler --request--> Manager.Run loop
//	                                                                 |-- Backend (tmux CLI one-shots, internal/proc)
//	                                                                 |-- Classify (pure, no I/O)
//	                                                                 +-- Emitter.Emit (ProbeHealth, UsageReading)
//
// No transaction spans a network call: the package holds no database;
// Emit is one ring submit per event. The only shared state is the probe
// table, touched solely by the manager goroutine; tests pin the clock
// and fake the backend, the emitter and PATH lookup.
package probe
