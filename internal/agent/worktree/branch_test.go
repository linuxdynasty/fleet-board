package worktree

import (
	"os/exec"
	"strings"
	"testing"
)

func TestValidateBranch(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		want  bool
		gitOK bool // also require git check-ref-format to agree
	}{
		{name: "main", want: true, gitOK: true},
		{name: "fleet/eshu-123", want: true, gitOK: true},
		{name: "goal-abc123", want: true, gitOK: true},
		{name: "a", want: true, gitOK: true},
		{name: "x.y-z_w/1", want: true, gitOK: true},
		{name: "", want: false},
		{name: "..", want: false},
		{name: "a..b", want: false, gitOK: true},
		{name: ".a", want: false, gitOK: true},
		{name: "a.", want: false},
		{name: "a.lock", want: false, gitOK: true},
		{name: "-a", want: false},
		{name: "a/", want: false, gitOK: true},
		{name: "/a", want: false, gitOK: true},
		{name: "a b", want: false, gitOK: true},
		{name: "a~b", want: false, gitOK: true},
		{name: "a^b", want: false, gitOK: true},
		{name: "a:b", want: false, gitOK: true},
		{name: "a?b", want: false, gitOK: true},
		{name: "a*b", want: false, gitOK: true},
		{name: "a[b", want: false, gitOK: true},
		{name: "a@{b}", want: false, gitOK: true},
		{name: "a\x7fb", want: false},
		{name: strings.Repeat("a", 300), want: false},
	}
	for _, tc := range cases {
		t.Run("branch="+tc.name, func(t *testing.T) {
			t.Parallel()
			if got := validateBranch(tc.name); got != tc.want {
				t.Fatalf("validateBranch(%q) = %v, want %v", tc.name, got, tc.want)
			}
		})
	}
}

// TestValidateBranchMatchesGit grounds the table above in git itself: every
// name both accept or both refuse must match check-ref-format. Longer names
// are ours to refuse (a length bound git does not share), so they sit out.
func TestValidateBranchMatchesGit(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git on PATH")
	}
	names := []string{"main", "fleet/eshu-123", "a..b", ".a", "a.lock", "a/", "/a", "a b", "a~b", "a^b", "a:b", "a?b", "a*b", "a[b", "a@{b}"}
	for _, name := range names {
		t.Run("git="+name, func(t *testing.T) {
			t.Parallel()
			cmd := exec.CommandContext(t.Context(), "git", "check-ref-format", "--branch", name) //nolint:gosec // G204: tests ask git about table-driven names
			gitOK := cmd.Run() == nil
			if got := validateBranch(name); got != gitOK {
				t.Fatalf("validateBranch(%q) = %v, git says %v", name, got, gitOK)
			}
		})
	}
}
