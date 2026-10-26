// Package spool tails the hook spool (E8): hook-events.jsonl lines the
// harness hooks append (internal/agent/hooks/hook.py owns the write
// format), parses each to a goalregistry.Call, and resolves it through
// Registry.TurnEnd, which learns harness sessions (E9) and emits
// HarnessHookEvent (both NEVER_DROP). Silent lines stay silent: a line
// that names no live goal, or names the wrong goal, emits nothing and
// never guesses a match.
//
// The tailer keeps its byte offset in a sidecar file next to the spool
// and fsyncs it after every line, so a restart replays at most the one
// line whose emit raced the crash. Session mappings replay idempotently
// (an already-learned session emits nothing); a replayed turn end may
// re-emit its event, which the hub's event log keeps as a duplicate.
package spool
