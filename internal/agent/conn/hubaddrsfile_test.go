package conn_test

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/conn"
)

// The hub's latest address list outlives a restart: the list a HelloAck
// brings is saved to hub-addresses, and after a restart, with the old
// address refusing, the agent connects through the saved list.
func TestTheHubsAddressListSurvivesARestart(t *testing.T) {
	t.Parallel()
	h := startConnectHub(t)
	a := enrolled(t, h) // cached: h.addr
	h.mu.Lock()
	old := h.srv
	h.mu.Unlock()
	moved := closedAddr(t)
	h.listen(t, moved) // the same hub, also at its new address
	h.set(func(h *connectHub) {
		h.tamperAck = func(_ int, ack *agentv1.HelloAck) { ack.HubAddresses = []string{moved, "not an address!"} }
	})
	r := startLoop(t, a, nil)
	h.connected(t, 1)
	r.connected(t)
	path := filepath.Join(a.dir, conn.HubAddressesFile)
	waitFor(t, 5*time.Second, "hub-addresses", func() bool { return exists(t, path) })
	if data, err := os.ReadFile(path); err != nil || string(data) != moved+"\n" { //nolint:gosec // G304: the test's own state folder
		t.Errorf("hub-addresses = %q, %v; want %q", data, err, moved+"\n")
	}
	if p := perm(t, path); p != 0o600 {
		t.Errorf("hub-addresses mode %04o, want 0600", p)
	}
	if s := a.events(t, "connect.addresses_saved"); len(s) != 1 || s[0]["count"] != float64(1) {
		t.Errorf("connect.addresses_saved = %v", s)
	}
	r.cancel()
	_ = r.result(t, 10*time.Second)

	if err := old.Close(); err != nil { // the old address now refuses
		t.Fatal(err)
	}
	before := len(a.events(t, "connect.attempt"))
	r2 := startLoop(t, a, nil)
	r2.connected(t)
	got := attempts(t, a)[before:]
	if want := [][2]string{{"cached", h.addr}, {"configured", moved}}; !slices.Equal(got, want) {
		t.Errorf("after the restart attempts = %v, want %v", got, want)
	}
	start := a.events(t, "connect.addresses")
	if last := start[len(start)-1]; last["source"] != conn.HubAddressesFile || last["count"] != float64(1) {
		t.Errorf("connect.addresses = %v, want the saved list of 1", last)
	}
}

// A HelloAck with no list, with only invalid entries, or with the list
// already in use leaves hub-addresses as it is: not rewritten.
func TestAnUnchangedListIsNotSaved(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		list func(h *connectHub) []string
	}{
		{"empty", func(*connectHub) []string { return nil }},
		{"only invalid entries", func(*connectHub) []string { return []string{"", "a b", "host:0"} }},
		{"the list in use", func(h *connectHub) []string { return []string{h.addr} }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := startConnectHub(t)
			a := enrolled(t, h)
			writeState(t, a, conn.HubAddressesFile, h.addr+"\n")
			path := filepath.Join(a.dir, conn.HubAddressesFile)
			before, err := os.Lstat(path)
			if err != nil {
				t.Fatal(err)
			}
			list := tc.list(h)
			h.set(func(h *connectHub) {
				h.tamperAck = func(_ int, ack *agentv1.HelloAck) { ack.HubAddresses = list }
			})
			r := startLoop(t, a, func(o *conn.LoopOptions) { o.Identity.HubAddresses = []string{closedAddr(t)} })
			h.connected(t, 1)
			r.connected(t)
			after, err := os.Lstat(path)
			if err != nil {
				t.Fatal(err)
			}
			if !os.SameFile(before, after) {
				t.Error("hub-addresses was rewritten")
			}
			if s := a.events(t, "connect.addresses_saved"); len(s) != 0 {
				t.Errorf("connect.addresses_saved = %v", s)
			}
		})
	}
}

