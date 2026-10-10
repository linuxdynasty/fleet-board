package conn_test

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/linuxdynasty/fleet-board/internal/agent/conn"
	"github.com/linuxdynasty/fleet-board/internal/hub/ca"
)

// soon is when a renewed certificate is issued in these tests: later than
// the enrolled one, so it ends later, but less than the 5 minutes the CA
// backdates, so the hub's handshake (on the real clock) takes it.
const soon = 4 * time.Minute

// pairFile is a cert.pem as a renewal writes it: a certificate auth
// issued for machine at the given time, then its new key.
func pairFile(t *testing.T, auth *ca.Authority, machine string, at time.Time) ([]byte, *x509.Certificate, ed25519.PrivateKey) {
	t.Helper()
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	c, err := auth.IssueAgent(machine, pub, at, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(c.CertDER)
	if err != nil {
		t.Fatal(err)
	}
	return append(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: c.CertDER}), keyText(t, key)...), leaf, key
}

func keyText(t *testing.T, key ed25519.PrivateKey) []byte {
	t.Helper()
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
}

func writeFile(t *testing.T, path string, data []byte, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, data, mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

// A cert.pem that is not a valid pair for this machine is logged
// (cert.file_invalid with the check it failed), never removed or changed,
// and the enrolled pair is used.
func TestAnInvalidCertFileFallsBackToTheEnrolledPair(t *testing.T) {
	t.Parallel()
	later := time.Now().Add(time.Hour)
	cases := []struct {
		name, check string
		mode        os.FileMode
		file        func(t *testing.T, h *connectHub) []byte
	}{
		{name: "garbage", check: "parse", file: func(*testing.T, *connectHub) []byte { return []byte("not a pair\n") }},
		{name: "a certificate with no key", check: "parse", file: func(t *testing.T, h *connectHub) []byte {
			t.Helper()
			data, _, _ := pairFile(t, h.auth, fakeMachine, later)
			b, _ := pem.Decode(data)
			return pem.EncodeToMemory(b)
		}},
		{name: "a key that is not the certificate's", check: "key", file: func(t *testing.T, h *connectHub) []byte {
			t.Helper()
			data, _, _ := pairFile(t, h.auth, fakeMachine, later)
			b, _ := pem.Decode(data)
			_, other, _ := ed25519.GenerateKey(rand.Reader)
			return append(pem.EncodeToMemory(b), keyText(t, other)...)
		}},
		{name: "another CA", check: "chain", file: func(t *testing.T, _ *connectHub) []byte {
			t.Helper()
			data, _, _ := pairFile(t, newCA(t), fakeMachine, later)
			return data
		}},
		{name: "another machine", check: "machine_id", file: func(t *testing.T, h *connectHub) []byte {
			t.Helper()
			data, _, _ := pairFile(t, h.auth, "01J0000000000000000000OTHER", later)
			return data
		}},
		{name: "the machine's name, another machine's URI", check: "machine_id", file: func(t *testing.T, h *connectHub) []byte {
			t.Helper()
			spec := machineCert()
			spec.uris = []string{"fleet://machine/01J0000000000000000000OTHER"}
			return customPair(t, h, spec)
		}},
		{name: "the machine's name, no URI", check: "machine_id", file: func(t *testing.T, h *connectHub) []byte {
			t.Helper()
			spec := machineCert()
			spec.uris = nil
			return customPair(t, h, spec)
		}},
		{name: "a third block", check: "parse", file: func(t *testing.T, h *connectHub) []byte {
			t.Helper()
			data, _, _ := pairFile(t, h.auth, fakeMachine, later)
			return append(data, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: h.auth.Cert.Raw})...)
		}},
		{name: "open to other users", check: "file", mode: 0o644, file: func(t *testing.T, h *connectHub) []byte {
			t.Helper()
			data, _, _ := pairFile(t, h.auth, fakeMachine, later)
			return data
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := startConnectHub(t)
			a := enrolled(t, h)
			data := tc.file(t, h)
			mode := tc.mode
			if mode == 0 {
				mode = 0o600
			}
			path := filepath.Join(a.dir, conn.CertFile)
			writeFile(t, path, data, mode)
			startLoop(t, a, nil)
			h.connected(t, 1)
			got := a.events(t, "cert.file_invalid")
			if len(got) != 1 || got[0]["check"] != tc.check || got[0]["file"] != conn.CertFile {
				t.Errorf("cert.file_invalid = %v, want one with check=%s", got, tc.check)
			}
			if serial := h.calls.of("Connect")[0].serial; serial != serialOf(enrolledLeaf(t, a)) {
				t.Errorf("the agent presented serial %s, want the enrolled one", serial)
			}
			after, err := os.ReadFile(path) //nolint:gosec // G304: the test's own temp folder
			if err != nil || !bytes.Equal(after, data) || perm(t, path) != mode {
				t.Errorf("%s was changed or removed (%v)", conn.CertFile, err)
			}
		})
	}
}

