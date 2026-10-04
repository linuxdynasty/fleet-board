# internal/buildinfo

The version every fleet-board binary reports, and the small `Run` that the
three scaffold binaries (`cmd/fleetd`, `cmd/fleet-agent`, `cmd/fleet`) share.

- `Version` is `dev` unless the linker sets it:
  `go build -ldflags "-X github.com/linuxdynasty/fleet-board/internal/buildinfo.Version=v0.1.0" ./cmd/fleetd`
- `Current(name)` returns the binary's identity; `Program.Run(args, stdout, stderr)`
  prints `<name> <version>` (no arguments or `--version`), the usage (`-h`,
  `--help`), or refuses an unknown argument with exit code 2.

Tests: `CGO_ENABLED=0 go test ./internal/buildinfo/`. `TestLinkerSetsVersion`
builds the real binaries with `-ldflags -X` and runs them; `-short` skips it.
