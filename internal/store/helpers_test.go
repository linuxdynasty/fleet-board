package store_test

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/linuxdynasty/fleet-board/internal/store"
	"github.com/linuxdynasty/fleet-board/internal/telemetry"
	_ "modernc.org/sqlite"
)

// itemsV1 is the schema the tests write to; v holds the submission order.
var itemsV1 = store.Migration{
	Version: 1,
	Name:    "items",
	SQL:     "CREATE TABLE items(id INTEGER PRIMARY KEY, v TEXT NOT NULL UNIQUE);",
}

var itemsV2 = store.Migration{
	Version: 2,
	Name:    "items note",
	SQL:     "ALTER TABLE items ADD COLUMN note TEXT; CREATE INDEX items_note ON items(note);",
}

type harness struct {
	tel  *telemetry.Telemetry
	logs *lockedBuffer
	path string
}

// lockedBuffer collects log lines written from the writer goroutine.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

// events returns the decoded log lines whose msg is name.
func (b *lockedBuffer) events(t *testing.T, name string) []map[string]any {
	t.Helper()
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []map[string]any
	dec := json.NewDecoder(bytes.NewReader(b.buf.Bytes()))
	for dec.More() {
		var m map[string]any
		if err := dec.Decode(&m); err != nil {
			t.Fatalf("log line is not JSON: %v", err)
		}
		if m["msg"] == name {
			out = append(out, m)
		}
	}
	return out
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	logs := &lockedBuffer{}
	tel, err := telemetry.New(telemetry.Config{Service: telemetry.Hub, Version: "test", InstanceID: "test", LogOutput: logs})
	if err != nil {
		t.Fatalf("telemetry.New: %v", err)
	}
	t.Cleanup(func() { _ = tel.Shutdown(context.Background()) })
	return &harness{tel: tel, logs: logs, path: filepath.Join(t.TempDir(), "state.db")}
}

func (h *harness) options(migrations ...store.Migration) store.Options {
	return store.Options{Path: h.path, Name: "state", Migrations: migrations, Telemetry: h.tel}
}

func (h *harness) open(t *testing.T, opts store.Options) *store.Store {
	t.Helper()
	s, err := store.Open(context.Background(), opts)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// scrape reads /metrics as a loopback peer.
func (h *harness) scrape(t *testing.T) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.RemoteAddr = "127.0.0.1:40000"
	rec := httptest.NewRecorder()
	h.tel.MetricsHandler(nil).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("scrape status %d", rec.Code)
	}
	return rec.Body.String()
}

// sample returns the value of the first series of name whose line contains
// every want, or -1 when there is none.
func sample(body, name string, want ...string) float64 {
	sc := bufio.NewScanner(strings.NewReader(body))
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, name+"{") && !strings.HasPrefix(line, name+" ") {
			continue
		}
		match := true
		for _, w := range want {
			if !strings.Contains(line, w) {
				match = false
				break
			}
		}
		if !match {
			continue
		}
		fields := strings.Fields(line)
		v, err := strconv.ParseFloat(fields[len(fields)-1], 64)
		if err != nil {
			return -1
		}
		return v
	}
	return -1
}

func insert(v string) store.ApplyFunc {
	return func(tx *sql.Tx) error {
		_, err := tx.Exec("INSERT INTO items(v) VALUES (?)", v)
		return err
	}
}

// rawValues opens the file with a separate connection that is not the
// store's and returns items.v in insertion order.
func rawValues(t *testing.T, path string) []string {
	t.Helper()
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatalf("raw open: %v", err)
	}
	defer func() { _ = db.Close() }()
	rows, err := db.Query("SELECT v FROM items ORDER BY id")
	if err != nil {
		t.Fatalf("raw query: %v", err)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			t.Fatalf("raw scan: %v", err)
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("raw rows: %v", err)
	}
	return out
}

func rawQueryString(t *testing.T, path, query string) string {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("raw open: %v", err)
	}
	defer func() { _ = db.Close() }()
	var v string
	if err := db.QueryRow(query).Scan(&v); err != nil {
		t.Fatalf("raw %q: %v", query, err)
	}
	return v
}
