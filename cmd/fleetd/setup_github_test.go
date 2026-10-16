package main_test

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

var (
	openURL   = regexp.MustCompile(`open (http://127\.0\.0\.1:\d+)`)
	formState = regexp.MustCompile(`state=([0-9a-f]+)`)
)

// Row 13a's acceptance for `fleetd setup github`: the command prints a
// local URL, the owner's confirm comes back as the one redirect, the
// code is exchanged against recorded GitHub responses, and the key file
// lands mode 0600 with the github keys in fleetd.yaml.
func TestSetupGithubAgainstRecordedResponses(t *testing.T) {
	t.Parallel()
	config, _ := setup(t)

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	pemBody := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/app-manifests/testcode123/conversions":
			_, _ = io.WriteString(w, `{"id": 4321, "pem": `+quoteJSON(t, string(pemBody))+
				`, "client_id": "Iv1.test", "client_secret": "test-secret"}`)
		case r.Method == http.MethodGet && r.URL.Path == "/app/installations":
			_, _ = io.WriteString(w, `[{"id": 77, "account": {"login": "acme"}}]`)
		default:
			t.Errorf("unexpected GitHub call: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	cmd := fleetd(t, "setup", "github", "--config", config, "--api-base", srv.URL)
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	stderr, err := cmd.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	local := make(chan string, 1)
	go func() {
		sc := bufio.NewScanner(stderr)
		for sc.Scan() {
			if m := openURL.FindStringSubmatch(sc.Text()); m != nil {
				local <- m[1]
				return
			}
		}
	}()
	var pageURL string
	select {
	case pageURL = <-local:
	case <-time.After(20 * time.Second):
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		t.Fatal("setup github printed no local URL within 20 s")
	}

	// The owner opens the page and confirms on GitHub: the page posts
	// the manifest, GitHub redirects once with the code and state.
	get := func(url string) (*http.Response, error) {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, nil)
		if err != nil {
			return nil, err
		}
		return http.DefaultClient.Do(req)
	}
	page, err := get(pageURL + "/")
	if err != nil {
		t.Fatal(err)
	}
	rawPage, _ := io.ReadAll(page.Body)
	_ = page.Body.Close()
	m := formState.FindStringSubmatch(string(rawPage))
	if m == nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		t.Fatal("setup page names no state")
	}
	cb, err := get(pageURL + "/callback?code=testcode123&state=" + m[1])
	if err != nil {
		t.Fatal(err)
	}
	_ = cb.Body.Close()

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if exitCode(err) != 0 {
			t.Fatalf("setup github exit %d, want 0", exitCode(err))
		}
	case <-time.After(30 * time.Second):
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		t.Fatal("setup github did not exit within 30 s of the redirect")
	}
	if !strings.Contains(stdout.String(), "restart fleetd to apply") {
		t.Errorf("stdout %q lacks the restart line", stdout.String())
	}
	dir := filepath.Dir(config)
	fi, err := os.Stat(filepath.Join(dir, "github-app.pem"))
	if err != nil {
		t.Fatalf("github-app.pem: %v", err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("github-app.pem mode %04o, want 0600", fi.Mode().Perm())
	}
	raw, err := os.ReadFile(config) //nolint:gosec // G304: the path is the test's own temp config
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"app_id: 4321", "installation_id: 77", "private_key_file:", "client_secret_file:"} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("fleetd.yaml lacks %q:\n%s", want, raw)
		}
	}
}

func quoteJSON(t *testing.T, s string) string {
	t.Helper()
	raw, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
