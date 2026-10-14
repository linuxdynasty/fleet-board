package github

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func testPrivateKey(t *testing.T) []byte {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	})
}

func TestTokenSourceCaches(t *testing.T) {
	t.Parallel()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/app/installations/123/access_tokens" {
			t.Errorf("token path = %s, want the installation endpoint", r.URL.Path)
		}
		if r.Header.Get("Authorization") == "" {
			t.Error("token request carries no JWT")
		}
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"token":      "ghs_testtoken",
			"expires_at": "2027-10-01T13:00:00Z",
		})
	}))
	defer srv.Close()

	src, err := NewTokenSource(srv.Client().Transport, srv.URL, 456, 123, testPrivateKey(t))
	if err != nil {
		t.Fatalf("NewTokenSource: %v", err)
	}
	for range 3 {
		tok, err := src.Token(context.Background())
		if err != nil {
			t.Fatalf("Token: %v", err)
		}
		if tok != "ghs_testtoken" {
			t.Errorf("token = %q, want ghs_testtoken", tok)
		}
	}
	if got := hits.Load(); got != 1 {
		t.Errorf("token endpoint hits = %d, want 1 (cached)", got)
	}
}

func TestTokenSourceBadKey(t *testing.T) {
	t.Parallel()
	if _, err := NewTokenSource(http.DefaultTransport, "https://api.github.com", 456, 123, []byte("not a key")); err == nil {
		t.Error("bad key succeeded, want an error")
	}
}
