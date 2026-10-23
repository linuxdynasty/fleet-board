package statedb_test

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

// The WatchedRepo list helper reads back every repo the owner writes, so
// the hub API can filter, sort and page watched repos in memory.
func TestWatchedRepoListReadsEveryRow(t *testing.T) {
	t.Parallel()
	dir := stateDir(t)
	create(t, dir)
	s := open(t, dir)
	ctx := context.Background()
	now := time.Date(2026, 10, 8, 7, 0, 0, 0, time.UTC)

	seed := func(host, path, requestID string) {
		t.Helper()
		spec := `{"repo":{"host":` + strconv.Quote(host) + `,"path":` + strconv.Quote(path) +
			`,"codeHost":{"value":"github"}},"enabled":true}`
		if _, err := statedb.CreateWatchedRepo(ctx, s, statedb.CreateRepo{
			RepoWrite: statedb.RepoWrite{
				Principal: "local:alice", RequestID: requestID, Now: now,
			},
			Host: host, Path: path, CodeHost: "github", Enabled: true,
			Spec: []byte(spec),
		}, nil); err != nil {
			t.Fatalf("CreateWatchedRepo %s/%s: %v", host, path, err)
		}
	}
	seed("github.com", "acme/api", "req-1")
	seed("github.com", "acme/web", "req-2")

	rows, err := statedb.ListWatchedRepoRows(ctx, s)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows %d, want 2 watched repos", len(rows))
	}

	got, err := statedb.GetWatchedRepo(ctx, s, "github.com", "acme/api")
	if err != nil {
		t.Fatal(err)
	}
	if got.Version != 1 || got.CodeHost != "github" {
		t.Errorf("row %+v, want version 1 reached through github", got)
	}
	if got.CreateMs != now.UnixMilli() || got.UpdateMs != now.UnixMilli() {
		t.Errorf("create %d update %d, want the write time for both", got.CreateMs, got.UpdateMs)
	}

	if _, err := statedb.GetWatchedRepo(ctx, s, "github.com", "acme/nope"); !errors.Is(err, statedb.ErrRepoUnknown) {
		t.Errorf("unknown repo: %v, want ErrRepoUnknown", err)
	}
}
