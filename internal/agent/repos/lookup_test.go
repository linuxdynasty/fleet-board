package repos

import "testing"

// TestLookupRepoFindsCheckoutByShortName is the row-20 coordinator seam:
// the hub addresses checkouts by short name (host, path) and the agent
// joins it to a checkout top for the H2/F7/G1 commands.
func TestLookupRepoFindsCheckoutByShortName(t *testing.T) {
	r := NewRefresher(Config{})
	r.tops = []string{"/roots/a/checkout", "/roots/a/other"}
	r.details["/roots/a/checkout"] = Detail{
		Top: "checkout", RemoteName: "origin",
		Remotes: []Remote{{Name: "origin", Host: "github.com", Path: "o/r"}},
	}
	r.details["/roots/a/other"] = Detail{
		Top: "other", RemoteName: "origin",
		Remotes: []Remote{{Name: "origin", Host: "github.com", Path: "o/other"}},
	}
	top, ok := r.LookupRepo("github.com", "o/r")
	if !ok || top != "/roots/a/checkout" {
		t.Fatalf("LookupRepo = %q, %v, want /roots/a/checkout, true", top, ok)
	}
	if _, ok := r.LookupRepo("github.com", "o/missing"); ok {
		t.Fatal("LookupRepo of an unknown repo must be false")
	}
	if _, ok := r.LookupRepo("gitlab.com", "o/r"); ok {
		t.Fatal("LookupRepo of an unknown host must be false")
	}
}
