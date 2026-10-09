# internal/agent/conn/conntest

Read the root `AGENTS.md` first; it applies here. Load `fleet-go`. Rules for
this package:

- Test code only: import it from `_test.go` files, never from product code.
- Use the hub's real packages (`gateway`, `statedb`, `ca`, `revocation`);
  do not copy their logic here. A helper that reads the store says which
  table it reads.
- Listen on `127.0.0.1:0` only and stop everything in `t.Cleanup`.
- A change here updates this README and `doc.go` in the same commit.
