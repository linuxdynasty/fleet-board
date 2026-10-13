package statusfile

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/agent/conn"
)

// status.json lists the folders holding two harness panes (02h "Observed
// goals"), so doctor lists them; with none the field is left out.
func TestAmbiguousFoldersWritten(t *testing.T) {
	t.Parallel()
	r, _ := newRig(t)
	_ = r.waitWrite(t)
	if got := r.read(t); len(got.AmbiguousFolders) != 0 {
		t.Fatalf("ambiguous = %q with no folders", got.AmbiguousFolders)
	}
	folders := []string{"/repos/b", "/repos/a"}
	r.observed.Store(&folders)
	r.w.Notify()
	r.clock.Advance(time.Second)
	if err := r.waitWrite(t); err != nil {
		t.Fatal(err)
	}
	got := r.read(t)
	if len(got.AmbiguousFolders) != 2 || got.AmbiguousFolders[0] != "/repos/a" || got.AmbiguousFolders[1] != "/repos/b" {
		t.Fatalf("ambiguous = %q, want both folders sorted", got.AmbiguousFolders)
	}
	r.observed.Store(nil)
	r.w.Notify()
	r.clock.Advance(time.Second)
	if err := r.waitWrite(t); err != nil {
		t.Fatal(err)
	}
	if got := r.read(t); len(got.AmbiguousFolders) != 0 {
		t.Fatalf("ambiguous = %q, want none again", got.AmbiguousFolders)
	}
}

// Only absolute, printable, bounded folders survive a read, at most
// maxAmbiguous: a damaged or hostile file reads clean.
func TestReadCleansAmbiguousFolders(t *testing.T) {
	t.Parallel()
	st := FromConn(conn.Status{State: conn.StateConnected}, t0)
	st.AmbiguousFolders = []string{"/repos/a", "relative", "/tmp/\u001b[31mred", "/" + strings.Repeat("a", 300)}
	b, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	write(t, dir, StatusFile, string(b))
	got, err := Read(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.AmbiguousFolders) != 1 || got.AmbiguousFolders[0] != "/repos/a" {
		t.Fatalf("ambiguous = %q, want only /repos/a", got.AmbiguousFolders)
	}
}
