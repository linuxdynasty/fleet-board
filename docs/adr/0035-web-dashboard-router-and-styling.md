# 0035. Web dashboard router and styling without new dependencies

- Status: Proposed
- Date: 2026-10-24
- Decider: fleet-issue-driver (row 14, #42; Codex review on PR 405)
- Amends: [0020](0020-dependencies.md) (adds Router and Styling rows; ADR 0020 itself is not edited)

## Context

Row 14 builds the web dashboard (`web/`) with React, Vite and the
Connect client already recorded in ADR 0020. Two UI choices still need a
recorded home: how views are addressed, and how they are styled. The
first version of the row-14 slice wrote these two rows directly into
ADR 0020's decision table; ADR 0020 is Accepted, and accepted records
stay unchanged, so this later record carries the choices instead (the
precedent is ADR 0029, which amends 0020 the same way).

## Decision

1. Router: none. The dashboard uses a small hand-written hash router
   (`#/...` links, unknown names fall back to Machines). The views need
   no nested routes, so this adds no dependency.
2. Styling: plain CSS with CSS Modules (`.module.css`), no framework.
   Vite supports modules with no new package; phone width comes first.

## Consequences

- ADR 0020's table is unchanged; its index relations name this
  amendment, as they do for 0021 and 0029.
- If a view later needs nested routes or shared styling, that change
  gets its own ADR; this record does not approve a router package.
