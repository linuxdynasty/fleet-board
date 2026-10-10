package conntest

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/hub/ca"
	"github.com/linuxdynasty/fleet-board/internal/hub/gateway"
	"github.com/linuxdynasty/fleet-board/internal/hub/revocation"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
	"github.com/linuxdynasty/fleet-board/internal/store"
	"github.com/linuxdynasty/fleet-board/internal/telemetry"
)

// Names are the SANs of the hub's server certificate (02j defaults with a
// test hostname); an agent dials 127.0.0.1.
var Names = []string{"fleet-test-host", "fleet-test-host.local", "localhost", "127.0.0.1", "::1"}

// Hub is a running agent listener (internal/hub/gateway) on a real state.db
// with its own fleet CA, served on 127.0.0.1.
type Hub struct {
	// Addr is host:port of the listener.
	Addr string
	// CACert is the fleet CA certificate (DER); CAHash its SHA-256, the value
	// a token carries.
	CACert []byte
	CAHash [32]byte
	// FleetID is the hub's fleet_id.
	FleetID string

	dir  string
	id   statedb.Identity
	opts Options
	tel  *telemetry.Telemetry
	logs *Buffer
	reqs atomic.Int64

	mu    sync.Mutex
	store *store.Store
	srv   *gateway.Server
	held  *Held         // the socket srv serves on; it outlives a Restart
	done  chan struct{} // closed when srv's Serve has returned
}

// Options adjust [StartHub].
type Options struct {
	// Wrap wraps the listener's handler, for example with [DropFirstReply].
	Wrap func(http.Handler) http.Handler
	// Now is the hub's clock (gateway.ServerOptions.Now): token expiry,
	// certificate issue, HelloAck.hub_time and the Hello rate limit; nil
	// means time.Now.
	Now func() time.Time
}

// StartHub starts a hub that lives until the test ends.
func StartHub(t *testing.T, opts Options) *Hub {
	t.Helper()
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "hub")
	if err := statedb.EnsureDir(dir); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	auth, err := ca.New(now, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sc, err := auth.IssueServer(Names, now, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := auth.KeyDER()
	if err != nil {
		t.Fatal(err)
	}
	id := statedb.Identity{
		FleetID: "fleet-test", CACert: auth.Cert.Raw, CAKey: keyDER,
		ServerCert: sc.CertDER, ServerKey: sc.KeyDER, ServerNames: sc.Names,
		AccountSalt: bytes.Repeat([]byte{7}, 32), SaltVersion: 1, SettingsEpoch: "epoch-1",
		HubAddresses: []string{"fleet-test-host.local:7443"}, CreatedTime: now,
	}
	if err := statedb.Create(ctx, dir, id, statedb.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	logs := &Buffer{}
	tel, err := telemetry.New(telemetry.Config{Service: telemetry.Hub, Version: "test", InstanceID: "hub-test", LogOutput: logs})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tel.Shutdown(context.Background()) })
	h := &Hub{
		CACert: auth.Cert.Raw, CAHash: sha256.Sum256(auth.Cert.Raw), FleetID: id.FleetID,
		dir: dir, id: id, opts: opts, tel: tel, logs: logs,
	}
	t.Cleanup(func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		_ = h.srv.Close()
		h.held.Close()
		h.srv.FlushAudit(context.Background())
		_ = h.store.Close()
	})
	h.serve(t, Hold(t, BindRetry(t, "127.0.0.1:0", time.Minute)))
	return h
}

// serve opens state.db and serves a new listener process on held.
func (h *Hub) serve(t *testing.T, held *Held) {
	t.Helper()
	ctx := context.Background()
	s, err := statedb.Open(ctx, h.dir, h.tel)
	if err != nil {
		t.Fatal(err)
	}
	certs, err := gateway.NewCerts(gateway.CertsOptions{Store: s, Identity: h.id, Names: Names, Telemetry: h.tel})
	if err != nil {
		_ = s.Close()
		t.Fatal(err)
	}
	list, err := revocation.Load(ctx, revocation.Options{Dir: h.dir, Store: s, Now: time.Now})
	if err != nil {
		_ = s.Close()
		t.Fatal(err)
	}
	srv, err := gateway.NewServer(gateway.ServerOptions{
		Certs: certs, Store: s, Identity: h.id, Revoked: list, Telemetry: h.tel, Now: h.opts.Now,
	})
	if err != nil {
		_ = s.Close()
		t.Fatal(err)
	}
	inner := srv.Handler
	if h.opts.Wrap != nil {
		inner = h.opts.Wrap(inner)
	}
	srv.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.reqs.Add(1)
		inner.ServeHTTP(w, r)
	})
	done := make(chan struct{})
	ln := held.View()
	go func() {
		defer close(done)
		_ = srv.ServeTLS(ln, "", "")
	}()
	h.mu.Lock()
	h.store, h.srv, h.held, h.done = s, srv, held, done
	h.mu.Unlock()
	if h.Addr == "" { // a restart keeps the address
		h.Addr = held.Addr().String()
	}
}

var requestSeq atomic.Int64

// Token stores a fresh enroll token for machine "laptop", valid until
// notAfter, and returns its text.
func (h *Hub) Token(t *testing.T, notAfter time.Time) string {
	t.Helper()
	return h.TokenForCA(t, h.CACert, notAfter)
}

