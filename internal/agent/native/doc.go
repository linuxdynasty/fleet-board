// Package native is the agent's machine-side runtime for fleet-managed
// native sessions (plan #266; ADR 0033; docs/specs/02n-native-execution.md).
// It sits behind the executor: five typed commands (N1) arrive through the
// existing command path (outbox, envelope checks, command log), and this
// package's handlers admit one journal effect per command, claim it in
// per-session order, call exactly one qualified Adapter outside every store
// transaction, then resolve the effect with bounded evidence.
//
// The journal (internal/agent/ring) is the only shared state: PREPARED
// grants no call authority, MAY_HAVE_RUN may have run, CONFIRMED and
// NO_EFFECT are terminal, RECONCILE_REQUIRED holds the session. A duplicate
// command_id returns the first outcome (the executor replays the logged
// result); a retry after NO_EFFECT needs fresh effect and command ids. No
// shell string, executable path or credential crosses this package: the hub
// sends references, the host joins them to local policy and allowed roots,
// and only a qualified Adapter ever touches a harness process, through
// internal/proc with its measured caller.
//
// Until a backend passes ADR 0033 qualification the adapter set is empty:
// every native command is then refused STATUS_UNSUPPORTED with its
// per-backend capability, and nothing is advertised in Hello. Unknown SDK
// events are recorded as identity only and never end a turn, complete a
// goal or move control state; secrets are scrubbed before storage or
// telemetry.
package native
