package gateway_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"io"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/hub/ca"
	"github.com/linuxdynasty/fleet-board/internal/hub/gateway"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
	"github.com/linuxdynasty/fleet-board/internal/store"
	"github.com/linuxdynasty/fleet-board/internal/telemetry"
)

var t0 = time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)

// wallNow is the one clock the listener tests read: the certificates' issue
// time and both TLS peers' verification time. Nothing in them calls time.Now
// directly, so the handshake tests hold on any date, not only near t0.
var wallNow = time.Now

var defaultNames = []string{"fleet-test-host", "fleet-test-host.local", "localhost", "127.0.0.1", "::1"}

// clock is a settable fake "now".
type clock struct{ ns atomic.Int64 }

func newClock(at time.Time) *clock { c := &clock{}; c.set(at); return c }
func (c *clock) set(at time.Time)  { c.ns.Store(at.UnixNano()) }
func (c *clock) now() time.Time    { return time.Unix(0, c.ns.Load()).UTC() }

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

type fixture struct {
	dir   string
	auth  *ca.Authority
	id    statedb.Identity
	store *store.Store
	tel   *telemetry.Telemetry
	logs  *lockedBuffer
}

// newFixture creates a real state.db with a CA and a server certificate
// issued at t0 for defaultNames, and opens it.
func newFixture(t *testing.T) *fixture { t.Helper(); return newFixtureAt(t, t0) }

// newFixtureAt is newFixture with the CA and server certificate issued at at.
func newFixtureAt(t *testing.T, at time.Time) *fixture {
	t.Helper()
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "state")
	if err := statedb.EnsureDir(dir); err != nil {
		t.Fatal(err)
	}
	auth, err := ca.New(at, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sc, err := auth.IssueServer(defaultNames, at, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := auth.KeyDER()
	if err != nil {
		t.Fatal(err)
	}
	id := statedb.Identity{
		FleetID: "fleet-1", CACert: auth.Cert.Raw, CAKey: keyDER,
		ServerCert: sc.CertDER, ServerKey: sc.KeyDER, ServerNames: sc.Names,
		AccountSalt: bytes.Repeat([]byte{1}, 32), SaltVersion: 1, SettingsEpoch: "epoch-1", CreatedTime: at,
	}
	if err := statedb.Create(ctx, dir, id, statedb.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	logs := &lockedBuffer{}
	tel, err := telemetry.New(telemetry.Config{Service: telemetry.Hub, Version: "test", InstanceID: "test", LogOutput: logs})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tel.Shutdown(context.Background()) })
	s, err := statedb.Open(ctx, dir, tel)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return &fixture{dir: dir, auth: auth, id: id, store: s, tel: tel, logs: logs}
}

func (f *fixture) certs(t *testing.T, names []string, now func() time.Time) *gateway.Certs {
	t.Helper()
	c, err := gateway.NewCerts(gateway.CertsOptions{
		Store: f.store, Identity: f.id, Names: names, Now: now, Telemetry: f.tel,
	})
	if err != nil {
		t.Fatalf("NewCerts: %v", err)
	}
	return c
}

func (f *fixture) stored(t *testing.T) statedb.Identity {
	t.Helper()
	id, err := statedb.ReadIdentity(context.Background(), f.store)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

var _ io.Writer = (*lockedBuffer)(nil)
