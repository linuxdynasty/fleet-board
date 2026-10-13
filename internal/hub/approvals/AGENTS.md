# internal/hub/approvals

Read the root `AGENTS.md` first; it applies here. Load `fleet-go`,
`fleet-sqlite-store`, `fleet-security` and `fleet-telemetry-coverage`. Rules
for this package:

- Every write runs inside the caller's writer job (`*sql.Tx`); the package
  never opens the store itself. The decision and its command commit together
  or not at all (03f).
- A transaction never spans a network call: `Decide` records the decision,
  the API layer enqueues in the same transaction, the agent answers later.
- Nothing is ever sent for a denied, expired or withdrawn approval; a deny
  sends nothing by construction (`Sends` is false).
- One answer in flight per prompt per machine, backed by the
  `approval_inflight` unique constraint; the constraint error is the refusal,
  never a pre-check.
- Audit every state change in the same transaction (`approval.decide`,
  `approval.expire`, `approval.withdraw`); new audit names go into 02m in the
  same change.
- No prompt text, preview or decision key in metrics or logs: ids and counts
  only (`fleet-security`, `fleet-telemetry-coverage`).
- Tests run the real SQLite file in `t.TempDir()` through `store.Write`
  and `store.Read`, with a fake clock (`t0`); never mock the store.
