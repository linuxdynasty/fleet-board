package release

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Limits keep an untrusted endpoint from eating memory: the release list is
// small JSON, an archive is one static binary.
const (
	maxReleaseList = 1 << 20
	maxDownload    = 128 << 20
)

// ReleaseInfo is one GitHub release: its tag plus its downloadable files.
type ReleaseInfo struct {
	Version string
	Assets  []Asset
}

// Fetcher lists releases and downloads files. GitHub is the live fetcher;
// tests pass a fake endpoint through BaseAPI.
type Fetcher interface {
	Release(ctx context.Context, repo, version string) (ReleaseInfo, error)
	Download(ctx context.Context, url string) ([]byte, error)
}

// GitHub reads public releases. It sends no credentials: releases are
// public, and nothing secret is ever read for an install or upgrade.
type GitHub struct {
	BaseAPI string
	Client  *http.Client
}

// defaultTimeout bounds one upgrade fetch when the caller sets no
// deadline: releases are small JSON and one static binary, so 30 seconds
// (the repo's HTTP convention) is generous.
const defaultTimeout = 30 * time.Second

func (g GitHub) client() *http.Client {
	if g.Client != nil {
		return g.Client
	}
	return &http.Client{Timeout: defaultTimeout}
}

// Timeout reports the bound client() applies without an explicit client.
func (g GitHub) Timeout() time.Duration {
	return g.client().Timeout
}

func (g GitHub) base() string {
	if g.BaseAPI != "" {
		return strings.TrimSuffix(g.BaseAPI, "/")
	}
	return "https://api.github.com"
}

// Release returns the latest release (version "") or the named tag.
func (g GitHub) Release(ctx context.Context, repo, version string) (ReleaseInfo, error) {
	if err := checkRepo(repo); err != nil {
		return ReleaseInfo{}, err
	}
	path := "/repos/" + repo + "/releases/latest"
	if version != "" {
		v := strings.TrimSpace(version)
		if err := checkVersion(v); err != nil {
			return ReleaseInfo{}, err
		}
		path = "/repos/" + repo + "/releases/tags/" + v
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, g.base()+path, nil)
	if err != nil {
		return ReleaseInfo{}, fmt.Errorf("release: list releases: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := g.client().Do(req)
	if err != nil {
		return ReleaseInfo{}, fmt.Errorf("release: list releases: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return ReleaseInfo{}, fmt.Errorf("release: list releases: server answered %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxReleaseList+1))
	if err != nil {
		return ReleaseInfo{}, fmt.Errorf("release: list releases: %w", err)
	}
	if len(body) > maxReleaseList {
		return ReleaseInfo{}, fmt.Errorf("release: release list is too large")
	}
	var payload struct {
		TagName string `json:"tag_name"`
		Assets  []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
		} `json:"assets"`
	}
	// Parsed leniently: GitHub sends many fields this code ignores; what
	// matters (the tag and every file's name and address) is checked below.
	if err := json.Unmarshal(body, &payload); err != nil {
		return ReleaseInfo{}, fmt.Errorf("release: bad release list: %w", err)
	}
	if payload.TagName == "" {
		return ReleaseInfo{}, fmt.Errorf("release: release list has no tag")
	}
	if len(payload.Assets) == 0 {
		return ReleaseInfo{}, fmt.Errorf("release: %s holds no files", payload.TagName)
	}
	out := ReleaseInfo{Version: payload.TagName}
	for _, a := range payload.Assets {
		if a.Name == "" || a.URL == "" {
			return ReleaseInfo{}, fmt.Errorf("release: %s lists a file with no name or address", payload.TagName)
		}
		out.Assets = append(out.Assets, Asset{Name: a.Name, URL: a.URL})
	}
	return out, nil
}

// Download fetches one file. Redirects are followed by the HTTP client;
// a non-200 answer is an error, never an empty install.
func (g GitHub) Download(ctx context.Context, url string) ([]byte, error) {
	if !strings.HasPrefix(url, "https://") && !strings.HasPrefix(url, "http://") {
		return nil, fmt.Errorf("release: bad download address")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("release: download: %w", err)
	}
	resp, err := g.client().Do(req)
	if err != nil {
		return nil, fmt.Errorf("release: download: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("release: download: server answered %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxDownload+1))
	if err != nil {
		return nil, fmt.Errorf("release: download: %w", err)
	}
	if len(body) > maxDownload {
		return nil, fmt.Errorf("release: download is too large")
	}
	if len(body) == 0 {
		return nil, fmt.Errorf("release: download is empty")
	}
	return body, nil
}

// checkVersion whitelists tag characters (letters, digits, dot, dash,
// underscore, optional leading v): a tag goes into a URL path, so anything
// URL-significant (?, #, space, slash) is refused before the request.
func checkVersion(v string) error {
	s := strings.TrimPrefix(v, "v")
	if s == "" {
		return fmt.Errorf("release: bad version %q: want vX.Y.Z", v)
	}
	for _, c := range s {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '-', c == '_', c == '.':
		default:
			return fmt.Errorf("release: bad version %q: want vX.Y.Z", v)
		}
	}
	return nil
}

func checkRepo(repo string) error {
	owner, name, ok := strings.Cut(repo, "/")
	if !ok || owner == "" || name == "" || strings.Contains(repo, "..") {
		return fmt.Errorf("release: bad repository %q: want owner/name", repo)
	}
	for _, c := range repo {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '-', c == '_', c == '.', c == '/':
		default:
			return fmt.Errorf("release: bad repository %q: want owner/name", repo)
		}
	}
	return nil
}
