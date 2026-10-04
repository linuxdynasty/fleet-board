# internal/buildinfo

Read the root `AGENTS.md` first; it applies here. Rules for this package:

- Keep it tiny: the version and the shared `Run` for version-printing binaries.
  Real command behavior goes in its own package under `internal/`.
- Standard library only, no cgo, no package-level state besides `Version`
  (the linker's `-X` needs a string variable).
- Test through `Program.Run` and `Current`; the link-time test builds the real
  binaries, so a rename of the package path or of `Version` fails it.
- A change here updates this README and `doc.go` in the same commit.
