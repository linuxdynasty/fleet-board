# internal/hub/setupgithub

Read the root `AGENTS.md` first; it applies here. Rules for this package:

- No listener, no stdout, no prompt: the browser half lives in
  `cmd/fleetd`. Everything here is observable without a browser.
- Key material is never logged, never echoed and never inside an error:
  errors name the step, never the secret, the code or the path.
- Every secret file is mode 0600, created exclusively (`O_EXCL`); the
  config rewrite is atomic (temp file plus rename) and keeps 0600.
- GitHub answers are recorded `httptest` fixtures; no test touches the
  live API. Account logins are untrusted text: capped and
  control-stripped at the boundary.
- A change here updates this README and `doc.go` in the same commit.
