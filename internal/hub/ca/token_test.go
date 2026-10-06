package ca_test

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/linuxdynasty/fleet-board/internal/hub/ca"
)

var tokenPattern = regexp.MustCompile(`^fbe1_[A-Za-z0-9_-]+$`)

// The token is fbe1_ + unpadded base64url of 65 bytes: 0x01, SHA-256 of the
// CA certificate DER, then the 32 secret bytes (02g, machine.proto).
func TestATokenCarriesVersionCAHashAndSecret(t *testing.T) {
	t.Parallel()
	caDER := newCA(t).Cert.Raw
	secret := bytes.Repeat([]byte{0xa5}, 32)
	tok, err := ca.NewToken(caDER, bytes.NewReader(secret))
	if err != nil {
		t.Fatalf("NewToken: %v", err)
	}
	text := tok.Reveal()
	if !tokenPattern.MatchString(text) {
		t.Fatalf("token %q does not match %s", text, tokenPattern)
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(text, "fbe1_"))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(raw) != 65 {
		t.Fatalf("decoded %d bytes, want 65", len(raw))
	}
	if raw[0] != 1 {
		t.Errorf("version byte %d, want 1", raw[0])
	}
	fp := sha256.Sum256(caDER)
	if !bytes.Equal(raw[1:33], fp[:]) {
		t.Error("bytes 1-32 are not SHA-256 of the CA certificate")
	}
	if !bytes.Equal(raw[33:], secret) {
		t.Error("bytes 33-64 are not the secret read from the random source")
	}
	if want := sha256.Sum256(secret); tok.SecretHash != want {
		t.Error("SecretHash is not SHA-256 of the 32 secret bytes")
	}
	if tok.CAFingerprint != fp {
		t.Error("CAFingerprint is not SHA-256 of the CA certificate")
	}
	// 65 bytes in unpadded base64url are 87 characters, with no '='.
	if len(text) != len("fbe1_")+87 {
		t.Errorf("token is %d characters, want %d", len(text), len("fbe1_")+87)
	}
}

func TestTwoTokensDiffer(t *testing.T) {
	t.Parallel()
	caDER := newCA(t).Cert.Raw
	a, err := ca.NewToken(caDER, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	b, err := ca.NewToken(caDER, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if a.Reveal() == b.Reveal() || a.SecretHash == b.SecretHash {
		t.Error("two tokens from crypto/rand are equal")
	}
}

func TestAShortRandomSourceIsAnError(t *testing.T) {
	t.Parallel()
	caDER := newCA(t).Cert.Raw
	for name, r := range map[string]interface{ Read([]byte) (int, error) }{
		"short": bytes.NewReader(make([]byte, 31)),
		"error": iotest.ErrReader(fmt.Errorf("no entropy")),
	} {
		if _, err := ca.NewToken(caDER, r); err == nil {
			t.Errorf("%s random source: NewToken succeeded", name)
		}
	}
	if _, err := ca.NewToken(nil, rand.Reader); err == nil {
		t.Error("an empty CA certificate made a token")
	}
}

// Printing or logging a Token never shows the secret or its hash.
func TestATokenNeverPrintsItsSecret(t *testing.T) {
	t.Parallel()
	tok, err := ca.NewToken(newCA(t).Cert.Raw, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	var logged bytes.Buffer
	slog.New(slog.NewJSONHandler(&logged, nil)).Info("x", "token", tok)
	secret := strings.TrimPrefix(tok.Reveal(), "fbe1_")
	hash := hex.EncodeToString(tok.SecretHash[:])
	for name, out := range map[string]string{
		"%v": fmt.Sprintf("%v", tok), "%+v": fmt.Sprintf("%+v", tok), "%#v": fmt.Sprintf("%#v", tok),
		"%q": fmt.Sprintf("%q", tok), "slog": logged.String(),
	} {
		if strings.Contains(out, secret[40:]) || strings.Contains(out, hash[:12]) {
			t.Errorf("%s shows the secret or its hash: %s", name, out)
		}
	}
}
