package setupgithub_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/linuxdynasty/fleet-board/internal/hub/setupgithub"
)

// The manifest must carry the read-only repository permission set of
// ADR 0022 and 02j: no webhook, no writes, no account permissions.
func TestManifestCarriesReadOnlyRepoPermissions(t *testing.T) {
	t.Parallel()
	m := setupgithub.Manifest("fleet-board", "http://127.0.0.1:9/", "http://127.0.0.1:9/callback")
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("unmarshal manifest: %v", err)
	}
	if doc["public"] != false {
		t.Errorf("public = %v, want false", doc["public"])
	}
	if ev, ok := doc["default_events"].([]any); !ok || len(ev) != 0 {
		t.Errorf("default_events = %v, want empty", doc["default_events"])
	}
	perms, ok := doc["default_permissions"].(map[string]any)
	if !ok {
		t.Fatalf("default_permissions = %v, want a map", doc["default_permissions"])
	}
	for _, key := range []string{"metadata", "issues", "pull_requests", "checks", "statuses", "contents"} {
		if perms[key] != "read" {
			t.Errorf("permission %s = %v, want read", key, perms[key])
		}
	}
	if len(perms) != 6 {
		t.Errorf("permissions = %v, want exactly the six read-only repo keys", perms)
	}
}

func TestCheckStateRefusesAWrongValue(t *testing.T) {
	t.Parallel()
	if err := setupgithub.CheckState("abc", "abc"); err != nil {
		t.Errorf("matching state refused: %v", err)
	}
	if err := setupgithub.CheckState("wrong", "abc"); err == nil {
		t.Error("wrong state accepted, want a refusal")
	}
}

// A used (or unknown) conversion code must surface as a CodeError and
// write nothing: the caller only writes files after Exchange succeeds.
func TestExchangeUsedCodeFailsWithoutWriting(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/app-manifests/used-code/conversions" {
			t.Errorf("path = %s, want the conversion path", r.URL.Path)
		}
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message": "code already used"}`))
	}))
	defer srv.Close()

	_, err := setupgithub.Exchange(context.Background(), srv.Client(), srv.URL, "used-code")
	if err == nil {
		t.Fatal("used code accepted, want a CodeError")
	}
	var codeErr *setupgithub.CodeError
	if !errors.As(err, &codeErr) {
		t.Fatalf("error type = %T, want *CodeError", err)
	}
	entries, readdir := os.ReadDir(dir)
	if readdir != nil {
		t.Fatal(readdir)
	}
	if len(entries) != 0 {
		t.Errorf("dir holds %d files after a refused exchange, want none", len(entries))
	}
}

func TestExchangeWritesSecretsAt0600(t *testing.T) {
	t.Parallel()
	const pem = "-----BEGIN RSA PRIVATE KEY-----\nrecorded\n-----END RSA PRIVATE KEY-----\n" //nolint:gosec // G101: a made-up key body in a recorded fixture; not a real key
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id": 1234, "pem": ` + quoteForJSON(t, pem) +
			`, "client_id": "Iv1.recorded", "client_secret": "recorded-secret"}`))
	}))
	defer srv.Close()

	res, err := setupgithub.Exchange(context.Background(), srv.Client(), srv.URL, "fresh-code")
	if err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	if res.AppID != 1234 || res.PrivateKey != pem {
		t.Errorf("result = %+v, want app 1234 with the recorded key", res)
	}

	dir := t.TempDir()
	pemPath, secretPath, err := setupgithub.WriteSecrets(dir, res.PrivateKey, res.ClientSecret)
	if err != nil {
		t.Fatalf("WriteSecrets: %v", err)
	}
	for _, p := range []string{pemPath, secretPath} {
		fi, err := os.Stat(p)
		if err != nil {
			t.Fatalf("stat %s: %v", p, err)
		}
		if fi.Mode().Perm() != 0o600 {
			t.Errorf("mode of %s = %04o, want 0600", p, fi.Mode().Perm())
		}
	}
	if got, _ := os.ReadFile(pemPath); string(got) != pem { //nolint:gosec // G304: the path is the test's own temp file
		t.Error("pem file does not hold the exchanged key")
	}
	if _, err := os.Stat(filepath.Join(dir, "github-app.pem")); err != nil {
		t.Errorf("pem is not next to the config dir: %v", err)
	}
}

// A network failure during the exchange must not surface the one-time
// code: http.Client.Do returns a url.Error holding the full request URL,
// whose path carries the still-valid code.
func TestExchangeNetworkErrorHidesCode(t *testing.T) {
	t.Parallel()
	const code = "secretcode123"
	broken := &http.Client{Transport: errTransport{err: &url.Error{
		Op:  "Post",
		URL: "https://api.github.com/app-manifests/" + code + "/conversions",
		Err: errors.New("connection refused"),
	}}}
	_, err := setupgithub.Exchange(context.Background(), broken, "https://api.github.com", code)
	if err == nil {
		t.Fatal("broken transport accepted, want an error")
	}
	if strings.Contains(err.Error(), code) {
		t.Errorf("exchange error leaks the one-time code: %q", err)
	}
	var codeErr *setupgithub.CodeError
	if errors.As(err, &codeErr) {
		t.Errorf("network failure typed as CodeError: %v", err)
	}
}

type errTransport struct{ err error }

func (t errTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, t.err
}

