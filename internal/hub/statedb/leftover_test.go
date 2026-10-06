package statedb_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

// An init that crashed leaves its temporary file (and SQLite's side files)
// behind; the next init or run removes them under fleetd.lock.
func TestRemoveLeftoversDeletesTheTempFilesOfACrashedInit(t *testing.T) {
	t.Parallel()
	dir := stateDir(t)
	for _, name := range []string{".state.db.init-0011223344556677", ".state.db.init-0011223344556677-wal", "fleetd.lock", "keep.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	n, err := statedb.RemoveLeftovers(dir)
	if err != nil {
		t.Fatalf("RemoveLeftovers: %v", err)
	}
	if n != 2 {
		t.Errorf("removed %d files, want 2", n)
	}
	if got, want := listDir(t, dir), []string{"fleetd.lock", "keep.txt"}; !reflect.DeepEqual(got, want) {
		t.Errorf("state folder holds %q, want %q", got, want)
	}
}

// A crash between the link and the temp file's removal leaves the temp name
// as a second link to state.db; removing it leaves state.db as it was.
func TestRemoveLeftoversDropsASecondLinkToStateDBAndKeepsTheStore(t *testing.T) {
	t.Parallel()
	dir := stateDir(t)
	create(t, dir)
	path := filepath.Join(dir, "state.db")
	if err := os.Link(path, filepath.Join(dir, ".state.db.init-8899aabbccddeeff")); err != nil {
		t.Fatal(err)
	}
	before := fileHash(t, path)
	n, err := statedb.RemoveLeftovers(dir)
	if err != nil || n != 1 {
		t.Fatalf("RemoveLeftovers = %d, %v; want 1, nil", n, err)
	}
	if got := listDir(t, dir); !reflect.DeepEqual(got, []string{"state.db"}) {
		t.Errorf("state folder holds %q, want only state.db", got)
	}
	if fileHash(t, path) != before {
		t.Error("state.db changed")
	}
}

// A failure syncing the folder comes after the link: state.db exists and is
// complete, and the error says so instead of implying nothing was created.
func TestAFolderSyncFailureAfterTheLinkSaysStateDBWasCreated(t *testing.T) {
	t.Parallel()
	dir := stateDir(t)
	boom := errors.New("injected folder sync failure")
	err := statedb.Create(context.Background(), dir, sampleIdentity(), statedb.CreateOptions{
		SyncDir: func(string) error { return boom },
	})
	if !errors.Is(err, boom) || !errors.Is(err, statedb.ErrCreatedNotSynced) {
		t.Fatalf("Create = %v, want the injected failure wrapped in ErrCreatedNotSynced", err)
	}
	if !strings.Contains(err.Error(), "state.db was created") {
		t.Errorf("message %q does not say state.db was created", err)
	}
	if got := listDir(t, dir); !reflect.DeepEqual(got, []string{"state.db"}) {
		t.Errorf("state folder holds %q, want state.db and no temporary file", got)
	}
	s := open(t, dir)
	if id, err := statedb.ReadIdentity(context.Background(), s); err != nil || id.FleetID != sampleIdentity().FleetID {
		t.Errorf("ReadIdentity = %q, %v; want the identity row", id.FleetID, err)
	}
}
