package api_test

import (
	"context"
	"errors"
	"io"
	"math"
	"net"
	"net/http"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	"github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1/hubv1connect"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/api"
)

// socketPath is a fresh path under /tmp: t.TempDir on macOS is too long for
// a Unix socket path.
func socketPath(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "fba") //nolint:usetesting // t.TempDir on macOS is too long for a Unix socket path
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return filepath.Join(dir, "admin.sock")
}

// selfUID is this process's user id as the uint32 the admin socket compares.
func selfUID(t *testing.T) uint32 {
	t.Helper()
	uid := os.Getuid()
	if uid >= 0 && uint64(uid) <= math.MaxUint32 {
		return uint32(uid)
	}
	t.Fatalf("user id %d is out of range", uid)
	return 0
}

// dialUnix connects to the Unix socket at path.
func dialUnix(t *testing.T, path string) net.Conn {
	t.Helper()
	var d net.Dialer
	c, err := d.DialContext(t.Context(), "unix", path)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// serveAdmin serves the admin socket for e's store with peer as the
// peer-credential reader (nil: the kernel's).
func serveAdmin(t *testing.T, e *fleetEnv, peer api.PeerUID) string {
	t.Helper()
	path := socketPath(t)
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	fleet, err := api.NewFleet(api.FleetOptions{Store: e.st, CACert: e.caDER, Telemetry: e.tel, HubInstanceID: testInstanceID})
	if err != nil {
		t.Fatal(err)
	}
	wrapped, srv := api.NewAdmin(ln, api.AdminOptions{
		Fleet: fleet, Telemetry: e.tel, UID: selfUID(t), PeerUID: peer,
	})
	done := make(chan error, 1)
	go func() { done <- srv.Serve(wrapped) }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(ctx); err != nil {
			t.Errorf("Shutdown: %v", err)
		}
		if err := <-done; !errors.Is(err, http.ErrServerClosed) {
			t.Errorf("Serve: %v", err)
		}
	})
	return path
}

func TestTheAdminSocketCutsOffAStalledRequest(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, nil)
	path := socketPath(t)
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	fleet, err := api.NewFleet(api.FleetOptions{Store: e.st, CACert: e.caDER, Telemetry: e.tel, HubInstanceID: testInstanceID})
	if err != nil {
		t.Fatal(err)
	}
	wrapped, srv := api.NewAdmin(ln, api.AdminOptions{
		Fleet: fleet, Telemetry: e.tel, UID: selfUID(t), RequestTimeout: 200 * time.Millisecond,
	})
	go func() { _ = srv.Serve(wrapped) }()
	t.Cleanup(func() { _ = srv.Close() })
	c := dialUnix(t, path)
	defer func() { _ = c.Close() }()
	if _, err := io.WriteString(c, "POST "+hubv1connect.FleetServiceCreateEnrollmentTokenProcedure+" HTTP/1.1\r\n"+
		"Host: fleetd\r\nContent-Type: application/proto\r\nContent-Length: 100\r\n\r\nab"); err != nil {
		t.Fatal(err)
	}
	if err := c.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	_, err = io.ReadAll(c)
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		t.Fatal("the connection was still open 5s after a request stalled with a 200ms limit")
	}
}

func TestTheKernelReportsThePeerUserOfAUnixSocket(t *testing.T) {
	t.Parallel()
	path := socketPath(t)
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	client := dialUnix(t, path)
	defer func() { _ = client.Close() }()
	server, err := ln.AcceptUnix()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = server.Close() }()
	uid, err := api.KernelPeerUID(server)
	if err != nil {
		t.Fatalf("KernelPeerUID: %v", err)
	}
	if want := selfUID(t); uid != want {
		t.Errorf("peer uid %d, want %d (this process)", uid, want)
	}
}

func TestTheSameUserIsServedOnTheAdminSocket(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, nil)
	path := serveAdmin(t, e, nil)
	resp, err := api.AdminClient(path).CreateEnrollmentToken(context.Background(), withEnrollSecret(t, e, enrollReq("req-1", "laptop")))
	if err != nil {
		t.Fatalf("CreateEnrollmentToken over the socket: %v", err)
	}
	if !tokenPattern.MatchString(resp.Msg.GetToken()) {
		t.Errorf("token %q", resp.Msg.GetToken())
	}
	want := api.LocalUser(selfUID(t), nil).GetId().GetValue()
	issued := e.logs.events(t, "enroll.issued")
	if len(issued) != 1 || issued[0]["actor"] != want {
		t.Errorf("enroll.issued = %v, want actor %q", issued, want)
	}
	if !strings.HasPrefix(want, "local:") {
		t.Errorf("actor %q is not local:<os user>", want)
	}
	t.Logf("actor from the kernel uid with CGO_ENABLED=%s: %s", os.Getenv("CGO_ENABLED"), want)
}

