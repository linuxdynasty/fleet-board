package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/hub/setupgithub"
)

// An org-owned App is created under the organization endpoint, not the
// personal one; the default stays personal.
func TestSetupPageOrgEndpoint(t *testing.T) {
	t.Parallel()
	m := setupgithub.Manifest("fleet-board", "http://127.0.0.1:9/", "http://127.0.0.1:9/callback")
	personal, err := setupPage("https://github.com", "abcstate", m, "")
	if err != nil {
		t.Fatalf("setupPage personal: %v", err)
	}
	if !strings.Contains(personal, "https://github.com/settings/apps/new?state=abcstate") {
		t.Errorf("personal page lacks the personal endpoint:\n%s", personal)
	}
	org, err := setupPage("https://github.com/", "abcstate", m, "acme")
	if err != nil {
		t.Fatalf("setupPage org: %v", err)
	}
	if !strings.Contains(org, "https://github.com/organizations/acme/settings/apps/new?state=abcstate") {
		t.Errorf("org page lacks the organization endpoint:\n%s", org)
	}
	if _, err := setupPage("https://github.com", "abcstate", m, "a/b"); err == nil {
		t.Error("organization 'a/b' accepted, want a refusal")
	}
}

// The callback handler must finish the confirmation page before telling
// the waiter it arrived: otherwise the waiter closes the server while
// the browser is still reading.
func TestCallbackHandlerSignalsAfterWrite(t *testing.T) {
	t.Parallel()
	got := make(chan setupCallback, 1)
	release := make(chan struct{})
	w := &blockingWriter{header: http.Header{}, release: release}
	done := make(chan struct{})
	go func() {
		defer close(done)
		callbackHandler(got, "<connected>")(w, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/callback?code=c&state=s", nil))
	}()
	select {
	case <-got:
		t.Fatal("handler signaled before the response was written")
	case <-time.After(200 * time.Millisecond):
	}
	close(release)
	select {
	case cb := <-got:
		if cb.code != "c" || cb.state != "s" {
			t.Errorf("callback = %+v, want code c state s", cb)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("handler never signaled after the write completed")
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("handler never returned")
	}
	if w.buf.String() != "<connected>" {
		t.Errorf("body = %q, want the full confirmation page", w.buf.String())
	}
}

type blockingWriter struct {
	header  http.Header
	buf     bytes.Buffer
	release chan struct{}
}

func (b *blockingWriter) Header() http.Header { return b.header }
func (b *blockingWriter) Write(p []byte) (int, error) {
	<-b.release
	return b.buf.Write(p)
}
func (b *blockingWriter) WriteHeader(int) {}
func (b *blockingWriter) Flush()          {}

// A re-run with --installation-id loads the saved App and only lists,
// selects and saves the installation: no browser, no new exchange.
func TestRunSetupGithubRerunLoadsSavedApp(t *testing.T) {
	t.Parallel()
	_, cfgPath := rerunConfig(t)
	var conversions int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost:
			conversions++
			w.WriteHeader(http.StatusInternalServerError)
		case r.URL.Path == "/app/installations":
			_, _ = io.WriteString(w, `[{"id": 77, "account": {"login": "acme"}}, {"id": 78, "account": {"login": "acme-ops"}}]`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	var stdout, stderr bytes.Buffer
	req := setupRequest{
		ConfigPath: cfgPath, AppName: "fleet-board", APIBase: srv.URL,
		WebBase: srv.URL, InstallationID: 77, Timeout: time.Minute,
	}
	if err := runSetupGithub(context.Background(), req, &stdout, &stderr); err != nil {
		t.Fatalf("rerun: %v", err)
	}
	if conversions != 0 {
		t.Errorf("rerun exchanged %d new codes, want none", conversions)
	}
	if !strings.Contains(stdout.String(), "restart fleetd to apply") {
		t.Errorf("stdout %q lacks the restart line", stdout.String())
	}
	raw, err := os.ReadFile(cfgPath) //nolint:gosec // G304: the path is the test's own temp config
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "installation_id: 77") {
		t.Errorf("config lacks installation_id 77:\n%s", raw)
	}
}

// A re-run without the flag and exactly one installation picks it.
func TestRunSetupGithubRerunPicksSingleInstallation(t *testing.T) {
	t.Parallel()
	_, cfgPath := rerunConfig(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/app/installations" {
			_, _ = io.WriteString(w, `[{"id": 77, "account": {"login": "acme"}}]`)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	var stdout, stderr bytes.Buffer
	req := setupRequest{
		ConfigPath: cfgPath, AppName: "fleet-board", APIBase: srv.URL,
		WebBase: srv.URL, Timeout: 3 * time.Second,
	}
	if err := runSetupGithub(context.Background(), req, &stdout, &stderr); err != nil {
		t.Fatalf("rerun: %v", err)
	}
	raw, err := os.ReadFile(cfgPath) //nolint:gosec // G304: the path is the test's own temp config
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "installation_id: 77") {
		t.Errorf("config lacks installation_id 77:\n%s", raw)
	}
}

// rerunConfig seeds a saved App: secret files plus the github config keys.
func rerunConfig(t *testing.T) (dir, cfgPath string) {
	t.Helper()
	var err error
	dir, err = os.MkdirTemp("/tmp", "rerun") //nolint:usetesting // t.TempDir breaks the socket-path check
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	cfgPath = filepath.Join(dir, "fleetd.yaml")
	base := "state_dir: " + filepath.Join(dir, "state") + "\nlisten: {agent: \"127.0.0.1:0\", api: \"127.0.0.1:0\"}\ndiscovery: {mdns: false}\n"
	if err := os.WriteFile(cfgPath, []byte(base), 0o600); err != nil {
		t.Fatal(err)
	}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	pemBody := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	pemPath, secretPath, err := setupgithub.WriteSecrets(dir, string(pemBody), "s")
	if err != nil {
		t.Fatal(err)
	}
	if err := setupgithub.UpdateConfig(cfgPath, 4321, pemPath, "Iv1.t", secretPath, 0); err != nil {
		t.Fatal(err)
	}
	return dir, cfgPath
}
