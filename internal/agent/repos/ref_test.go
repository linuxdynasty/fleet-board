package repos_test

import (
	"testing"

	"github.com/linuxdynasty/fleet-board/internal/agent/repos"
)

func TestNormalizeRemote(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		remote string
		host   string
		path   string
		ok     bool
	}{
		{"https", "https://github.com/LinuxDynasty/fleet-board.git", "github.com", "linuxdynasty/fleet-board", true},
		{"https no git", "https://github.com/acme/repo", "github.com", "acme/repo", true},
		{"https user port", "https://ann@github.com:8443/acme/repo.git", "github.com", "acme/repo", true},
		{"scp", "git@github.com:acme/repo.git", "github.com", "acme/repo", true},
		{"scp upper", "git@GitHub.com:ACME/Repo", "github.com", "acme/repo", true},
		{"ssh", "ssh://git@github.com/acme/repo.git", "github.com", "acme/repo", true},
		{"ssh port", "ssh://git@github.com:22/acme/repo.git", "github.com", "acme/repo", true},
		{"local path is not a repo", "/Users/ann/repo", "", "", false},
		{"file url is not a repo", "file:///Users/ann/repo", "", "", false},
		{"empty", "", "", "", false},
		{"no path", "github.com", "", "", false},
		{"single segment", "https://github.com/acme", "", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			host, path, ok := repos.NormalizeRemote(tc.remote)
			if ok != tc.ok || host != tc.host || path != tc.path {
				t.Fatalf("NormalizeRemote(%q) = (%q, %q, %v), want (%q, %q, %v)",
					tc.remote, host, path, ok, tc.host, tc.path, tc.ok)
			}
		})
	}
}
