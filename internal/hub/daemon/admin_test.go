package daemon_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"io/fs"
	"net"
	"os"
	"strings"
	"testing"

	"connectrpc.com/connect"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/api"
	"github.com/linuxdynasty/fleet-board/internal/hub/config"
	"github.com/linuxdynasty/fleet-board/internal/hub/daemon"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

func enroll(t *testing.T, sock, requestID string) (*hubv1.CreateEnrollmentTokenResponse, error) {
	t.Helper()
	resp, err := api.AdminClient(sock).CreateEnrollmentToken(context.Background(),
		connect.NewRequest(&hubv1.CreateEnrollmentTokenRequest{RequestId: requestID, MachineName: "laptop"}))
	if err != nil {
		return nil, err
	}
	return resp.Msg, nil
}

// stateBytes reads every file in the state folder (state.db, its -wal and
// -shm) into one slice.
func stateBytes(t *testing.T, dir string) []byte {
	t.Helper()
	var all []byte
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Type()&fs.ModeSocket != 0 || e.IsDir() {
			continue
		}
		b, err := fs.ReadFile(os.DirFS(dir), e.Name())
		if err != nil {
			t.Fatal(err)
		}
		all = append(all, b...)
	}
	return all
}

func TestTheAdminSocketIssuesATokenForTheStoredCA(t *testing.T) {
	t.Parallel()
	h := newHub(t, "")
	h.init(t)
	id := h.storedIdentity(t)
	r := h.start(t, h.options())
	sock := config.AdminSocketPath(h.state)
	if r.addrs.Admin == nil || r.addrs.Admin.String() != sock {
		t.Errorf("Addrs.Admin = %v, want %s", r.addrs.Admin, sock)
	}
	fi, err := os.Lstat(sock)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Type() != fs.ModeSocket || fi.Mode().Perm() != 0o600 {
		t.Errorf("admin.sock mode %v, want a socket with 0600", fi.Mode())
	}

	resp, err := enroll(t, sock, "req-1")
	if err != nil {
		t.Fatalf("CreateEnrollmentToken: %v", err)
	}
	tok := resp.GetToken()
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(tok, "fbe1_"))
	if err != nil || len(raw) != 65 || raw[0] != 1 {
		t.Fatalf("token decodes to %d bytes (err %v), want 65 starting with 1", len(raw), err)
	}
	fp := sha256.Sum256(id.CACert)
	if !bytes.Equal(raw[1:33], fp[:]) {
		t.Error("the token's CA hash is not SHA-256 of the CA stored in hub_identity")
	}
	secret := raw[33:]
	hash := sha256.Sum256(secret)
	needles := map[string][]byte{
		"token": []byte(tok), "secret": secret, "secret (base64url)": []byte(base64.RawURLEncoding.EncodeToString(secret)),
		"secret (hex)": []byte(hex.EncodeToString(secret)),
	}
	scan := func(when string, hay []byte) {
		for name, n := range needles {
			if bytes.Contains(hay, n) {
				t.Errorf("%s: found the %s", when, name)
			}
		}
	}
	scan("state folder while running", stateBytes(t, h.state))

	r.stop(t)
	if _, err := os.Lstat(sock); !os.IsNotExist(err) {
		t.Errorf("admin.sock still exists after a clean stop: %v", err)
	}
	scan("state folder after stop", stateBytes(t, h.state))
	logs := h.logs.String()
	scan("logs", []byte(logs))
	for _, s := range []string{hex.EncodeToString(hash[:])[:12], base64.StdEncoding.EncodeToString(hash[:])[:16]} {
		if strings.Contains(logs, s) {
			t.Error("the logs hold a prefix of the secret's hash")
		}
	}
	if !bytes.Contains(stateBytes(t, h.state), hash[:]) {
		t.Error("state.db does not hold SHA-256 of the secret")
	}
	st, err := statedb.Open(context.Background(), h.state, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	var stored []byte
	if err := st.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRowContext(t.Context(), `SELECT secret_hash FROM enroll_tokens WHERE request_id = 'req-1'`).Scan(&stored)
	}); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(stored, hash[:]) {
		t.Error("enroll_tokens.secret_hash is not SHA-256 of the decoded secret")
	}
}

func TestAnInjectedOtherUserIsClosedUnservedByRun(t *testing.T) {
	t.Parallel()
	h := newHub(t, "")
	h.init(t)
	opts := h.options()
	other := selfUID(t) + 1
	opts.PeerUID = func(*net.UnixConn) (uint32, error) { return other, nil }
	h.start(t, opts)
	if _, err := enroll(t, config.AdminSocketPath(h.state), "req-1"); err == nil {
		t.Fatal("a peer of another user was served")
	}
	if len(h.logs.events(t, "admin.peer_refused")) != 1 || len(h.logs.events(t, "enroll.issued")) != 0 {
		t.Error("want one admin.peer_refused line and no enroll.issued")
	}
	if got := sample(h.scrape(t), "fleet_hub_auth_failures_total", `reason="peer_uid"`); got != 1 {
		t.Errorf("fleet_hub_auth_failures_total{peer_uid} = %v, want 1", got)
	}
}

// A hub killed with SIGKILL leaves admin.sock behind with no listener; the
// next run, holding fleetd.lock, removes it and binds a fresh one.
func TestALeftoverSocketDoesNotBlockTheNextStart(t *testing.T) {
	t.Parallel()
	h := newHub(t, "")
	h.init(t)
	sock := config.AdminSocketPath(h.state)
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: sock, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	ln.SetUnlinkOnClose(false)
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	var d net.Dialer
	if _, err := d.DialContext(t.Context(), "unix", sock); err == nil {
		t.Fatal("the leftover socket still accepts")
	}
	h.start(t, h.options())
	if _, err := enroll(t, sock, "req-1"); err != nil {
		t.Errorf("enroll after a leftover socket: %v", err)
	}
}

func TestANonSocketAtTheAdminPathIsRefusedAndKept(t *testing.T) {
	t.Parallel()
	h := newHub(t, "")
	h.init(t)
	sock := config.AdminSocketPath(h.state)
	if err := os.WriteFile(sock, []byte("not a socket"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := daemon.Run(context.Background(), h.options())
	if got := reasonOf(t, err); got != daemon.ReasonListen {
		t.Errorf("reason %q, want %q (%v)", got, daemon.ReasonListen, err)
	}
	if b, err := os.ReadFile(sock); err != nil || string(b) != "not a socket" { //nolint:gosec // G304: sock is admin.sock in this test's own temp state folder
		t.Errorf("the file at admin.sock was changed: %q, %v", b, err)
	}
}
