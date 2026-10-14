package repos_test

import (
	"testing"

	"github.com/linuxdynasty/fleet-board/internal/agent/repos"
)

func TestParseAuthStatus(t *testing.T) {
	t.Parallel()
	out := `github.com
  ✓ Logged in to github.com account octocat (keyring)
  - Active account: true
  - Git operations protocol: https
  - Token: *******************
  - Token scopes: 'repo', 'read:org', 'gist'
ghe.acme.dev
  X Failed to log in to ghe.acme.dev
`
	got := repos.ParseAuthStatusForTest(out)
	if len(got) != 1 {
		t.Fatalf("hosts = %+v, want the one signed-in host", got)
	}
	h := got[0]
	if h.Tool != "gh" || h.Host != "github.com" || h.User != "octocat" || !h.SignedIn {
		t.Fatalf("host = %+v, want gh/github.com/octocat signed in", h)
	}
}

func TestParseAuthStatusEmpty(t *testing.T) {
	t.Parallel()
	for _, out := range []string{"", "github.com\n  X not logged in\n"} {
		if got := repos.ParseAuthStatusForTest(out); len(got) != 0 {
			t.Fatalf("hosts = %+v, want none", got)
		}
	}
}

func TestParseViewerPermission(t *testing.T) {
	t.Parallel()
	cases := []struct {
		out  string
		perm string
		push bool
		ok   bool
	}{
		{`{"viewerPermission":"ADMIN"}`, "ADMIN", true, true},
		{`{"viewerPermission":"WRITE"}`, "WRITE", true, true},
		{`{"viewerPermission":"READ"}`, "READ", false, true},
		{`not json`, "", false, false},
		{`{}`, "", false, false},
	}
	for _, tc := range cases {
		perm, push, ok := repos.ParseViewerPermissionForTest(tc.out)
		if perm != tc.perm || push != tc.push || ok != tc.ok {
			t.Fatalf("parse(%q) = (%q, %v, %v), want (%q, %v, %v)",
				tc.out, perm, push, ok, tc.perm, tc.push, tc.ok)
		}
	}
}
