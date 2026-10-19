# 0034. Native policy supersession references

- Status: Accepted
- Date: 2026-10-19
- Decider: linuxdynasty (the direction recorded in ADR 0033; reference clarification only)
- Supersedes, in part: [0022](0022-drift-audit-decisions.md) addendum A.1,
  only its terminal-only execution rule and blanket headless ban
- Amends: [0033](0033-native-sessions-and-handoff.md), its supersession references only

## Context

ADR 0022 addendum A.1 says every harness runs interactively in tmux and
forbids headless execution. ADR 0033 records the owner's later direction:
qualified native interfaces are possible, with execution disabled until
qualification. Its references name ADRs 0024 and 0027 but omit the same
restriction in ADR 0022.

ADR 0033 has been accepted. Accepted records stay unchanged; this later
record supplies the missing relationship and the index mirrors it.

## Options

Record the missing relationship in a new ADR and link both affected records
from the index.

## Decision

1. ADR 0033's qualified-native policy replaces ADR 0022 addendum A.1's
   terminal-only requirement and blanket headless ban. This record makes
   that limited supersession explicit.
2. ADR 0022's restrictions on credentials, root privileges and sandboxing
   remain in force. No account or credential access is added.
3. All ADR 0033 qualification, local-policy, ownership, handoff and consent
   boundaries remain in force. This clarification enables no native runtime
   or provider capability.

## Consequences

The index points readers of ADR 0022 to this clarification and then ADR 0033.
The accepted text of both earlier records is preserved. Provider permission,
version, authentication, billing, approval and deployment proof still gate
native execution.