// Filesystem failures must name the step only: the raw error keeps the
// full config or secret-file path, which the command prints to stderr.
func TestFilesystemFailuresHidePaths(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if _, _, err := setupgithub.WriteSecrets(dir, "key", "secret"); err != nil {
		t.Fatalf("WriteSecrets: %v", err)
	}
	_, _, err := setupgithub.WriteSecrets(dir, "key", "secret")
	if err == nil {
		t.Fatal("second WriteSecrets accepted, want an exclusivity refusal")
	}
	if strings.Contains(err.Error(), dir) {
		t.Errorf("write error leaks the path: %q", err)
	}
	missing := filepath.Join(dir, "no-such-dir", "fleetd.yaml")
	if err := setupgithub.UpdateConfig(missing, 1, "k", "c", "s", 0); err == nil {
		t.Fatal("UpdateConfig on a missing file accepted, want an error")
	} else if strings.Contains(err.Error(), missing) {
		t.Errorf("config error leaks the path: %q", err)
	}
}

// A re-run loads the saved App identity instead of exchanging a new
// code: LoadSavedApp returns what WriteSecrets plus UpdateConfig stored.
func TestLoadSavedAppRoundTrip(t *testing.T) {
	t.Parallel()
	dir, err := os.MkdirTemp("/tmp", "savedapp") //nolint:usetesting // t.TempDir breaks the socket-path check
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	cfgPath := filepath.Join(dir, "fleetd.yaml")
	base := "state_dir: " + filepath.Join(dir, "state") + "\nlisten: {agent: \"127.0.0.1:0\", api: \"127.0.0.1:0\"}\ndiscovery: {mdns: false}\n"
	if err := os.WriteFile(cfgPath, []byte(base), 0o600); err != nil {
		t.Fatal(err)
	}
	const pem = "key-body"
	pemPath, secretPath, err := setupgithub.WriteSecrets(dir, pem, "s")
	if err != nil {
		t.Fatalf("WriteSecrets: %v", err)
	}
	if err := setupgithub.UpdateConfig(cfgPath, 4321, pemPath, "Iv1.t", secretPath, 0); err != nil {
		t.Fatalf("UpdateConfig: %v", err)
	}
	saved, err := setupgithub.LoadSavedApp(cfgPath)
	if err != nil {
		t.Fatalf("LoadSavedApp: %v", err)
	}
	if saved.AppID != 4321 || saved.PrivateKey != pem || saved.ClientID != "Iv1.t" ||
		saved.PEMPath != pemPath || saved.SecretPath != secretPath {
		t.Errorf("saved app = %+v, want app 4321 with the stored paths", saved)
	}
}

func TestLoadSavedAppMissingRefusesWithoutPath(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	missing := filepath.Join(dir, "fleetd.yaml")
	_, err := setupgithub.LoadSavedApp(missing)
	if err == nil {
		t.Fatal("LoadSavedApp on a missing config accepted, want a refusal")
	}
	if strings.Contains(err.Error(), missing) {
		t.Errorf("saved-app error leaks the path: %q", err)
	}
}

func quoteForJSON(t *testing.T, s string) string {
	t.Helper()
	raw, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestInstallationsZeroOneMany(t *testing.T) {
	t.Parallel()
	payload := `[{"id": 11, "account": {"login": "acme"}}, {"id": 22, "account": {"login": "acme-ops"}}]`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/app/installations" {
			t.Errorf("path = %s, want /app/installations", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(payload))
	}))
	defer srv.Close()

	got, err := setupgithub.ListInstallations(context.Background(), srv.Client(), srv.URL)
	if err != nil {
		t.Fatalf("ListInstallations: %v", err)
	}
	if len(got) != 2 || got[0].ID != 11 || got[1].Account != "acme-ops" {
		t.Errorf("installations = %+v, want ids 11 and 22", got)
	}

	empty := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[]`))
	}))
	defer empty.Close()
	none, err := setupgithub.ListInstallations(context.Background(), empty.Client(), empty.URL)
	if err != nil {
		t.Fatalf("ListInstallations empty: %v", err)
	}
	if len(none) != 0 {
		t.Errorf("installations = %d, want 0", len(none))
	}
}

func TestUpdateConfigWritesGithubKeysAt0600(t *testing.T) {
	t.Parallel()
	// Short root under /tmp: t.TempDir on macOS is too long for the
	// admin socket path the config check enforces.
	dir, err := os.MkdirTemp("/tmp", "sut") //nolint:usetesting // t.TempDir breaks the socket-path check
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	cfgPath := filepath.Join(dir, "fleetd.yaml")
	base := "state_dir: " + filepath.Join(dir, "state") + "\nlisten: {agent: \"127.0.0.1:0\", api: \"127.0.0.1:0\"}\ndiscovery: {mdns: false}\n"
	if err := os.WriteFile(cfgPath, []byte(base), 0o600); err != nil {
		t.Fatal(err)
	}
	err = setupgithub.UpdateConfig(cfgPath, 1234, filepath.Join(dir, "github-app.pem"),
		"Iv1.recorded", filepath.Join(dir, "github-client-secret"), 11)
	if err != nil {
		t.Fatalf("UpdateConfig: %v", err)
	}
	fi, err := os.Stat(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("config mode = %04o, want 0600", fi.Mode().Perm())
	}
	raw, err := os.ReadFile(cfgPath) //nolint:gosec // G304: the path is the test's own temp file
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"app_id: 1234", "installation_id: 11", "private_key_file:", "client_secret_file:"} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("config lacks %q:\n%s", want, raw)
		}
	}
}
