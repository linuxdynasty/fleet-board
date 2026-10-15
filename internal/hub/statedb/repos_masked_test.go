package statedb_test

import (
	"context"
	"testing"

	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

// TestMaskedUpdateMergesAgainstTheCurrentRow proves the merge runs inside
// the writer transaction: it sees the latest stored spec, so an update
// that only names one field keeps the fields a concurrent update wrote.
func TestMaskedUpdateMergesAgainstTheCurrentRow(t *testing.T) {
	t.Parallel()
	dir := stateDir(t)
	create(t, dir)
	s := open(t, dir)
	createRepo(t, s, repoWrite("local:alice", "req-1", 1))
	var saw string
	got, err := statedb.UpdateWatchedRepoMasked(context.Background(), s, statedb.MaskedUpdate{
		RepoWrite: repoWrite("local:alice", "req-2", 3),
		Host:      "github.com", Path: "acme/api", ExpectedVersion: 0,
		Merge: func(current []byte) ([]byte, bool, error) {
			saw = string(current)
			return []byte(`{"enabled":false,"note":"merged"}`), false, nil
		},
	})
	if err != nil {
		t.Fatalf("masked update: %v", err)
	}
	if saw != `{"enabled":true}` {
		t.Errorf("merge saw %q, want the stored spec", saw)
	}
	if got.Version != 2 || string(got.Spec) != `{"enabled":false,"note":"merged"}` {
		t.Errorf("stored = v%d %q", got.Version, got.Spec)
	}
}
