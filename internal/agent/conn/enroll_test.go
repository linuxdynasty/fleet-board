package conn_test

import (
	"bytes"
	"crypto/ed25519"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/conn"
	"github.com/linuxdynasty/fleet-board/internal/agent/conn/conntest"
	"github.com/linuxdynasty/fleet-board/internal/agent/ring"
)

// Plan row 4: enroll against the real hub listener stores a certificate
// that chains to the pinned CA, privately, and writes the cached address.
func TestEnrollAgainstTheHub(t *testing.T) {
	t.Parallel()
	h := conntest.StartHub(t, conntest.Options{})
	a := newAgent(t)
	tok := h.Token(t, time.Now().Add(15*time.Minute))

	res, err := a.enroll(t, h.Addr, tok)
	if err != nil {
		t.Fatalf("Enroll: %v", err)
	}
	if res.Replayed || res.Identity.MachineID == "" || res.Identity.FleetID != h.FleetID || res.Identity.MachineName != "laptop" {
		t.Errorf("result %+v; want a fresh enrollment of laptop in %s", res.Identity, h.FleetID)
	}
	stored, err := conn.LoadIdentity(a.dir)
	if err != nil {
		t.Fatalf("LoadIdentity: %v", err)
	}
	if stored.MachineID != res.Identity.MachineID {
		t.Errorf("stored machine %q, enrolled %q", stored.MachineID, res.Identity.MachineID)
	}
	leaf := chainsToCA(t, stored, h.CACert)
	if leaf.Subject.CommonName != stored.MachineID {
		t.Errorf("certificate common name %q, want the machine id %q", leaf.Subject.CommonName, stored.MachineID)
	}
	key, err := x509.ParsePKCS8PrivateKey(onePEM(t, []byte(stored.KeyPEM), "PRIVATE KEY"))
	if err != nil {
		t.Fatal(err)
	}
	if priv, ok := key.(ed25519.PrivateKey); !ok || !priv.Public().(ed25519.PublicKey).Equal(leaf.PublicKey) { //nolint:forcetypeassert // Public of an ed25519.PrivateKey is an ed25519.PublicKey
		t.Error("the stored key is not the certificate's Ed25519 key")
	}
	if stored.CAFingerprint != hex.EncodeToString(h.CAHash[:]) || len(stored.AccountSalt) != 32 || stored.SaltVersion != 1 {
		t.Error("the CA fingerprint, salt or salt version was not pinned")
	}
	if addr, err := conn.LoadHubAddress(a.dir); err != nil || addr != h.Addr {
		t.Errorf("cached hub address %q (%v), want %q", addr, err, h.Addr)
	}

	if got := perm(t, a.dir); got != 0o700 {
		t.Errorf("state folder mode %04o, want 0700", got)
	}
	for _, name := range []string{conn.IdentityFile, conn.HubAddressFile, ring.FileName} {
		if got := perm(t, filepath.Join(a.dir, name)); got != 0o600 {
			t.Errorf("%s mode %04o, want 0600", name, got)
		}
	}
	if exists(t, filepath.Join(a.dir, conn.PendingKeyFile)) {
		t.Error("the pending key file is still there after a successful enroll")
	}
	if n := h.Machines(t); n != 1 || !h.TokenUsed(t, tok) {
		t.Errorf("hub has %d machines, token used %v; want 1 and true", n, h.TokenUsed(t, tok))
	}

	done := a.events(t, "enroll.completed")
	if len(done) != 1 || done[0]["machine_id"] != stored.MachineID || done[0]["replayed"] != false {
		t.Errorf("enroll.completed lines %v, want one with the machine id and replayed=false", done)
	}
	logs := a.logs.String()
	for what, secret := range map[string]string{
		"the token": tok, "its secret part": tok[40:70], "the salt": base64.StdEncoding.EncodeToString(stored.AccountSalt),
	} {
		if strings.Contains(logs, secret) {
			t.Errorf("the agent's log holds %s", what)
		}
	}
}

// 02g A1: a lost reply is recovered by running enroll again, which gets the
// stored answer: the same machine and certificate, no second machine.
func TestALostReplyIsRecoveredByEnrollingAgain(t *testing.T) {
	t.Parallel()
	h := conntest.StartHub(t, conntest.Options{Wrap: conntest.DropFirstReply()})
	a := newAgent(t)
	tok := h.Token(t, time.Now().Add(15*time.Minute))

	if _, err := a.enroll(t, h.Addr, tok); err == nil {
		t.Fatal("the first Enroll succeeded although its reply was dropped")
	}
	if _, err := conn.LoadIdentity(a.dir); !errors.Is(err, conn.ErrNotEnrolled) {
		t.Fatalf("after a lost reply LoadIdentity = %v, want ErrNotEnrolled", err)
	}
	if got := perm(t, filepath.Join(a.dir, conn.PendingKeyFile)); got != 0o600 {
		t.Errorf("the pending key is mode %04o, want 0600", got)
	}
	if h.Machines(t) != 1 {
		t.Fatal("the hub did not commit the first Enroll; the test proves nothing")
	}

	res, err := a.enroll(t, h.Addr, tok)
	if err != nil {
		t.Fatalf("Enroll again: %v", err)
	}
	if !res.Replayed {
		t.Error("the second Enroll was not a replay")
	}
	issued := h.IssuedCerts(t)
	if len(issued) != 1 || !bytes.Equal(onePEM(t, []byte(res.Identity.CertPEM), "CERTIFICATE"), issued[0]) {
		t.Error("the replay did not return the certificate the hub issued the first time")
	}
	if n := h.Machines(t); n != 1 {
		t.Errorf("hub has %d machines after the replay, want 1", n)
	}
	chainsToCA(t, res.Identity, h.CACert)
}

