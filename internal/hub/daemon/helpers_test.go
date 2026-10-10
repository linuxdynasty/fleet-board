package daemon_test

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/hub/daemon"
	"github.com/linuxdynasty/fleet-board/internal/hub/discovery"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
	"github.com/linuxdynasty/fleet-board/internal/telemetry"
)

const testHost = "fleet-test-host.local"

// serviceTarget is the mDNS SRV target, in the server certificate's names
// whatever tls.agent_names says.
const serviceTarget = "fleetd-fleet-test-host.local"

var defaultNames = []string{"fleet-test-host", "fleet-test-host.local", "localhost", "127.0.0.1", "::1", serviceTarget}

// shortDir is a temp folder under /tmp: t.TempDir on macOS can be too long
// for the admin socket path that config validation checks.
func shortDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "fbd") //nolint:usetesting // t.TempDir on macOS can be too long for the admin socket path
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
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

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// events returns the log lines whose msg is name.
func (b *lockedBuffer) events(t *testing.T, name string) []map[string]any {
	t.Helper()
	var out []map[string]any
	sc := bufio.NewScanner(strings.NewReader(b.String()))
	for sc.Scan() {
		var m map[string]any
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			t.Fatalf("log line %q is not JSON: %v", sc.Text(), err)
		}
		if m["msg"] == name {
			out = append(out, m)
		}
	}
	return out
}

type hub struct {
	root, config, state string
	tel                 *telemetry.Telemetry
	logs                *lockedBuffer
	// resp stands in for the multicast sockets: no test sends multicast.
	resp *fakeResponder
}

// newHub writes fleetd.yaml with a short state_dir, loopback ports chosen by
// the OS, and extra YAML lines appended.
func newHub(t *testing.T, extra string) *hub {
	t.Helper()
	root := shortDir(t)
	h := &hub{root: root, config: filepath.Join(root, "fleetd.yaml"), state: filepath.Join(root, "state")}
	h.writeConfig(t, extra)
	h.logs = &lockedBuffer{}
	h.resp = &fakeResponder{}
	tel, err := telemetry.New(telemetry.Config{Service: telemetry.Hub, Version: "test", InstanceID: "test", LogOutput: h.logs})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tel.Shutdown(context.Background()) })
	h.tel = tel
	return h
}

func (h *hub) writeConfig(t *testing.T, extra string) {
	t.Helper()
	body := "state_dir: " + h.state + "\nlisten: {agent: \"0.0.0.0:0\", api: \"127.0.0.1:0\"}\n" + extra
	if err := os.WriteFile(h.config, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func (h *hub) options() daemon.Options {
	return daemon.Options{
		ConfigPath: h.config,
		Hostname:   func() (string, error) { return testHost, nil },
		Telemetry:  h.tel,
		Version:    "test",
		Discovery: discovery.Env{
			Responder: h.resp,
			Ifaces: func() ([]discovery.Iface, error) {
				return []discovery.Iface{{Name: "en0", Flags: net.FlagUp | net.FlagMulticast, Addrs: []netip.Addr{netip.MustParseAddr("192.168.1.5")}}}, nil
			},
			Route: func() discovery.Route { return discovery.Route{} },
		},
	}
}

func (h *hub) init(t *testing.T) {
	t.Helper()
	if err := daemon.Init(context.Background(), h.options()); err != nil {
		t.Fatalf("Init: %v", err)
	}
}

// running is one daemon.Run in a goroutine.
type running struct {
	addrs  daemon.Addrs
	cancel context.CancelFunc
	done   chan error
	once   sync.Once
}

func (h *hub) start(t *testing.T, opts daemon.Options) *running {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan daemon.Addrs, 1)
	opts.Ready = func(a daemon.Addrs) { ready <- a }
	r := &running{cancel: cancel, done: make(chan error, 1)}
	go func() { r.done <- daemon.Run(ctx, opts) }()
	select {
	case r.addrs = <-ready:
	case err := <-r.done:
		cancel()
		t.Fatalf("Run stopped before it was ready: %v", err)
	case <-time.After(20 * time.Second):
		cancel()
		t.Fatal("Run was not ready within 20 s")
	}
	t.Cleanup(func() { r.stop(t) })
	return r
}

func (r *running) stop(t *testing.T) {
	t.Helper()
	r.once.Do(func() {
		r.cancel()
		select {
		case err := <-r.done:
			if err != nil {
				t.Errorf("Run returned %v after cancel, want nil", err)
			}
		case <-time.After(20 * time.Second):
			t.Error("Run did not stop within 20 s of cancel")
		}
	})
}

func reasonOf(t *testing.T, err error) daemon.Reason {
	t.Helper()
	var se *daemon.StartError
	if !errors.As(err, &se) {
		t.Fatalf("error %v is not a *daemon.StartError", err)
	}
	return se.Reason
}

func (h *hub) scrape(t *testing.T) string {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/metrics", nil)
	req.RemoteAddr = "127.0.0.1:1"
	rec := httptest.NewRecorder()
	h.tel.MetricsHandler(nil).ServeHTTP(rec, req)
	return rec.Body.String()
}

func sample(body, name string, want ...string) float64 {
	sc := bufio.NewScanner(strings.NewReader(body))
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, name+"{") {
			continue
		}
		match := true
		for _, w := range want {
			match = match && strings.Contains(line, w)
		}
		if match {
			f := strings.Fields(line)
			v, err := strconv.ParseFloat(f[len(f)-1], 64)
			if err != nil {
				return -1
			}
			return v
		}
	}
	return -1
}

// storedIdentity opens state.db after the hub stopped and reads the row.
func (h *hub) storedIdentity(t *testing.T) statedb.Identity {
	t.Helper()
	s, err := statedb.Open(context.Background(), h.state, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	id, err := statedb.ReadIdentity(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func caPool(t *testing.T, id statedb.Identity) *x509.CertPool {
	t.Helper()
	c, err := x509.ParseCertificate(id.CACert)
	if err != nil {
		t.Fatal(err)
	}
	p := x509.NewCertPool()
	p.AddCert(c)
	return p
}

// dialAgent opens TLS to addr trusting only the stored CA, checking serverName.
func dialAgent(t *testing.T, pool *x509.CertPool, addr, serverName string, maxVersion uint16) (*tls.ConnectionState, error) {
	t.Helper()
	d := tls.Dialer{Config: &tls.Config{RootCAs: pool, ServerName: serverName, MinVersion: tls.VersionTLS12, MaxVersion: maxVersion}}
	c, err := d.DialContext(t.Context(), "tcp", addr)
	if err != nil {
		return nil, err
	}
	defer func() { _ = c.Close() }()
	conn, ok := c.(*tls.Conn)
	if !ok {
		return nil, fmt.Errorf("tls.Dialer returned %T", c)
	}
	st := conn.ConnectionState()
	return &st, nil
}
