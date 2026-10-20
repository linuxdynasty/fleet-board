package api_test

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	"github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1/hubv1connect"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/api"
	"github.com/linuxdynasty/fleet-board/internal/hub/ca"
	"github.com/linuxdynasty/fleet-board/internal/hub/outbox"
	"github.com/linuxdynasty/fleet-board/internal/hub/secret"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
	"github.com/linuxdynasty/fleet-board/internal/hub/system"
	"github.com/linuxdynasty/fleet-board/internal/store"
	"github.com/linuxdynasty/fleet-board/internal/telemetry"
)

var fleetNow = time.Date(2026, 10, 6, 4, 0, 0, 0, time.UTC)

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// events returns the JSON log lines whose msg is name.
func (b *syncBuffer) events(t *testing.T, name string) []map[string]any {
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

// fleetEnv is a FleetService on a real state.db, reached over HTTP with the
// principal a test chose.
type fleetEnv struct {
	dir     string
	caDER   []byte
	tel     *telemetry.Telemetry
	logs    *syncBuffer
	st      *store.Store
	fleet   *api.Fleet
	client  hubv1connect.FleetServiceClient
	sysLoop *system.Loop
}

func localUser(name string) *typev1.Principal {
	return &typev1.Principal{
		Name: name, Role: typev1.Role_ROLE_OWNER, Kind: typev1.PrincipalKind_PRINCIPAL_KIND_LOCAL_USER,
		Id: &typev1.PrincipalId{Value: "local:" + name},
	}
}

// envOpt tunes the test FleetService. Zero value is local_trusted with the
// state dir's secret file absent (DecideApproval fails closed until the
// test creates it).
type envOpt struct {
	now          func() time.Time
	localTrusted *bool
	sysSources   *system.Sources
	onQueued     func(outbox.Op)
	instanceID   string
	watchLogCap  int
	checkpoint   time.Duration
}

// withInstanceID runs the test hub under another hub_instance_id, so a
// resume token from elsewhere reads HUB_RESTARTED.
func withInstanceID(id string) envOpt { return envOpt{instanceID: id} }

// withWatchLogCap bounds the Watch change log, evicting old tokens fast.
func withWatchLogCap(cap int) envOpt { return envOpt{watchLogCap: cap} }

// withNow swaps the test hub clock for a mutable one, so a test can age
// entries past the change-log floor without waiting it out.
func withNow(now func() time.Time) envOpt { return envOpt{now: now} }

// withWatchCheckpoint shortens the idle-stream CHECKPOINT interval, so a
// test reads checkpoints without waiting out the production interval.
func withWatchCheckpoint(d time.Duration) envOpt { return envOpt{checkpoint: d} }

func withCommandQueued(fn func(outbox.Op)) envOpt { return envOpt{onQueued: fn} }

// withSystemSources evaluates the hub signals from fake readings instead
// of serving them UNKNOWN: the built loop lands on e.sysLoop for the test
// to tick, with item writes against the env's own store.
func withSystemSources(src system.Sources) envOpt {
	return envOpt{sysSources: &src}
}

// trusted toggles local_trusted mode for the test hub.
func trusted(on bool) envOpt {
	return envOpt{localTrusted: &on}
}

// secretMu serializes test secret creation: Ensure writes the file after
// creating it, so two parallel tests must not Ensure at once (production
// holds the fleetd lock instead).
var secretMu sync.Mutex

// withEnrollSecret ensures the approval secret and sets its header on an
// enroll request: CreateEnrollmentToken carries the ADMIN scope, so it
// needs the secret in local_trusted mode (ADR 0032).
func withEnrollSecret(t *testing.T, e *fleetEnv, req *connect.Request[hubv1.CreateEnrollmentTokenRequest],
) *connect.Request[hubv1.CreateEnrollmentTokenRequest] {
	t.Helper()
	secretMu.Lock()
	defer secretMu.Unlock()
	if _, err := secret.Ensure(e.dir, rand.Read); err != nil {
		t.Fatal(err)
	}
	shown, err := secret.Reveal(e.dir)
	if err != nil {
		t.Fatal(err)
	}
	req.Header().Set(api.SecretHeader, shown)
	return req
}

func newFleetEnv(t *testing.T, who *typev1.Principal, opts ...envOpt) *fleetEnv {
	t.Helper()
	auth, err := ca.New(fleetNow, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	e := &fleetEnv{dir: filepath.Join(t.TempDir(), "state"), caDER: auth.Cert.Raw, logs: &syncBuffer{}}
	now := func() time.Time { return fleetNow }
	local := true
	instanceID := testInstanceID
	var sysSources *system.Sources
	var onQueued func(outbox.Op)
	watchLogCap := 0
	var checkpoint time.Duration
	for _, o := range opts {
		if o.now != nil {
			now = o.now
		}
		if o.localTrusted != nil {
			local = *o.localTrusted
		}
		if o.sysSources != nil {
			sysSources = o.sysSources
		}
		if o.onQueued != nil {
			onQueued = o.onQueued
		}
		if o.instanceID != "" {
			instanceID = o.instanceID
		}
		if o.watchLogCap != 0 {
			watchLogCap = o.watchLogCap
		}
		if o.checkpoint != 0 {
			checkpoint = o.checkpoint
		}
	}
	if err := statedb.EnsureDir(e.dir); err != nil {
		t.Fatal(err)
	}
	id := statedb.Identity{
		FleetID: "fleet", CACert: auth.Cert.Raw, CAKey: []byte("k"), ServerCert: []byte("c"), ServerKey: []byte("k"),
		AccountSalt: make([]byte, 32), SaltVersion: 1, SettingsEpoch: "e", CreatedTime: fleetNow,
	}
	if err := statedb.Create(context.Background(), e.dir, id, statedb.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	e.tel, err = telemetry.New(telemetry.Config{Service: telemetry.Hub, Version: "test", InstanceID: "test", LogOutput: e.logs})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.tel.Shutdown(context.Background()) })
	e.st, err = statedb.Open(context.Background(), e.dir, e.tel)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.st.Close() })
	var sysLoop *system.Loop
	if sysSources != nil {
		sysLoop = system.NewLoop(*sysSources,
			func(ctx context.Context) ([]system.OpenItem, error) {
				return statedb.ListOpenSystemItems(ctx, e.st)
			},
			func(ctx context.Context, changes []system.Change, at time.Time) error {
				return statedb.ApplySystemChanges(ctx, e.st, changes, at)
			},
			e.tel.Tracer("test"))
	}
	e.sysLoop = sysLoop
	fleet, err := api.NewFleet(api.FleetOptions{
		Store: e.st, CACert: e.caDER, Telemetry: e.tel, Now: now,
		StateDir: e.dir, LocalTrusted: local, HubInstanceID: instanceID,
		SystemLoop: sysLoop, OnCommandQueued: onQueued, WatchLogCap: watchLogCap,
		WatchCheckpoint: checkpoint,
	})
	if err != nil {
		t.Fatal(err)
	}
	e.fleet = fleet
	path, h := fleet.Handler()
	mux := http.NewServeMux()
	mux.Handle(path, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if who != nil {
			r = r.WithContext(api.WithPrincipal(r.Context(), who))
		}
		h.ServeHTTP(w, r)
	}))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	e.client = hubv1connect.NewFleetServiceClient(srv.Client(), srv.URL)
	return e
}

func (e *fleetEnv) scrape(t *testing.T) string {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/metrics", nil)
	req.RemoteAddr = "127.0.0.1:1"
	rec := httptest.NewRecorder()
	e.tel.MetricsHandler(nil).ServeHTTP(rec, req)
	return rec.Body.String()
}

// detail is the code, the ErrorDetail reason and the violated fields of err.
func detail(t *testing.T, err error) (connect.Code, typev1.ReasonCode, []string) {
	t.Helper()
	var ce *connect.Error
	if !errors.As(err, &ce) {
		t.Fatalf("error %v is not a *connect.Error", err)
	}
	var fields []string
	var reason typev1.ReasonCode
	for _, d := range ce.Details() {
		msg, derr := d.Value()
		if derr != nil {
			t.Fatal(derr)
		}
		if ed, ok := msg.(*typev1.ErrorDetail); ok {
			reason = ed.GetReason()
			for _, v := range ed.GetViolations() {
				fields = append(fields, v.GetField())
			}
		}
	}
	return ce.Code(), reason, fields
}
