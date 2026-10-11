package policy_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/linuxdynasty/fleet-board/internal/agent/policy"
)

func TestInspectReadsWithoutSideEffects(t *testing.T) {
	t.Parallel()
	roots := t.TempDir()
	tests := []struct {
		name      string
		content   *string
		present   bool
		wantHash  bool
		wantCode  string
		wantRoots int
	}{
		{name: "missing file is a normal first run"},
		{name: "valid file", content: ptr("schema_version: \"1\"\nallowed_repo_roots: [" + roots + "]\n"), present: true, wantHash: true, wantRoots: 1},
		{name: "unknown key is a problem", content: ptr("nope: 1\n"), present: true, wantHash: true, wantCode: policy.CodeUnknownKey},
		{name: "bad yaml", content: ptr("a: [\n"), present: true, wantHash: true, wantCode: policy.CodeBadYAML},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			if tc.content != nil {
				if err := os.WriteFile(filepath.Join(dir, policy.File), []byte(*tc.content), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			in := policy.Inspect(dir)
			if in.Present != tc.present || (in.Hash != "") != tc.wantHash {
				t.Errorf("Present=%v Hash=%q, want present=%v hash=%v", in.Present, in.Hash, tc.present, tc.wantHash)
			}
			if (in.Problem == nil) != (tc.wantCode == "") || (in.Problem != nil && in.Problem.Code != tc.wantCode) {
				t.Errorf("Problem = %v, want code %q", in.Problem, tc.wantCode)
			}
			if in.Policy == nil || len(in.Policy.AllowedRepoRoots) != tc.wantRoots {
				t.Errorf("Policy = %+v, want %d roots and never nil", in.Policy, tc.wantRoots)
			}
			if in.Path != filepath.Join(dir, policy.File) {
				t.Errorf("Path = %q", in.Path)
			}
		})
	}
}

func TestInspectOfAFifoDoesNotBlockAndIsUnreadable(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	mkfifo(t, filepath.Join(dir, policy.File))
	var in policy.Inspection
	within(t, "Inspect on a FIFO", func() { in = policy.Inspect(dir) })
	if in.Problem == nil || in.Problem.Code != policy.CodeUnreadable {
		t.Errorf("Problem = %v, want unreadable", in.Problem)
	}
}

func ptr(s string) *string { return &s }