// A valid cert.pem is used only when it ends after the enrolled pair; one
// that ends sooner is left alone, and so is not logged as invalid.
func TestTheValidPairThatEndsLastIsUsed(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		at      time.Duration
		useFile bool
	}{
		{name: "ends later", at: soon, useFile: true},
		{name: "ends sooner", at: -time.Hour},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := startConnectHub(t)
			a := enrolled(t, h)
			data, leaf, _ := pairFile(t, h.auth, fakeMachine, time.Now().Add(tc.at))
			writeFile(t, filepath.Join(a.dir, conn.CertFile), data, 0o600)
			startLoop(t, a, nil)
			h.connected(t, 1)
			want := serialOf(enrolledLeaf(t, a))
			if tc.useFile {
				want = serialOf(leaf)
			}
			if got := h.calls.of("Connect")[0].serial; got != want {
				t.Errorf("the agent presented serial %s, want %s", got, want)
			}
			if got := a.events(t, "cert.file_invalid"); len(got) != 0 {
				t.Errorf("cert.file_invalid = %v for a valid file", got)
			}
		})
	}
}

// At start, a renew.key with the key of a valid cert.pem is the leftover of
// a saved renewal and is removed; any other renew.key is kept for the next
// try.
func TestALeftoverRenewKeyIsRemovedAtStart(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		same bool
	}{
		{name: "the saved pair's key", same: true},
		{name: "another key"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := startConnectHub(t)
			a := enrolled(t, h)
			data, _, key := pairFile(t, h.auth, fakeMachine, time.Now().Add(soon))
			writeFile(t, filepath.Join(a.dir, conn.CertFile), data, 0o600)
			if !tc.same {
				_, key, _ = ed25519.GenerateKey(rand.Reader)
			}
			renewKey := filepath.Join(a.dir, conn.RenewKeyFile)
			writeFile(t, renewKey, keyText(t, key), 0o600)
			startLoop(t, a, nil)
			h.connected(t, 1)
			if got := exists(t, renewKey); got == tc.same {
				t.Errorf("%s exists = %v, want %v", conn.RenewKeyFile, got, !tc.same)
			}
			if got := len(a.events(t, "renew.leftover_removed")); (got == 1) != tc.same {
				t.Errorf("%d renew.leftover_removed lines", got)
			}
		})
	}
}

// The loop's expiry check and fleet_agent_cert_expiry_seconds follow the
// pair in use: with the enrolled certificate past its end but a renewed
// one still good, the loop connects and does not end.
func TestTheExpiryCheckUsesThePairInUse(t *testing.T) {
	t.Parallel()
	h := startConnectHub(t)
	// Keep the renewed pair in use: every later renewal is refused.
	h.set(func(h *connectHub) {
		h.renew.fail = func(int) error { return connect.NewError(connect.CodeUnavailable, errNoRenew) }
	})
	a := enrolled(t, h)
	enrolledEnd := enrolledLeaf(t, a).NotAfter
	// Issued 4 minutes from now: valid now for the hub (5 minutes are
	// backdated), and ending about 4 minutes after the enrolled one.
	data, leaf, _ := pairFile(t, h.auth, fakeMachine, time.Now().Add(soon))
	writeFile(t, filepath.Join(a.dir, conn.CertFile), data, 0o600)
	clock := &skewClock{}
	clock.set(time.Until(enrolledEnd) + time.Minute)
	r := startLoop(t, a, func(o *conn.LoopOptions) { conn.SetClock(o, clock.now) })
	h.connected(t, 1)
	r.running(t)
	if got := h.calls.of("Connect")[0].serial; got != serialOf(leaf) {
		t.Errorf("the agent presented serial %s, want the renewed %s", got, serialOf(leaf))
	}
	exp, err := strconv.ParseFloat(metric(scrape(t, a), "fleet_agent_cert_expiry_seconds"), 64)
	want := time.Until(leaf.NotAfter).Seconds() // by hub time, the fake's real clock
	if err != nil || want <= 0 || math.Abs(exp-want) > 30 {
		t.Errorf("fleet_agent_cert_expiry_seconds = %v (%v), want about %v (the renewed pair)", exp, err, want)
	}
}

// customPair is a cert.pem holding a customCert for a new key and the key.
func customPair(t *testing.T, h *connectHub, spec certSpec) []byte {
	t.Helper()
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return append(customCert(t, h.auth, pub, spec), keyText(t, key)...)
}
