package release_test

import (
	"testing"

	"github.com/linuxdynasty/fleet-board/internal/release"
)

func TestAssetName(t *testing.T) {
	t.Parallel()
	cases := []struct{ program, goos, goarch, want string }{
		{"fleetd", "darwin", "arm64", "fleetd-darwin-arm64.tar.gz"},
		{"fleetd", "linux", "amd64", "fleetd-linux-amd64.tar.gz"},
		{"fleet-agent", "linux", "arm64", "fleet-agent-linux-arm64.tar.gz"},
		{"fleet", "darwin", "amd64", "fleet-darwin-amd64.tar.gz"},
	}
	for _, tc := range cases {
		t.Run(tc.want, func(t *testing.T) {
			t.Parallel()
			if got := release.AssetName(tc.program, tc.goos, tc.goarch); got != tc.want {
				t.Errorf("AssetName = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestSelectAsset(t *testing.T) {
	t.Parallel()
	assets := []release.Asset{
		{Name: "fleetd-darwin-arm64.tar.gz", URL: "https://example.com/a"},
		{Name: "fleetd-linux-amd64.tar.gz", URL: "https://example.com/b"},
		{Name: release.ChecksumName, URL: "https://example.com/sums"},
	}
	got, err := release.SelectAsset(assets, "fleetd", "linux", "amd64")
	if err != nil {
		t.Fatalf("SelectAsset: %v", err)
	}
	if got.URL != "https://example.com/b" {
		t.Errorf("SelectAsset URL = %q, want the linux archive", got.URL)
	}
	if _, err := release.SelectAsset(assets, "fleetd", "darwin", "amd64"); err == nil {
		t.Error("SelectAsset picked a missing platform, want an error")
	}
	if _, err := release.SelectAsset(assets, "fleet-agent", "linux", "amd64"); err == nil {
		t.Error("SelectAsset picked a missing program, want an error")
	}
	if _, err := release.SelectAsset([]release.Asset{{Name: "fleetd-linux-amd64.tar.gz"}}, "fleetd", "linux", "amd64"); err == nil {
		t.Error("SelectAsset accepted an asset with no URL, want an error")
	}
}

func TestCompareVersions(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		a, b    string
		want    int
		wantErr bool
	}{
		{name: "equal", a: "v0.1.0", b: "v0.1.0", want: 0},
		{name: "leading v is optional", a: "0.1.0", b: "v0.1.0", want: 0},
		{name: "newer patch", a: "v0.1.1", b: "v0.1.0", want: 1},
		{name: "older minor", a: "v0.1.0", b: "v0.2.0", want: -1},
		{name: "newer major", a: "v1.0.0", b: "v0.9.9", want: 1},
		{name: "dev is refused", a: "dev", b: "v0.1.0", wantErr: true},
		{name: "two parts refused", a: "v0.1", b: "v0.1.0", wantErr: true},
		{name: "words refused", a: "v0.1.x", b: "v0.1.0", wantErr: true},
		{name: "empty refused", a: "", b: "v0.1.0", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := release.CompareVersions(tc.a, tc.b)
			if tc.wantErr {
				if err == nil {
					t.Fatal("CompareVersions succeeded, want an error")
				}
				return
			}
			if err != nil {
				t.Fatalf("CompareVersions: %v", err)
			}
			if got != tc.want {
				t.Errorf("CompareVersions(%q, %q) = %d, want %d", tc.a, tc.b, got, tc.want)
			}
		})
	}
}
