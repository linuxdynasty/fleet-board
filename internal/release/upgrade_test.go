package release_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"runtime"
	"testing"

	"github.com/linuxdynasty/fleet-board/internal/release"
)

// Upgrade fetches the next release from a release endpoint, verifies it and
// swaps the binary. Every path below runs against fakes: no network, no
// installed service, no database.

type fakeFetch struct {
	rel     release.ReleaseInfo
	relErr  error
	files   map[string][]byte
	dlErr   map[string]error
	gotRepo string
	gotVers string
}

func (f *fakeFetch) Release(_ context.Context, repo, version string) (release.ReleaseInfo, error) {
	f.gotRepo, f.gotVers = repo, version
	if f.relErr != nil {
		return release.ReleaseInfo{}, f.relErr
	}
	return f.rel, nil
}

func (f *fakeFetch) Download(_ context.Context, url string) ([]byte, error) {
	if err, ok := f.dlErr[url]; ok {
		return nil, err
	}
	b, ok := f.files[url]
	if !ok {
		return nil, errors.New("release: download: server answered 404")
	}
	return b, nil
}

type fakeSwap struct {
	called  bool
	program string
	archive []byte
	err     error
}

func (s *fakeSwap) Swap(archive []byte, program string) error {
	s.called, s.program, s.archive = true, program, archive
	return s.err
}

type fakeRestart struct {
	called  bool
	program string
	err     error
}

func (r *fakeRestart) Restart(program string) error {
	r.called, r.program = true, program
	return r.err
}

// rig builds an upgrade with a release holding a good archive and its sums.
func rig(archive []byte) (*fakeFetch, *fakeSwap, *fakeRestart) {
	sum := sha256.Sum256(archive)
	sums := []byte(fmt.Sprintf("%x  %s\n", sum, release.AssetName("fleetd", "linux", "amd64")))
	f := &fakeFetch{
		rel: release.ReleaseInfo{Version: "v0.2.0", Assets: []release.Asset{
			{Name: release.AssetName("fleetd", "linux", "amd64"), URL: "https://x/archive"},
			{Name: release.ChecksumName, URL: "https://x/sums"},
		}},
		files: map[string][]byte{"https://x/archive": archive, "https://x/sums": sums},
	}
	return f, &fakeSwap{}, &fakeRestart{}
}

func upgradeOpts(f *fakeFetch, s *fakeSwap, r *fakeRestart) release.UpgradeOptions {
	return release.UpgradeOptions{
		Program: "fleetd",
		Repo:    "o/r",
		Current: "v0.1.0",
		GOOS:    "linux",
		GOARCH:  "amd64",
		Fetch:   f,
		Swap:    s,
		Restart: r,
	}
}

func TestUpgradeToLatest(t *testing.T) {
	t.Parallel()
	archive := []byte("new-binary-bytes")
	f, s, r := rig(archive)
	if err := release.Upgrade(t.Context(), upgradeOpts(f, s, r)); err != nil {
		t.Fatalf("Upgrade: %v", err)
	}
	if f.gotVers != "" {
		t.Errorf("fetched version %q, want latest", f.gotVers)
	}
	if !s.called || s.program != "fleetd" || string(s.archive) != string(archive) {
		t.Errorf("swap = (%v, %q, %q), want the verified archive for fleetd", s.called, s.program, s.archive)
	}
	if !r.called || r.program != "fleetd" {
		t.Errorf("restart = (%v, %q), want fleetd", r.called, r.program)
	}
}

func TestUpgradeNamedVersion(t *testing.T) {
	t.Parallel()
	f, s, r := rig([]byte("new-binary-bytes"))
	opts := upgradeOpts(f, s, r)
	opts.Want = "v0.2.0"
	if err := release.Upgrade(t.Context(), opts); err != nil {
		t.Fatalf("Upgrade: %v", err)
	}
	if f.gotVers != "v0.2.0" {
		t.Errorf("fetched version %q, want v0.2.0", f.gotVers)
	}
}

