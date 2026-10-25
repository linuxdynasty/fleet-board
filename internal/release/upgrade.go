package release

import (
	"context"
	"fmt"
	"runtime"
)

// Swapper replaces the running binary with verified archive bytes. It runs
// only after the checksum passes; a mismatch swaps nothing.
type Swapper interface {
	Swap(archive []byte, program string) error
}

// Restarter restarts the program's service after the swap, so the new
// binary (and its forward-only database migrations) takes effect.
type Restarter interface {
	Restart(program string) error
}

// UpgradeOptions are one `upgrade` run. Program is "fleetd" or
// "fleet-agent" (the CLI has no service); Want names a tag, or "" for the
// latest release. GOOS/GOARCH default to the running platform. Current is
// the linked version ("dev" for a local build, which always upgrades).
type UpgradeOptions struct {
	Program string
	Repo    string
	Current string
	Want    string
	GOOS    string
	GOARCH  string
	Fetch   Fetcher
	Swap    Swapper
	Restart Restarter
}

// Upgrade fetches the release, checks its checksum, swaps the binary and
// restarts the service. Anything before the swap fails leaves the install
// untouched; a failed restart is reported after the swap.
func Upgrade(ctx context.Context, o UpgradeOptions) error {
	if o.Program != ProgramFleetd && o.Program != ProgramAgent {
		return fmt.Errorf("release: no upgrade for %q: only fleetd and fleet-agent run as services", o.Program)
	}
	if o.Fetch == nil || o.Swap == nil || o.Restart == nil {
		return fmt.Errorf("release: upgrade needs a release source, an installer and a service restart")
	}
	goos, goarch := o.GOOS, o.GOARCH
	if goos == "" {
		goos = runtime.GOOS
	}
	if goarch == "" {
		goarch = runtime.GOARCH
	}
	repo := o.Repo
	if repo == "" {
		repo = DefaultRepo
	}
	rel, err := o.Fetch.Release(ctx, repo, o.Want)
	if err != nil {
		return err
	}
	if o.Current != "dev" && o.Current != "" {
		cmp, err := CompareVersions(rel.Version, o.Current)
		if err != nil {
			return err
		}
		switch {
		case cmp == 0:
			return fmt.Errorf("release: already at %s", o.Current)
		case cmp < 0:
			return fmt.Errorf("release: %s is older than %s: databases move forward only, so a downgrade is refused", rel.Version, o.Current)
		}
	}
	programURL, err := programAsset(rel, o.Program, goos, goarch)
	if err != nil {
		return err
	}
	archive, err := o.Fetch.Download(ctx, programURL)
	if err != nil {
		return err
	}
	sumsURL, err := sumsAsset(rel)
	if err != nil {
		return err
	}
	raw, err := o.Fetch.Download(ctx, sumsURL)
	if err != nil {
		return err
	}
	sums, err := ParseChecksums(raw)
	if err != nil {
		return err
	}
	want, ok := sums[AssetName(o.Program, goos, goarch)]
	if !ok {
		return fmt.Errorf("release: %s has no checksum for %s", rel.Version, AssetName(o.Program, goos, goarch))
	}
	if err := VerifyBytes(archive, want); err != nil {
		return err
	}
	if err := o.Swap.Swap(archive, o.Program); err != nil {
		return fmt.Errorf("release: install %s: %w", rel.Version, err)
	}
	if err := o.Restart.Restart(o.Program); err != nil {
		return fmt.Errorf("release: %s is installed but the service did not restart: %w", rel.Version, err)
	}
	return nil
}

func programAsset(rel ReleaseInfo, program, goos, goarch string) (string, error) {
	a, err := SelectAsset(rel.Assets, program, goos, goarch)
	if err != nil {
		return "", err
	}
	return a.URL, nil
}

func sumsAsset(rel ReleaseInfo) (string, error) {
	for _, a := range rel.Assets {
		if a.Name == ChecksumName {
			if a.URL == "" {
				return "", fmt.Errorf("release: %s has no download address", ChecksumName)
			}
			return a.URL, nil
		}
	}
	return "", fmt.Errorf("release: %s holds no %s", rel.Version, ChecksumName)
}
