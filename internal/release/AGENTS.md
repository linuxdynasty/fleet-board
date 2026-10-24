# internal/release

Read the root `AGENTS.md` first; it applies here. Rules for this package:

- Security-sensitive: a checksum mismatch installs nothing, ever. Every new
  path (fetch, verify, swap, restart) needs a test before it lands.
- No credentials: release reads are public. Never add a token, key, or auth
  file read here.
- No `os/exec`: the restart runs through `internal/proc` (caller `service`).
  New callers are measured and written into the defaults row first.
- Archive names are data, never paths: join only the exact selected name,
  and refuse anything with a folder, an absolute path, or `..`.
- A change here updates this README and `doc.go` in the same commit.
