package daemon_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func leftovers(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".state.db.init-") {
			out = append(out, e.Name())
		}
	}
	return out
}

func assertLeftoversLogged(t *testing.T, h *hub, command string) {
	t.Helper()
	ev := h.logs.events(t, "hub.leftovers_removed")
	if len(ev) != 1 || ev[0]["command"] != command || ev[0]["files"] != float64(1) {
		t.Errorf("hub.leftovers_removed lines = %v, want one with command %s and files 1", ev, command)
	}
}

// An init that crashed before its link left a temporary file; the next init
// removes it under fleetd.lock and creates the store.
func TestInitRemovesTheTempFileOfACrashedInit(t *testing.T) {
	t.Parallel()
	h := newHub(t, "")
	if err := os.MkdirAll(h.state, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(h.state, ".state.db.init-0011223344556677"), []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	h.init(t)
	if got := leftovers(t, h.state); len(got) != 0 {
		t.Errorf("leftovers after init: %q", got)
	}
	if _, err := os.Stat(filepath.Join(h.state, "state.db")); err != nil {
		t.Errorf("state.db: %v", err)
	}
	assertLeftoversLogged(t, h, "init")
}

// An init that crashed after its link left the temporary name as a second
// link to state.db; the owner's next command is run, which removes it.
func TestRunRemovesASecondLinkLeftByACrashedInit(t *testing.T) {
	t.Parallel()
	h := newHub(t, "")
	h.init(t)
	if err := os.Link(filepath.Join(h.state, "state.db"), filepath.Join(h.state, ".state.db.init-8899aabbccddeeff")); err != nil {
		t.Fatal(err)
	}
	r := h.start(t, h.options())
	if got := leftovers(t, h.state); len(got) != 0 {
		t.Errorf("leftovers after run started: %q", got)
	}
	r.stop(t)
	assertLeftoversLogged(t, h, "run")
}
