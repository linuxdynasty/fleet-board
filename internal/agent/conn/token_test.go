package conn_test

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/linuxdynasty/fleet-board/internal/agent/conn"
	"github.com/linuxdynasty/fleet-board/internal/hub/ca"
)

// The agent reads the token the hub mints (02g): the CA hash it finds is the
// one the hub put in.
func TestParseTokenReadsWhatTheHubMints(t *testing.T) {
	t.Parallel()
	caDER := []byte("a CA certificate")
	minted, err := ca.NewToken(caDER, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	got, err := conn.ParseToken(minted.Reveal())
	if err != nil {
		t.Fatalf("ParseToken: %v", err)
	}
	if got.CAFingerprint != sha256.Sum256(caDER) {
		t.Error("the CA fingerprint is not SHA-256 of the CA the hub used")
	}
	if got.Reveal() != minted.Reveal() {
		t.Error("Reveal does not give back the token text")
	}
}

func TestParseTokenRefusesAnythingElse(t *testing.T) {
	t.Parallel()
	payload := func(version byte, n int) string {
		b := bytes.Repeat([]byte{9}, n)
		b[0] = version
		return base64.RawURLEncoding.EncodeToString(b)
	}
	good := payload(1, 65)
	cases := []struct{ name, text string }{
		{"empty", ""},
		{"no prefix", good},
		{"other prefix", "fbe2_" + good},
		{"version 2", "fbe1_" + payload(2, 65)},
		{"64 bytes", "fbe1_" + payload(1, 64)},
		{"66 bytes", "fbe1_" + payload(1, 66)},
		{"padded", "fbe1_" + base64.URLEncoding.EncodeToString(bytes.Repeat([]byte{1}, 65))},
		{"standard alphabet", "fbe1_" + strings.ReplaceAll(strings.ReplaceAll(good, "-", "+"), "_", "/") + "+"},
		{"trailing space", "fbe1_" + good + " "},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := conn.ParseToken(tc.text)
			if !errors.Is(err, conn.ErrTokenMalformed) {
				t.Fatalf("ParseToken(%s) = %v, want ErrTokenMalformed", tc.name, err)
			}
			if tc.text != "" && strings.Contains(err.Error(), tc.text) {
				t.Error("the error echoes the token text")
			}
		})
	}
}

func TestATokenNeverPrintsItsSecret(t *testing.T) {
	t.Parallel()
	minted, err := ca.NewToken([]byte("ca"), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tok, err := conn.ParseToken(minted.Reveal())
	if err != nil {
		t.Fatal(err)
	}
	var logged bytes.Buffer
	slog.New(slog.NewJSONHandler(&logged, nil)).Info("x", "token", tok)
	for name, out := range map[string]string{
		"%v": fmt.Sprintf("%v", tok), "%+v": fmt.Sprintf("%+v", tok), "%#v": fmt.Sprintf("%#v", tok),
		"%q": fmt.Sprintf("%q", tok), "slog": logged.String(),
	} {
		if strings.Contains(out, minted.Reveal()[5:20]) {
			t.Errorf("%s shows the token: %s", name, out)
		}
	}
}

func TestHubAddressAddsTheAgentPort(t *testing.T) {
	t.Parallel()
	cases := []struct{ in, want string }{
		{"hub.local", "hub.local:7443"},
		{"hub.local:9443", "hub.local:9443"},
		{"10.0.0.5", "10.0.0.5:7443"},
		{"[::1]:7443", "[::1]:7443"},
		{"::1", "[::1]:7443"},
		// One spelling per address, so a list keeps no repeats.
		{"HUB.Local.", "hub.local:7443"},
		{"hub.local:07443", "hub.local:7443"},
		{"[2001:DB8:0::1]:7443", "[2001:db8::1]:7443"},
		{"[::ffff:10.0.0.5]:7443", "10.0.0.5:7443"},
	}
	for _, tc := range cases {
		got, err := conn.HubAddress(tc.in)
		if err != nil || got != tc.want {
			t.Errorf("HubAddress(%q) = %q, %v; want %q", tc.in, got, err, tc.want)
		}
	}
	if _, err := conn.HubAddress(""); !errors.Is(err, conn.ErrNoHub) {
		t.Errorf("HubAddress(\"\") = %v, want ErrNoHub", err)
	}
	for _, bad := range []string{
		":7443", "hub:", "a b:1", "hub/x", ".:7443",
		// No IPv6 zone (it can carry any byte, and it means nothing off the
		// local link), and no address longer than a host name and a port.
		"[fe80::1%a\x01b]:7443", "[fe80::1%eth0]:7443", "fe80::1%eth0",
		strings.Repeat("a", 253) + ":" + strings.Repeat("0", 300) + "1",
	} {
		if _, err := conn.HubAddress(bad); err == nil {
			t.Errorf("HubAddress(%q) accepted a bad address", bad)
		}
	}
}
