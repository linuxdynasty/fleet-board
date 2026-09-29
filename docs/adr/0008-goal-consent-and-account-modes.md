# 0008. Configurable goal consent and interactive-only accounts

- Status: Accepted
- Date: 2026-09-29
- Deciders: linuxdynasty

## Context

Dispatched goals carry a `CONSENT:` line that says what the agent may do
without asking (see [../design.md](../design.md), goal protocol). The terms
research ([../research/terms-of-service.md](../research/terms-of-service.md))
found Anthropic the highest-risk provider for unattended subscription use, and
suggested a human merge gate and a way to keep an account out of automation.
The owner wants both to be configurable rather than fixed.

## Decision

**Consent is configurable per repo and per account.** The effective consent
for a goal is the intersection of the repo's allowed set and the account's
allowed set. Default for both: `push, pr-open, merge`.

```yaml
repos:
  - repo: eshu-hq/eshu
    consent: [push, pr-open, merge]
accounts:
  - name: claude-max
    harness: claude
    mode: fleet              # fleet | interactive-only
    consent: [push, pr-open] # no merge: Claude goals stop at an approved, green PR
  - name: chatgpt-pro
    harness: codex
    mode: fleet
    consent: [push, pr-open, merge]
```

Without `merge`, a goal's done state is "PR approved and green" and it waits in
Triage for a human merge; the worker is still freed only after GitHub shows the
merge.

**Account mode.**
- `fleet`: the scheduler may dispatch goals to this account.
- `interactive-only`: never dispatched. Usage is still read and shown, and
  sessions the owner creates by hand still appear, so pacing across accounts
  stays accurate.

## Consequences

- An owner can keep Claude out of automation entirely, or allow it without
  merge rights, without touching other harnesses.
- The consent actually written into each goal is recorded in the audit log.