// TokenForCA is [Hub.Token] with the SHA-256 of caDER inside the token, so
// a token can name another CA while its secret is stored in this hub.
func (h *Hub) TokenForCA(t *testing.T, caDER []byte, notAfter time.Time) string {
	t.Helper()
	return h.token(t, caDER, "", notAfter)
}

// TokenExpecting is [Hub.Token] with the hostname the owner expects; empty
// means the machine name, "laptop".
func (h *Hub) TokenExpecting(t *testing.T, hostname string, notAfter time.Time) string {
	t.Helper()
	return h.token(t, h.CACert, hostname, notAfter)
}

func (h *Hub) token(t *testing.T, caDER []byte, expect string, notAfter time.Time) string {
	t.Helper()
	tok, err := ca.NewToken(caDER, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	reqID := fmt.Sprintf("req-%d", requestSeq.Add(1))
	err = statedb.InsertEnrollToken(context.Background(), h.current(), statedb.EnrollToken{
		SecretHash: tok.SecretHash, MachineName: "laptop", ExpectedHostname: expect, NotAfter: notAfter.Truncate(time.Millisecond),
		Created: time.Now(), Principal: "local:test", RequestID: reqID, RequestHash: sha256.Sum256([]byte(reqID)),
	})
	if err != nil {
		t.Fatal(err)
	}
	return tok.Reveal()
}

// Requests is the number of HTTP requests the listener served, so a test
// can prove a refused agent sent nothing.
func (h *Hub) Requests() int64 { return h.reqs.Load() }

// Machines is the number of rows in machines.
func (h *Hub) Machines(t *testing.T) int {
	t.Helper()
	var n int
	h.read(t, func(tx *sql.Tx) error {
		return tx.QueryRowContext(context.Background(), `SELECT count(*) FROM machines`).Scan(&n)
	})
	return n
}

// IssuedCerts are the certificates (DER) the hub stored for replay, one per
// first Enroll (enroll_responses.cert).
func (h *Hub) IssuedCerts(t *testing.T) [][]byte {
	t.Helper()
	return column[[]byte](t, h, `SELECT cert FROM enroll_responses ORDER BY id`)
}

// InstallIDs are the install_id of each first Enroll, in order
// (enroll_responses.install_id).
func (h *Hub) InstallIDs(t *testing.T) []string {
	t.Helper()
	return column[string](t, h, `SELECT install_id FROM enroll_responses ORDER BY id`)
}

func column[T any](t *testing.T, h *Hub, query string, args ...any) []T {
	t.Helper()
	var out []T
	h.read(t, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(context.Background(), query, args...)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var v T
			if err := rows.Scan(&v); err != nil {
				return err
			}
			out = append(out, v)
		}
		return rows.Err()
	})
	return out
}

// TokenUsed reports whether the token was burned.
func (h *Hub) TokenUsed(t *testing.T, token string) bool {
	t.Helper()
	parsed, err := ca.ParseToken(token)
	if err != nil {
		t.Fatal(err)
	}
	var used sql.NullInt64
	h.read(t, func(tx *sql.Tx) error {
		return tx.QueryRowContext(context.Background(), `SELECT used_time FROM enroll_tokens WHERE secret_hash = ?`,
			parsed.SecretHash[:]).Scan(&used)
	})
	return used.Valid
}

// Events returns the hub's JSON log lines whose msg starts with prefix.
func (h *Hub) Events(t *testing.T, prefix string) []map[string]any {
	t.Helper()
	return Lines(t, h.logs.String(), prefix)
}

func (h *Hub) read(t *testing.T, fn func(*sql.Tx) error) {
	t.Helper()
	if err := h.current().Read(context.Background(), fn); err != nil {
		t.Fatal(err)
	}
}

// Lines decodes JSON log lines and keeps those whose msg starts with prefix.
func Lines(t *testing.T, logs, prefix string) []map[string]any {
	t.Helper()
	var out []map[string]any
	sc := bufio.NewScanner(strings.NewReader(logs))
	for sc.Scan() {
		var m map[string]any
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			continue // a line that is not JSON, such as a human message
		}
		if msg, _ := m["msg"].(string); strings.HasPrefix(msg, prefix) {
			out = append(out, m)
		}
	}
	return out
}

// DropFirstReply runs the first Enroll on the real handler, so the hub
// commits it, then resets the stream instead of answering: the reply is
// lost in transit.
func DropFirstReply() func(http.Handler) http.Handler { return DropFirstReplyTo("Enroll") }

// DropFirstReplyTo is [DropFirstReply] for the first call of method (for
// example "RenewCert").
func DropFirstReplyTo(method string) func(http.Handler) http.Handler {
	var dropped atomic.Bool
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !strings.HasSuffix(r.URL.Path, "/"+method) || !dropped.CompareAndSwap(false, true) {
				next.ServeHTTP(w, r)
				return
			}
			next.ServeHTTP(&discard{header: http.Header{}}, r)
			panic(http.ErrAbortHandler)
		})
	}
}

// discard is a ResponseWriter that throws the reply away.
type discard struct{ header http.Header }

func (d *discard) Header() http.Header         { return d.header }
func (d *discard) Write(b []byte) (int, error) { return len(b), nil }
func (d *discard) WriteHeader(int)             {}
func (d *discard) Flush()                      {}

// Buffer collects log lines written from several goroutines.
type Buffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *Buffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *Buffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
