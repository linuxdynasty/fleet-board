package daemon_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/api"
	"github.com/linuxdynasty/fleet-board/internal/hub/config"
	"github.com/linuxdynasty/fleet-board/internal/hub/daemon"
	"github.com/linuxdynasty/fleet-board/internal/hub/secret"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

// enrollSecret attaches the hub's local approval secret to an enrollment
// request: CreateEnrollmentToken needs the secret in local_trusted mode
// (ADR 0032). The secret lives next to the admin socket in the state dir.
func enrollSecret(t *testing.T, sock string,
	req *connect.Request[hubv1.CreateEnrollmentTokenRequest],
) (*hubv1.CreateEnrollmentTokenResponse, error) {
	t.Helper()
	shown, err := secret.Reveal(filepath.Dir(sock))
	if err != nil {
		return nil, err
	}
	req.Header().Set(api.SecretHeader, shown)
	resp, err := api.AdminClient(sock).CreateEnrollmentToken(context.Background(), req)
	if err != nil {
		return nil, err
	}
	return resp.Msg, nil
}

func enroll(t *testing.T, sock, requestID string) (*hubv1.CreateEnrollmentTokenResponse, error) {
	t.Helper()
	req := connect.NewRequest(&hubv1.CreateEnrollmentTokenRequest{RequestId: requestID, MachineName: "laptop"})
	return enrollSecret(t, sock, req)
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

// The running hub answers GetServerInfo on the admin socket with its own
// identity: the fleet_id from hub_identity and the agent listener's
// hub_instance_id (one bindAdmin call wires both, and an empty id would
// have refused the start). Two reads in one run agree.
func TestTheAdminSocketServesServerInfo(t *testing.T) {
	t.Parallel()
	h := newHub(t, "")
	h.init(t)
	id := h.storedIdentity(t)
	r := h.start(t, h.options())
	sock := config.AdminSocketPath(h.state)
	first, err := api.AdminClient(sock).GetServerInfo(context.Background(), connect.NewRequest(&hubv1.GetServerInfoRequest{}))
	if err != nil {
		t.Fatalf("GetServerInfo: %v", err)
	}
	msg := first.Msg
	if msg.GetFleetId() != id.FleetID {
		t.Errorf("fleet_id %q, want the hub_identity row %q", msg.GetFleetId(), id.FleetID)
	}
	if msg.GetHubInstanceId() == "" {
		t.Error("hub_instance_id is empty, want the agent listener's id")
	}
	if msg.GetApiVersion() != 1 {
		t.Errorf("api_version %d, want 1", msg.GetApiVersion())
	}
	if msg.GetAuthMode() != hubv1.AuthMode_AUTH_MODE_LOCAL_TRUSTED {
		t.Errorf("auth_mode %s, want LOCAL_TRUSTED", msg.GetAuthMode())
	}
	if !strings.HasPrefix(msg.GetCaller().GetId().GetValue(), "local:") {
		t.Errorf("caller %v, want the local socket peer", msg.GetCaller())
	}
	second, err := api.AdminClient(sock).GetServerInfo(context.Background(), connect.NewRequest(&hubv1.GetServerInfoRequest{}))
	if err != nil {
		t.Fatalf("second GetServerInfo: %v", err)
	}
	if second.Msg.GetHubInstanceId() != msg.GetHubInstanceId() {
		t.Error("hub_instance_id changed within one run")
	}
	r.stop(t)
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

// enrollSocketMachine enrolls one machine into the hub's store before it
// starts, so the admin socket test below reads a real row over the real
// socket.
func enrollSocketMachine(t *testing.T, state string) {
	t.Helper()
	now := time.Now().Truncate(time.Millisecond)
	secret := sha256.Sum256([]byte{7, 1, 2, 3})
	s, err := statedb.Open(context.Background(), state, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	tok := statedb.EnrollToken{
		SecretHash: secret, MachineName: "laptop", ExpectedHostname: "laptop.lan",
		Labels: []string{"gpu"}, NotAfter: now.Add(24 * time.Hour), Created: now,
		Principal: "local:root", RequestID: "req-sock-1",
		RequestHash: sha256.Sum256([]byte{7}),
	}
	if err := statedb.InsertEnrollToken(context.Background(), s, tok); err != nil {
		t.Fatal(err)
	}
	if _, err := statedb.Enroll(context.Background(), s, statedb.NewMachine{
		Key: statedb.EnrollKey{
			SecretHash: secret, InstallID: "install-sock-1",
			CSRKeyHash: sha256.Sum256([]byte{7, 4}),
		},
		MachineID: "m-sock-1", CertDER: []byte("cert-sock-1"), Serial: "aa07",
		CertNotAfter: now.Add(90 * 24 * time.Hour), ReportedHostname: "laptop.lan",
		Facts: `{"hostname":"laptop.lan","os":"linux","username":"ada"}`, Now: now,
	}); err != nil {
		t.Fatal(err)
	}
}

// The running hub serves the machine reads on the admin socket: the row
// enrolled before start lists and reads back, and an unknown id is NOT_FOUND.
func TestTheAdminSocketServesMachines(t *testing.T) {
	t.Parallel()
	h := newHub(t, "")
	h.init(t)
	enrollSocketMachine(t, h.state)
	h.start(t, h.options())
	sock := config.AdminSocketPath(h.state)
	client := api.AdminClient(sock)

	list, err := client.ListMachines(context.Background(), connect.NewRequest(&hubv1.ListMachinesRequest{}))
	if err != nil {
		t.Fatalf("ListMachines: %v", err)
	}
	if len(list.Msg.GetMachines()) != 1 {
		t.Fatalf("machines %d, want the 1 enrolled row", len(list.Msg.GetMachines()))
	}
	got := list.Msg.GetMachines()[0]
	if got.GetId().GetValue() != "m-sock-1" || got.GetSpec().GetDisplayName() != "laptop" {
		t.Errorf("machine %v, want m-sock-1/laptop", got.GetId())
	}

	read, err := client.GetMachine(context.Background(), connect.NewRequest(
		&hubv1.GetMachineRequest{Id: &typev1.MachineId{Value: "m-sock-1"}}))
	if err != nil {
		t.Fatalf("GetMachine: %v", err)
	}
	if read.Msg.GetMachine().GetStatus().GetHostname() != "laptop.lan" {
		t.Errorf("hostname %q, want laptop.lan", read.Msg.GetMachine().GetStatus().GetHostname())
	}

	if _, err := client.GetMachine(context.Background(), connect.NewRequest(
		&hubv1.GetMachineRequest{Id: &typev1.MachineId{Value: "m-ghost"}})); err == nil {
		t.Error("GetMachine for an unknown id succeeded, want NOT_FOUND")
	} else {
		var ce *connect.Error
		if !errors.As(err, &ce) || ce.Code() != connect.CodeNotFound {
			t.Errorf("unknown id: %v, want NOT_FOUND", err)
		}
	}
}
