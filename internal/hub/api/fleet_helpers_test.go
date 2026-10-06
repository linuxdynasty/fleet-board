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

	"github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1/hubv1connect"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/api"
	"github.com/linuxdynasty/fleet-board/internal/hub/ca"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
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
	dir    string
	caDER  []byte
	tel    *telemetry.Telemetry
	logs   *syncBuffer
	st     *store.Store
	client hubv1connect.FleetServiceClient
}

func localUser(name string) *typev1.Principal {
	return &typev1.Principal{
		Name: name, Role: typev1.Role_ROLE_OWNER, Kind: typev1.PrincipalKind_PRINCIPAL_KIND_LOCAL_USER,
		Id: &typev1.PrincipalId{Value: "local:" + name},
	}
}

func newFleetEnv(t *testing.T, who *typev1.Principal) *fleetEnv {
	t.Helper()
	auth, err := ca.New(fleetNow, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	e := &fleetEnv{dir: filepath.Join(t.TempDir(), "state"), caDER: auth.Cert.Raw, logs: &syncBuffer{}}
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
	fleet, err := api.NewFleet(api.FleetOptions{
		Store: e.st, CACert: e.caDER, Telemetry: e.tel, Now: func() time.Time { return fleetNow },
	})
	if err != nil {
		t.Fatal(err)
	}
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
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
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
