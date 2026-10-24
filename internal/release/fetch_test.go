package release_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/linuxdynasty/fleet-board/internal/release"
)

// The upgrade path fetches the release list over HTTP. These tests run it
// against a fake release endpoint, never the real GitHub.

func fakeReleases(t *testing.T, latest, tag string) *httptest.Server {
	t.Helper()
	body := `{"tag_name":"v0.2.0","assets":[` +
		`{"name":"fleetd-linux-amd64.tar.gz","browser_download_url":"%s/fleetd-linux-amd64.tar.gz"},` +
		`{"name":"SHA256SUMS","browser_download_url":"%s/SHA256SUMS"}]}`
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/o/r/releases/latest":
			_, _ = fmt.Fprintf(w, body, srv.URL, srv.URL)
		case "/repos/o/r/releases/tags/v0.2.0":
			_, _ = fmt.Fprintf(w, body, srv.URL, srv.URL)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	_ = latest
	_ = tag
	return srv
}

func TestFetchRelease(t *testing.T) {
	t.Parallel()
	srv := fakeReleases(t, "", "")
	f := release.GitHub{BaseAPI: srv.URL, Client: srv.Client()}

	got, err := f.Release(t.Context(), "o/r", "")
	if err != nil {
		t.Fatalf("Release latest: %v", err)
	}
	if got.Version != "v0.2.0" {
		t.Errorf("Version = %q, want v0.2.0", got.Version)
	}
	if len(got.Assets) != 2 {
		t.Fatalf("Assets = %v, want 2 files", got.Assets)
	}

	tagged, err := f.Release(t.Context(), "o/r", "v0.2.0")
	if err != nil {
		t.Fatalf("Release tag: %v", err)
	}
	if tagged.Version != "v0.2.0" {
		t.Errorf("Version = %q, want v0.2.0", tagged.Version)
	}

	if _, err := f.Release(t.Context(), "o/r", "v9.9.9"); err == nil {
		t.Error("Release of a missing tag succeeded, want an error")
	}
	if _, err := f.Release(t.Context(), "not-a-repo", ""); err == nil {
		t.Error("Release with a bad repo name succeeded, want an error")
	}
	for _, bad := range []string{"v0.2.0?x=1", "v0.2.0#frag", "v0.2 0", "v0.2.0/x", "../v0.2.0"} {
		if _, err := f.Release(t.Context(), "o/r", bad); err == nil {
			t.Errorf("Release with version %q succeeded, want an error", bad)
		}
	}
}

func TestDefaultClientHasATimeout(t *testing.T) {
	t.Parallel()
	f := release.GitHub{}
	if got := f.Timeout(); got <= 0 {
		t.Errorf("default fetch timeout = %v, want a positive bound", got)
	}
}

func TestFetchReleaseRefusesBadPayloads(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/o/r/releases/latest":
			_, _ = fmt.Fprint(w, `{"tag_name": 42}`)
		case "/repos/o/empty/releases/latest":
			_, _ = fmt.Fprint(w, `{"tag_name":"v0.2.0","assets":[]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	f := release.GitHub{BaseAPI: srv.URL, Client: srv.Client()}

	if _, err := f.Release(t.Context(), "o/r", ""); err == nil {
		t.Error("Release with a bad payload succeeded, want an error")
	}
	if _, err := f.Release(t.Context(), "o/empty", ""); err == nil {
		t.Error("Release with no assets succeeded, want an error")
	}
}

func TestDownload(t *testing.T) {
	t.Parallel()
	const payload = "binary-bytes"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/fleetd-linux-amd64.tar.gz" {
			_, _ = fmt.Fprint(w, payload)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	f := release.GitHub{BaseAPI: srv.URL, Client: srv.Client()}

	got, err := f.Download(t.Context(), srv.URL+"/fleetd-linux-amd64.tar.gz")
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	if string(got) != payload {
		t.Errorf("Download = %q, want %q", got, payload)
	}
	if _, err := f.Download(t.Context(), srv.URL+"/missing.tar.gz"); err == nil {
		t.Error("Download of a missing file succeeded, want an error")
	}
}
