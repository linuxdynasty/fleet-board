package conn_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/conn"
)

// The fixes the end tells the owner (the arbiter's ruling on #32, F-B).
const (
	reEnrollFix = "remove `identity.json` (keep `agent.db`), then `fleet-agent enroll --token <new>`"
	mismatchFix = "check the hub address, then re-enroll or remove `ended.json` to try again"
)

// writeEnded puts an ended.json the way the loop writes it, mode 0600.
func writeEnded(t *testing.T, dir, end, machineID string) {
	t.Helper()
	data, err := json.Marshal(map[string]any{
		"version": 1, "end": end, "machine_id": machineID, "peer": "hub.example:7443", "time": time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, conn.EndedFile), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// An end that needs the owner is saved in ended.json before Run returns,
// so a restart does not reconnect; an expired certificate is read from the
// certificate itself and saves nothing.
func TestTheLoopSavesAnEndThatNeedsTheOwner(t *testing.T) {
	t.Parallel()
	byeAtHello := func(reason agentv1.ByeReason) func(*connectHub) {
		return func(h *connectHub) {
			h.onHello = func(int, *agentv1.Hello) *agentv1.Bye { return bye(reason) }
		}
	}
	cases := []struct {
		name      string
		hub       func(*connectHub)
		opts      func(*conn.LoopOptions)
		connected bool
		want      conn.End
		fix       string
	}{
		{name: "revoked at hello", hub: byeAtHello(agentv1.ByeReason_BYE_REASON_REVOKED), want: conn.EndRevoked, fix: reEnrollFix},
		{name: "revoked while connected", connected: true, want: conn.EndRevoked, fix: reEnrollFix},
		{
			name: "unknown machine", hub: byeAtHello(agentv1.ByeReason_BYE_REASON_UNKNOWN_MACHINE),
			want: conn.EndUnknownMachine, fix: reEnrollFix,
		},
		{name: "another fleet", hub: func(h *connectHub) { h.fleetID = "fleet-other" }, want: conn.EndFleetMismatch, fix: mismatchFix},
		{
			name: "certificate expired", want: conn.EndCertExpired,
			opts: func(o *conn.LoopOptions) { o.Identity.CertNotAfter = time.Now().Add(-time.Minute) },
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := startConnectHub(t)
			a := enrolled(t, h)
			id, err := conn.LoadIdentity(a.dir)
			if err != nil {
				t.Fatal(err)
			}
			if tc.hub != nil {
				h.set(tc.hub)
			}
			start := time.Now()
			r := startLoop(t, a, tc.opts)
			if tc.connected {
				h.connected(t, 1)
				h.push(t, &agentv1.ConnectResponse{Body: &agentv1.ConnectResponse_Bye{Bye: bye(agentv1.ByeReason_BYE_REASON_REVOKED)}})
			}
			var ended *conn.EndedError
			if err := r.result(t, 10*time.Second); !errors.As(err, &ended) || ended.End != tc.want {
				t.Fatalf("Run = %v, want the loop ended as %s", err, tc.want)
			}
			path := filepath.Join(a.dir, conn.EndedFile)
			if tc.want == conn.EndCertExpired {
				if exists(t, path) {
					t.Fatal("an expired certificate saved ended.json")
				}
				if _, err := conn.CheckEnded(a.dir, id); err != nil {
					t.Errorf("CheckEnded with no ended.json = %v", err)
				}
				return
			}
			if p := perm(t, path); p != 0o600 {
				t.Errorf("ended.json mode %04o, want 0600", p)
			}
			data, err := os.ReadFile(path) //nolint:gosec // G304: a file in the test's temp folder
			if err != nil {
				t.Fatal(err)
			}
			var got struct {
				Version   int       `json:"version"`
				End       string    `json:"end"`
				MachineID string    `json:"machine_id"`
				Peer      string    `json:"peer"`
				Time      time.Time `json:"time"`
			}
			if err := json.Unmarshal(data, &got); err != nil {
				t.Fatalf("ended.json %q: %v", data, err)
			}
			if got.Version != 1 || got.End != string(tc.want) || got.MachineID != id.MachineID || got.Peer != h.addr ||
				got.Time.Before(start.Add(-time.Second)) || got.Time.After(time.Now().Add(time.Second)) {
				t.Errorf("ended.json = %+v, want version 1, %s, machine %s, peer %s, now", got, tc.want, id.MachineID, h.addr)
			}
			// The restart: run reads it before any network call.
			removed, err := conn.CheckEnded(a.dir, id)
			var refused *conn.EndedError
			if removed || !errors.As(err, &refused) || refused.End != tc.want || !strings.Contains(err.Error(), tc.fix) ||
				!strings.Contains(err.Error(), h.addr) {
				t.Errorf("CheckEnded = %v, %v; want the %s end naming the hub and the fix %q", removed, err, tc.want, tc.fix)
			}
		})
	}
}

// ended.json for another machine id is a leftover of an earlier enrollment:
// it is removed and the agent connects.
func TestAnEndForAnotherMachineIsRemoved(t *testing.T) {
	t.Parallel()
	h := startConnectHub(t)
	a := enrolled(t, h)
	id, err := conn.LoadIdentity(a.dir)
	if err != nil {
		t.Fatal(err)
	}
	writeEnded(t, a.dir, string(conn.EndRevoked), "01J0000000000000000000OTHER")
	removed, err := conn.CheckEnded(a.dir, id)
	if !removed || err != nil {
		t.Fatalf("CheckEnded = %v, %v; want the leftover removed", removed, err)
	}
	if exists(t, filepath.Join(a.dir, conn.EndedFile)) {
		t.Fatal("the leftover ended.json is still there")
	}
	r := startLoop(t, a, nil)
	h.connected(t, 1)
	r.connected(t)
}

// A damaged ended.json is refused, like a damaged identity.json: the hub's
// last word may be in it, so the agent does not guess. Removing it retries.
func TestADamagedEndIsRefused(t *testing.T) {
	t.Parallel()
	h := startConnectHub(t)
	a := enrolled(t, h)
	id, err := conn.LoadIdentity(a.dir)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(a.dir, conn.EndedFile)
	for name, content := range map[string]string{
		"not json":       `{"version":1,"end":"rev`,
		"another layout": `{"version":2,"end":"revoked","machine_id":"` + id.MachineID + `"}`,
		"an unknown end": `{"version":1,"end":"cert_expired","machine_id":"` + id.MachineID + `"}`,
		"no machine":     `{"version":1,"end":"revoked"}`,
	} {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		removed, err := conn.CheckEnded(a.dir, id)
		var ended *conn.EndedError
		if removed || err == nil || errors.As(err, &ended) || !strings.Contains(err.Error(), conn.EndedFile) {
			t.Errorf("%s: CheckEnded = %v, %v; want an error naming ended.json", name, removed, err)
		}
		if !exists(t, path) {
			t.Errorf("%s: the damaged ended.json was removed", name)
		}
	}
	if err := os.Chmod(path, 0o644); err != nil { //nolint:gosec // G302: the test loosens the file on purpose
		t.Fatal(err)
	}
	if _, err := conn.CheckEnded(a.dir, id); !errors.Is(err, conn.ErrLooseMode) {
		t.Errorf("CheckEnded on a 0644 ended.json = %v, want ErrLooseMode", err)
	}
}

// When ended.json cannot be written the loop logs it and still ends.
func TestAnEndThatCannotBeSavedStillEnds(t *testing.T) {
	t.Parallel()
	h := startConnectHub(t)
	a := enrolled(t, h)
	// A folder in its place makes the rename fail.
	if err := os.MkdirAll(filepath.Join(a.dir, conn.EndedFile, "x"), 0o700); err != nil {
		t.Fatal(err)
	}
	h.set(func(h *connectHub) {
		h.onHello = func(int, *agentv1.Hello) *agentv1.Bye { return bye(agentv1.ByeReason_BYE_REASON_REVOKED) }
	})
	r := startLoop(t, a, nil)
	var ended *conn.EndedError
	if err := r.result(t, 10*time.Second); !errors.As(err, &ended) || ended.End != conn.EndRevoked {
		t.Fatalf("Run = %v, want the loop ended as revoked", err)
	}
	saves := a.events(t, "connect.failed")
	if len(saves) != 1 || saves[0]["step"] != "save" || saves[0]["peer"] != h.addr || saves[0]["err_kind"] == "" {
		t.Errorf("connect.failed = %v, want one step=save naming the hub", saves)
	}
}

// Enroll removes ended.json when it saves a new identity, so the owner's
// re-enroll is the whole fix; an enrolled folder keeps it.
func TestEnrollRemovesTheEnd(t *testing.T) {
	t.Parallel()
	h := startConnectHub(t)
	a := enrolled(t, h)
	id, err := conn.LoadIdentity(a.dir)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(a.dir, conn.EndedFile)
	writeEnded(t, a.dir, string(conn.EndRevoked), id.MachineID)
	if _, err := a.enroll(t, h.addr, h.token); !errors.Is(err, conn.ErrAlreadyEnrolled) ||
		!strings.Contains(err.Error(), conn.IdentityFile) || !strings.Contains(err.Error(), "keep agent.db") {
		t.Fatalf("Enroll on the enrolled folder = %v, want ErrAlreadyEnrolled with its advice", err)
	}
	if !exists(t, path) {
		t.Fatal("a refused enroll removed ended.json")
	}
	if err := os.Remove(filepath.Join(a.dir, conn.IdentityFile)); err != nil {
		t.Fatal(err)
	}
	if _, err := a.enroll(t, h.addr, h.token); err != nil {
		t.Fatalf("Enroll after following the advice: %v", err)
	}
	if exists(t, path) {
		t.Fatal("ended.json is still there after a new enrollment")
	}
	id, err = conn.LoadIdentity(a.dir)
	if err != nil {
		t.Fatal(err)
	}
	if removed, err := conn.CheckEnded(a.dir, id); removed || err != nil {
		t.Errorf("CheckEnded after enroll = %v, %v", removed, err)
	}
}