func TestAMismatchedPeerIsClosedUnserved(t *testing.T) {
	t.Parallel()
	other := selfUID(t) + 1
	for _, tc := range []struct {
		name, cause string
		peer        api.PeerUID
	}{
		{"another user", "uid_mismatch", func(*net.UnixConn) (uint32, error) { return other, nil }},
		{"unreadable credentials", "unreadable", func(*net.UnixConn) (uint32, error) { return 0, errors.New("no creds") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newFleetEnv(t, nil)
			path := serveAdmin(t, e, tc.peer)
			_, err := api.AdminClient(path).CreateEnrollmentToken(context.Background(), withEnrollSecret(t, e, enrollReq("req-1", "laptop")))
			if err == nil {
				t.Fatal("the refused peer got a response")
			}
			var ce *connect.Error
			if errors.As(err, &ce) && len(ce.Details()) > 0 {
				t.Errorf("the refused peer got a served Connect error: %v", err)
			}
			if got := countRows(t, e); got != 0 {
				t.Errorf("%d rows stored, want 0", got)
			}
			if n := len(e.logs.events(t, "enroll.issued")) + len(e.logs.events(t, "enroll.issue_refused")); n != 0 {
				t.Errorf("the handler ran for a refused peer (%d enroll lines)", n)
			}
			refused := e.logs.events(t, "admin.peer_refused")
			if len(refused) != 1 || refused[0]["cause"] != tc.cause {
				t.Errorf("admin.peer_refused = %v, want one with cause %s", refused, tc.cause)
			}
			if got := sample(e.scrape(t), "fleet_hub_auth_failures_total", `reason="peer_uid"`); got != 1 {
				t.Errorf("fleet_hub_auth_failures_total{peer_uid} = %v, want 1", got)
			}
		})
	}
}

func TestTheAdminSocketRefusesAnyOrigin(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, nil)
	path := serveAdmin(t, e, nil)
	for _, origin := range []string{"http://localhost", "null", ""} {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, "http://fleetd"+hubv1connectCreate, strings.NewReader(`{"requestId":"r","machineName":"m"}`))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header["Origin"] = []string{origin}
		resp, err := api.AdminHTTPClient(path).Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != http.StatusForbidden || reason(t, body) != typev1.ReasonCode_REASON_CODE_ORIGIN_NOT_ALLOWED {
			t.Errorf("Origin %q: status %d, reason %v; want 403 ORIGIN_NOT_ALLOWED", origin, resp.StatusCode, reason(t, body))
		}
	}
	if got := countRows(t, e); got != 0 {
		t.Errorf("%d rows stored, want 0", got)
	}
	if got := sample(e.scrape(t), "fleet_hub_auth_failures_total", `reason="origin"`); got != 3 {
		t.Errorf("fleet_hub_auth_failures_total{origin} = %v, want 3", got)
	}
}

const hubv1connectCreate = "/fleet.hub.v1.FleetService/CreateEnrollmentToken"

func TestOtherMethodsOnTheAdminSocketAreUnimplemented(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, nil)
	c := api.AdminClient(serveAdmin(t, e, nil))
	_, err := c.ListSessions(context.Background(), connect.NewRequest(&hubv1.ListSessionsRequest{}))
	if code := connect.CodeOf(err); code != connect.CodeUnimplemented {
		t.Errorf("ListSessions: %v, want unimplemented", code)
	}
	_, err = c.GetSystemStatus(context.Background(), connect.NewRequest(&hubv1.GetSystemStatusRequest{}))
	if code := connect.CodeOf(err); code != connect.CodeUnimplemented {
		t.Errorf("GetSystemStatus: %v, want unimplemented", code)
	}
}

func TestLocalUserNamesTheOSUserOrFallsBackToTheUID(t *testing.T) {
	t.Parallel()
	named := func(name string) func(string) (*user.User, error) {
		return func(string) (*user.User, error) { return &user.User{Username: name}, nil }
	}
	failing := func(string) (*user.User, error) { return nil, user.UnknownUserIdError(501) }
	for _, tc := range []struct {
		name   string
		lookup func(string) (*user.User, error)
		want   string
	}{
		{"found", named("alice"), "local:alice"},
		{"lookup fails", failing, "local:uid-501"},
		{"name with a space", named("Alice Smith"), "local:uid-501"},
		{"empty name", named(""), "local:uid-501"},
		{"name too long for a PrincipalId", named(strings.Repeat("a", 123)), "local:uid-501"},
	} {
		p := api.LocalUser(501, tc.lookup)
		if p.GetId().GetValue() != tc.want || p.GetKind() != typev1.PrincipalKind_PRINCIPAL_KIND_LOCAL_USER {
			t.Errorf("%s: %v %v, want %s LOCAL_USER", tc.name, p.GetId().GetValue(), p.GetKind(), tc.want)
		}
	}
	real := api.LocalUser(selfUID(t), nil).GetId().GetValue()
	if want := "local:uid-" + strconv.Itoa(os.Getuid()); real != want {
		if u, err := user.Current(); err != nil || real != "local:"+u.Username {
			t.Errorf("real lookup gave %q, want local:<current user> or %q", real, want)
		}
	}
}