func TestUpgradeRefusals(t *testing.T) {
	t.Parallel()
	archive := []byte("new-binary-bytes")
	cases := []struct {
		name  string
		setup func(f *fakeFetch, s *fakeSwap, r *fakeRestart, o *release.UpgradeOptions)
	}{
		{name: "a tampered archive swaps and restarts nothing", setup: func(f *fakeFetch, _ *fakeSwap, _ *fakeRestart, _ *release.UpgradeOptions) {
			f.files["https://x/archive"] = []byte("tampered-bytes")
		}},
		{name: "already current installs nothing", setup: func(_ *fakeFetch, _ *fakeSwap, _ *fakeRestart, o *release.UpgradeOptions) {
			o.Current = "v0.2.0"
		}},
		{name: "a downgrade is refused before any install", setup: func(_ *fakeFetch, _ *fakeSwap, _ *fakeRestart, o *release.UpgradeOptions) {
			o.Current = "v0.3.0"
		}},
		{name: "a bad current version is refused", setup: func(_ *fakeFetch, _ *fakeSwap, _ *fakeRestart, o *release.UpgradeOptions) {
			o.Current = "last-tuesday"
		}},
		{name: "an unknown program is refused", setup: func(_ *fakeFetch, _ *fakeSwap, _ *fakeRestart, o *release.UpgradeOptions) {
			o.Program = "fleet"
		}},
		{name: "a release without checksums is refused", setup: func(f *fakeFetch, _ *fakeSwap, _ *fakeRestart, _ *release.UpgradeOptions) {
			f.rel.Assets = f.rel.Assets[:1]
		}},
		{name: "a release without the program archive is refused", setup: func(f *fakeFetch, _ *fakeSwap, _ *fakeRestart, _ *release.UpgradeOptions) {
			f.rel.Assets = []release.Asset{{Name: release.ChecksumName, URL: "https://x/sums"}}
		}},
		{name: "a missing tag is refused", setup: func(f *fakeFetch, _ *fakeSwap, _ *fakeRestart, o *release.UpgradeOptions) {
			o.Want = "v9.9.9"
			f.relErr = errors.New("release: list releases: server answered 404 Not Found")
		}},
		{name: "a failed swap restarts nothing", setup: func(_ *fakeFetch, s *fakeSwap, _ *fakeRestart, _ *release.UpgradeOptions) {
			s.err = errors.New("read-only folder")
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f, s, r := rig(archive)
			opts := upgradeOpts(f, s, r)
			tc.setup(f, s, r, &opts)
			if err := release.Upgrade(t.Context(), opts); err == nil {
				t.Fatal("Upgrade succeeded, want an error")
			}
			if !s.called {
				if r.called {
					t.Error("restart ran without a swap")
				}
				return
			}
			// Only the failed-swap case reaches the swap; it must not restart.
			if tc.name == "a failed swap restarts nothing" && r.called {
				t.Error("restart ran after a failed swap")
			}
		})
	}
}

func TestUpgradeFromDevIsAllowed(t *testing.T) {
	t.Parallel()
	f, s, r := rig([]byte("new-binary-bytes"))
	opts := upgradeOpts(f, s, r)
	opts.Current = "dev"
	if err := release.Upgrade(t.Context(), opts); err != nil {
		t.Fatalf("Upgrade from dev: %v", err)
	}
	if !s.called {
		t.Error("no swap ran for a dev binary")
	}
}

func TestUpgradeFailedRestartIsReported(t *testing.T) {
	t.Parallel()
	f, s, r := rig([]byte("new-binary-bytes"))
	r.err = errors.New("no service manager")
	if err := release.Upgrade(t.Context(), upgradeOpts(f, s, r)); err == nil {
		t.Fatal("Upgrade succeeded with a failed restart, want an error")
	}
	if !s.called {
		t.Error("no swap ran before the restart failed")
	}
}

func TestUpgradeDefaultsToTheRunningPlatform(t *testing.T) {
	t.Parallel()
	archive := []byte("new-binary-bytes")
	sum := sha256.Sum256(archive)
	name := release.AssetName("fleet-agent", runtime.GOOS, runtime.GOARCH)
	f := &fakeFetch{
		rel: release.ReleaseInfo{Version: "v0.2.0", Assets: []release.Asset{
			{Name: name, URL: "https://x/archive"},
			{Name: release.ChecksumName, URL: "https://x/sums"},
		}},
		files: map[string][]byte{
			"https://x/archive": archive,
			"https://x/sums":    []byte(fmt.Sprintf("%x  %s\n", sum, name)),
		},
	}
	s, r := &fakeSwap{}, &fakeRestart{}
	opts := release.UpgradeOptions{Program: "fleet-agent", Repo: "o/r", Current: "v0.1.0", Fetch: f, Swap: s, Restart: r}
	if err := release.Upgrade(t.Context(), opts); err != nil {
		t.Fatalf("Upgrade: %v", err)
	}
	if s.program != "fleet-agent" {
		t.Errorf("swap program = %q, want fleet-agent", s.program)
	}
}
