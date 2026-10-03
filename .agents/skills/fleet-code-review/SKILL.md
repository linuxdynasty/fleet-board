---
name: fleet-code-review
description: "Use when reviewing a fleet-board diff before push, PR update or merge: review passes, P0-P3 severities, proof checks, and the READY or BLOCKED verdict. Not for resolving GitHub review threads."
---

# fleet-board code review

Review the final work product against its requirements and evidence. Author
confidence, memory of earlier rounds and file type do not establish safety.
Process wording (AGENTS.md, skills, ADRs) can authorize a mistake as easily as
code, so review it as carefully. Load the project skills matching the diff
(`fleet-go`, `fleet-contract-rigor`, `fleet-harness-adapter`, `fleet-tmux`,
`fleet-sqlite-store`, `fleet-telemetry-coverage`, `fleet-frontend`,
`fleet-security`).

## Workflow

1. **Bind the review**: base and head SHAs of the rebased final diff, branch,
   issue and acceptance criteria, changed files, proof actually run, open
   findings. Record `no PR exists yet` before first PR creation. A reviewer
   works read-only until the verdict is delivered and never edits the diff; if
   no separate reviewer is available say so and name the limitation.
2. **Run two axes in separate contexts.** The coordinator dispatches them as two
   reviewer agents in parallel (leaf agents dispatch no one); a lone reviewer
   runs spec first, then standards, and names that limitation. The **spec** axis
   reads only the diff and the issue (its scope and acceptance test, and the
   plan row it names) and reports: what the issue asked for that is missing or
   partial; behavior the diff adds that nobody asked for (scope creep); what
   looks done but is done wrong. With no issue (a docs-only change), it reads
   the diff against the purpose the PR body states. The **standards** axis runs
   the five passes below. A dispatched axis returns its findings only; the
   coordinator writes the one verdict, listing each axis under its own heading
   so a clean axis never hides a failing one.
   Record a result for each pass, grouping clean passes and giving an excluded
   surface a one-line reason.
3. **Check proof**: the claims in the PR title and body are each backed by a
   command that ran after the last edit, with its exit code. Confirm the
   regression test fails without the fix when you can do so read-only (check the
   commit order or the test's assertion), and that filtered tests ran.
4. **Deliver the verdict** with the schema below.

## The five passes

- **Pass 0: scope and diff integrity.** Base and head are the final rebased
  diff; target is not `main`; only intended files changed; no unrelated
  reversion, formatter drift or generated churn; files under 500 lines; no
  `CLAUDE.md` anywhere (root `AGENTS.md` is the only canon); `.claude/skills`
  and `.codex/skills` links exist for every skill.
- **Pass 1: correctness and contract.** Trace the change through its consumers
  and the owning spec (`docs/specs`, ADRs). Contract changes follow
  `fleet-contract-rigor` (buf build and lint ran; unfrozen vs additive-only
  phase stated) and the spec is updated in the same diff. Parsers never guess.
- **Pass 2: performance and storage.** Does it change a write rate, a batch,
  a transaction scope, a query, a buffer or a fan-out? If yes, the measurement
  against the 03g and 04 budgets is cited (`fleet-performance-rigor`).
- **Pass 3: reliability, concurrency, security, workflow.** One goroutine owns
  each decision; the single writer is not bypassed; no transaction spans a
  network call; idempotency, duplicates, restart mid-command, never-dropped
  kinds, unique-constraint backstops; credentials never read or logged;
  untrusted text capped; scopes checked; destructive prompts default Deny;
  telemetry for every new path including failures and the 3 AM question;
  commit and PR hygiene (named branch, no closing keywords unless intended).
- **Pass 4: hostile read.** What claim could this make too early? What proof is
  deferred though in scope? Which test passes while the production path is
  broken (a copied implementation, a mock of the thing under test)? Which
  generated file or spec table drifts unnoticed? What does an operator not see
  at 3 AM? What could a rebase or force-push change between review and merge?
  Name any smell as a labelled judgement call with the hunk quoted, P3 unless
  it breaks a documented rule: mysterious name, duplicated code, feature envy,
  data clumps, primitive obsession, shotgun surgery, speculative generality,
  middle man. A documented repo rule wins over a smell; skip what `gofumpt` or
  `golangci-lint` enforces.

## Findings

Every finding has: pass (`spec` for the spec axis), class (`correctness`,
`performance`, `concurrency`, `security`, `contract`, `docs`, `workflow`),
severity, confidence, disposition, `file:line` evidence, the violated rule, and
the fix plus the check that closes it.

| Severity | Meaning | Effect |
|---|---|---|
| P0 | Data or goal loss or duplication, credential exposure, security hole, deadlock, broken `main` | blocks everything |
| P1 | Wrong result, missing idempotency or ordering, silent failure, false-green test, missing signal on a runtime path, unmeasured hot-path change, required proof not run | blocks push |
| P2 | Edge case, doc or spec drift, missing coverage, minor performance or naming issue | fix inline by default; blocks when it contradicts a PR claim or is cheap in the same edit; otherwise tracked in a linked issue with the owner's agreement quoted in the PR |
| P3 | Cosmetic | never blocks; fix a line inline, do not open an issue |

A spec finding of missing or wrong is at least P1; scope creep is P2 unless it
changes behavior nobody reviewed. Severity follows consequence, not file type:
prose that an agent follows (AGENTS.md, a skill, a runbook) or a spec that
contradicts the code is not P3. Disposition is `fixed`,
`not-a-bug-with-evidence`, `deferred-to-linked-follow-up` or `blocked`. A
finding restated in a later round is the same finding; cite the earlier round.

## Verdict

```text
VERDICT: READY | BLOCKED
Base..Head: <sha>..<sha>   Branch: <name>   PR: <number | none yet>
Spec: <result: missing | creep | wrong | clean>
Passes: 0 <result> | 1 <result> | 2 <result> | 3 <result> | 4 <result>
Findings: P0=<n> P1=<n> P2-blocking=<n> P2-deferred=<n> P3=<n>
Proof run (command, exit code, postdates last edit?): ...
Not checked: ...
Stale when: base, head, tree, PR body or CI state changes
```

`BLOCKED` when base or head is not the final diff, any P0, P1 or blocking P2
remains, a claim has no postdated proof, or a review comment on the latest head
is unresolved. Never manufacture findings to fill the template, and never infer
a pass from a command you could not run: ask the coordinator for its output.
