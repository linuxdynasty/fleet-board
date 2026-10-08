package ca_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/linuxdynasty/fleet-board/internal/hub/ca"
)

func TestParseTokenReadsBackTheCAHashAndTheSecretHash(t *testing.T) {
	t.Parallel()
	caDER := newCA(t).Cert.Raw
	secret := bytes.Repeat([]byte{0x3c}, 32)
	tok, err := ca.NewToken(caDER, bytes.NewReader(secret))
	if err != nil {
		t.Fatal(err)
	}
	got, err := ca.ParseToken(tok.Reveal())
	if err != nil {
		t.Fatalf("ParseToken: %v", err)
	}
	if got.SecretHash != sha256.Sum256(secret) {
		t.Error("SecretHash is not SHA-256 of the 32 secret bytes")
	}
	if got.CAFingerprint != sha256.Sum256(caDER) {
		t.Error("CAFingerprint is not SHA-256 of the CA certificate")
	}
	if s := fmt.Sprint(got); strings.Contains(s, tok.Reveal()[5:]) {
		t.Errorf("a parsed token prints its secret: %s", s)
	}
}

func TestParseTokenRefusesAnythingElse(t *testing.T) {
	t.Parallel()
	payload := func(version byte, n int) string {
		b := make([]byte, n)
		b[0] = version
		return base64.RawURLEncoding.EncodeToString(b)
	}
	cases := map[string]string{
		"empty":                  "",
		"prefix only":            "fbe1_",
		"no prefix":              payload(1, 65),
		"another prefix":         "fbe2_" + payload(1, 65),
		"version 2":              "fbe1_" + payload(2, 65),
		"version 0":              "fbe1_" + payload(0, 65),
		"64 bytes":               "fbe1_" + payload(1, 64),
		"66 bytes":               "fbe1_" + payload(1, 66),
		"padded base64":          "fbe1_" + base64.URLEncoding.EncodeToString(make([]byte, 65)),
		"standard alphabet":      "fbe1_" + strings.Repeat("+", 87),
		"trailing space":         "fbe1_" + payload(1, 65) + " ",
		"a kilobyte of base64":   "fbe1_" + strings.Repeat("A", 1024),
		"newline inside payload": "fbe1_" + payload(1, 65)[:40] + "\n" + payload(1, 65)[40:],
	}
	for name, text := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, err := ca.ParseToken(text)
			if !errors.Is(err, ca.ErrTokenMalformed) {
				t.Errorf("ParseToken = %v, want ErrTokenMalformed", err)
			}
			if err != nil && text != "" && strings.Contains(err.Error(), text) {
				t.Error("the error echoes the token text")
			}
		})
	}
}
