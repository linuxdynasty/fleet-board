// Package buildinfo is the version each fleet-board binary reports.
//
// Release builds set Version at link time:
//
//	go build -ldflags "-X github.com/linuxdynasty/fleet-board/internal/buildinfo.Version=v0.1.0" ./cmd/fleetd
//
// An ordinary build reports "dev". The three binaries (fleetd, fleet-agent,
// fleet) are wiring only: each calls [Current] and [Program.Run].
package buildinfo