// A damaged hub-addresses falls back to the identity list: no valid line,
// a file open to other users, or a folder in its place. Invalid lines next
// to a valid one are dropped and the valid one is used.
func TestADamagedListFallsBackToTheIdentityList(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		make    func(t *testing.T, path, hub string)
		source  string
		errKind string
	}{
		{"no valid line", func(t *testing.T, path, _ string) {
			t.Helper()
			write(t, path, "not an address!\n\nhost:port:extra\n", 0o600)
		}, "identity", "parse"},
		{"open to other users", func(t *testing.T, path, _ string) {
			t.Helper()
			write(t, path, closedAddr(t)+"\n", 0o644)
		}, "identity", "constraint"},
		{"a folder", func(t *testing.T, path, _ string) {
			t.Helper()
			if err := os.Mkdir(path, 0o700); err != nil {
				t.Fatal(err)
			}
		}, "identity", "constraint"},
		{"one bad line, one good", func(t *testing.T, path, hub string) {
			t.Helper()
			write(t, path, "not an address!\n"+hub+"\n", 0o600)
		}, conn.HubAddressesFile, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := startConnectHub(t)
			a := enrolled(t, h)
			removeState(t, a, conn.HubAddressFile)
			tc.make(t, filepath.Join(a.dir, conn.HubAddressesFile), h.addr)
			idList := []string{h.addr}
			if tc.source == conn.HubAddressesFile {
				idList = []string{closedAddr(t)}
			}
			r := startLoop(t, a, func(o *conn.LoopOptions) { o.Identity.HubAddresses = idList })
			h.connected(t, 1)
			r.connected(t)
			l := a.events(t, "connect.addresses")
			if len(l) != 1 || l[0]["source"] != tc.source || l[0]["count"] != float64(1) {
				t.Fatalf("connect.addresses = %v, want source %s with 1", l, tc.source)
			}
			if got, _ := l[0]["err_kind"].(string); got != tc.errKind {
				t.Errorf("err_kind = %q, want %q", got, tc.errKind)
			}
			if got := attempts(t, a); len(got) != 1 || got[0] != [2]string{"configured", h.addr} {
				t.Errorf("attempts = %v, want only %s", got, h.addr)
			}
		})
	}
}

// A list that cannot be saved is still used, and the stream stays up.
func TestAFailedSaveKeepsTheStream(t *testing.T) {
	t.Parallel()
	h := startConnectHub(t)
	a := enrolled(t, h)
	path := filepath.Join(a.dir, conn.HubAddressesFile)
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(path, "keep"), "x", 0o600) // a folder that is not empty cannot be renamed over
	other := closedAddr(t)
	h.set(func(h *connectHub) {
		h.tamperAck = func(_ int, ack *agentv1.HelloAck) { ack.HubAddresses = []string{h.addr, other} }
	})
	r := startLoop(t, a, nil)
	h.connected(t, 1)
	r.connected(t)
	waitFor(t, 5*time.Second, "the failed save", func() bool {
		for _, l := range a.events(t, "connect.failed") {
			if l["step"] == "save" && l["peer"] == h.addr {
				return true
			}
		}
		return false
	})
	time.Sleep(100 * time.Millisecond)
	if st := r.loop.Status(); st.State != conn.StateConnected || h.count() != 1 {
		t.Errorf("after the failed save: state %s, %d streams; want the first stream still connected", st.State, h.count())
	}
}

// Enroll removes a leftover hub-addresses with the new identity.
func TestEnrollRemovesALeftoverAddressList(t *testing.T) {
	t.Parallel()
	h := startConnectHub(t)
	a := newAgent(t)
	if err := conn.EnsureDir(a.dir); err != nil {
		t.Fatal(err)
	}
	writeState(t, a, conn.HubAddressesFile, "old.example:7443\n")
	if _, err := a.enroll(t, h.addr, h.token); err != nil {
		t.Fatal(err)
	}
	if exists(t, filepath.Join(a.dir, conn.HubAddressesFile)) {
		t.Error("the leftover hub-addresses is still there")
	}
}

func write(t *testing.T, path, data string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(data), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil { // past the umask
		t.Fatal(err)
	}
}