func TestARefusedTokenExitsWithTheHubsReason(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		token  func(t *testing.T, h *conntest.Hub) string
		code   connect.Code
		reason typev1.ReasonCode
	}{
		{"expired", func(t *testing.T, h *conntest.Hub) string {
			t.Helper()
			return h.Token(t, time.Now().Add(-time.Minute))
		}, connect.CodeUnauthenticated, typev1.ReasonCode_REASON_CODE_ENROLL_TOKEN_EXPIRED},
		{"burned by another install", func(t *testing.T, h *conntest.Hub) string {
			t.Helper()
			tok := h.Token(t, time.Now().Add(time.Hour))
			if _, err := newAgent(t).enroll(t, h.Addr, tok); err != nil {
				t.Fatalf("first install: %v", err)
			}
			return tok
		}, connect.CodeAlreadyExists, typev1.ReasonCode_REASON_CODE_ENROLL_TOKEN_USED},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := conntest.StartHub(t, conntest.Options{})
			tok := tc.token(t, h)
			a := newAgent(t)
			_, err := a.enroll(t, h.Addr, tok)
			var refused *conn.RefusedError
			if !errors.As(err, &refused) || refused.Code != tc.code || refused.Reason != tc.reason {
				t.Fatalf("Enroll = %v, want a refusal %v %v", err, tc.code, tc.reason)
			}
			short := strings.TrimPrefix(tc.reason.String(), "REASON_CODE_")
			if !strings.Contains(err.Error(), short) {
				t.Errorf("the message %q does not name %s", err, short)
			}
			if _, err := conn.LoadIdentity(a.dir); !errors.Is(err, conn.ErrNotEnrolled) {
				t.Errorf("a refused agent has an identity: %v", err)
			}
			lines := a.events(t, "enroll.refused")
			if len(lines) != 1 || lines[0]["reason"] != short || lines[0]["peer"] != h.Addr {
				t.Errorf("enroll.refused lines %v, want one with reason %s and the peer", lines, short)
			}
		})
	}
}

// Once enrolled, enroll never replaces the identity, and sends nothing.
func TestAnEnrolledAgentKeepsItsIdentity(t *testing.T) {
	t.Parallel()
	h := conntest.StartHub(t, conntest.Options{})
	a := newAgent(t)
	first, err := a.enroll(t, h.Addr, h.Token(t, time.Now().Add(time.Hour)))
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(a.dir, conn.IdentityFile))
	if err != nil {
		t.Fatal(err)
	}
	sent := h.Requests()
	second := h.Token(t, time.Now().Add(time.Hour))
	_, err = a.enroll(t, h.Addr, second)
	if !errors.Is(err, conn.ErrAlreadyEnrolled) || !strings.Contains(err.Error(), first.Identity.MachineID) {
		t.Fatalf("Enroll on an enrolled agent = %v, want ErrAlreadyEnrolled naming %s", err, first.Identity.MachineID)
	}
	after, err := os.ReadFile(filepath.Join(a.dir, conn.IdentityFile))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Error("the identity file changed")
	}
	if h.Requests() != sent || h.TokenUsed(t, second) || h.Machines(t) != 1 {
		t.Error("an enrolled agent contacted the hub with the new token")
	}
}

// Before discovery exists (plan row 4, PR 4), no --hub means no attempt.
func TestNoHubAddressSendsNothing(t *testing.T) {
	t.Parallel()
	h := conntest.StartHub(t, conntest.Options{})
	a := newAgent(t)
	tok := h.Token(t, time.Now().Add(time.Hour))
	_, err := a.enroll(t, "", tok)
	if !errors.Is(err, conn.ErrNoHub) || !strings.Contains(err.Error(), "--hub") {
		t.Fatalf("Enroll with no hub = %v, want ErrNoHub naming --hub", err)
	}
	if exists(t, a.dir) {
		t.Error("the state folder was created")
	}
	if h.Requests() != 0 || h.TokenUsed(t, tok) {
		t.Error("the hub was contacted")
	}
}

func TestAMalformedTokenSendsNothing(t *testing.T) {
	t.Parallel()
	h := conntest.StartHub(t, conntest.Options{})
	a := newAgent(t)
	if _, err := a.enroll(t, h.Addr, "fbe1_not-a-token"); !errors.Is(err, conn.ErrTokenMalformed) {
		t.Fatalf("Enroll = %v, want ErrTokenMalformed", err)
	}
	if exists(t, a.dir) || h.Requests() != 0 {
		t.Error("a malformed token created the state folder or reached the hub")
	}
}

func TestALooseStateFolderIsRefused(t *testing.T) {
	t.Parallel()
	h := conntest.StartHub(t, conntest.Options{})
	a := newAgent(t)
	if err := os.Mkdir(a.dir, 0o755); err != nil { //nolint:gosec // G301: the test makes the folder loose on purpose
		t.Fatal(err)
	}
	if err := os.Chmod(a.dir, 0o755); err != nil { //nolint:gosec // G302: as above, past the umask
		t.Fatal(err)
	}
	if _, err := a.enroll(t, h.Addr, h.Token(t, time.Now().Add(time.Hour))); !errors.Is(err, conn.ErrLooseMode) {
		t.Fatalf("Enroll into a 0755 folder = %v, want ErrLooseMode", err)
	}
	if h.Requests() != 0 {
		t.Error("the hub was contacted")
	}
	if got := perm(t, a.dir); got != 0o755 {
		t.Errorf("Enroll changed the folder's mode to %04o", got)
	}
}
