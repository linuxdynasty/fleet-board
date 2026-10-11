# internal/agent/doctor

Read the root `AGENTS.md` first; it applies here. Load `fleet-go`,
`fleet-security` and `fleet-telemetry-coverage`. Rules for this package:

- Doctor is read-only. A check never writes, creates, removes or locks
  anything, and never calls a function that does: not `policy.Load`
  (metrics, log, audit row), `conn.CheckEnded` (it removes another machine's
  file), `statusfile.RecordEnroll`, `conn.EnsureDir`, `policy.EnsureDir`.
  Use `policy.Inspect`, `statusfile.Read`, `statusfile.ReadEnroll` and `lstat`.
- Never read a credential. `identity.json` is decoded into a struct that has no
  key or salt field; `cert.pem` is parsed only to its first CERTIFICATE block;
  `renew.key` and `enroll.key` are only looked at (mode). Never open `agent.log`,
  `audit.log` or `agent.db`; a test fails if their names appear as strings.
- Every string printed goes through `clean` in `Run`, whatever the check
  returned. A check never prints a name found in a folder, an OS error text or a
  child's output: use fixed sentences and the closed words of status.json.
- Start a process only through `Options.TmuxVersion` (the measured proc caller
  `tmux`). No `os/exec`, no import of `internal/agent/ring`, `internal/agent/tmux`
  or `internal/store`; `TestDoctorImportsNoProcessNoRingAndNoTmuxClient` holds it.
- Every check returns a reason. A value never set is `unknown`, never 0 or ok;
  a check this build cannot run is `NotCheckedYet(..., row)`. A silent omission
  is a bug.
- Judge a status.json fact only when the file is fresh (`StaleAfter`, 2
  minutes: the writer rewrites at least once a minute). The end the loop stopped
  on, the clock measurement and the last mDNS browse are the exceptions: they
  stay true when old.
- No telemetry: doctor prints to the person who ran it
  (`No-Observability-Change`, the proc call is already counted by `internal/proc`).
- A new check updates the table in README.md, the count in the tests and 02k.
