package conn

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"path/filepath"
	"testing"
	"time"
)

// renewKey keeps renew.key across tries, makes one when there is none, and
// replaces one that holds the key in use: a leftover whose removal failed
// after a renewal saved, which the hub would refuse as the same key.
func TestRenewKeyKeepsItsKeyButNotTheOneInUse(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	_, inUse, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "m"}, NotAfter: time.Now().Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, inUse.Public(), inUse)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	cur := &certPair{leaf: leaf, key: inUse}

	first, kept, err := renewKey(dir, cur)
	if err != nil || kept {
		t.Fatalf("renewKey with no file = kept %v, %v; want a new key", kept, err)
	}
	again, kept, err := renewKey(dir, cur)
	if err != nil || !kept || !again.Equal(first) {
		t.Fatalf("renewKey again = kept %v, %v; want the same key", kept, err)
	}

	data, err := keyPEM(inUse)
	if err != nil {
		t.Fatal(err)
	}
	if err := writePrivate(dir, RenewKeyFile, data, true); err != nil {
		t.Fatal(err)
	}
	fresh, kept, err := renewKey(dir, cur)
	if err != nil || kept || fresh.Equal(inUse) {
		t.Fatalf("renewKey over the key in use = kept %v, %v, same %v; want a new key", kept, err, fresh.Equal(inUse))
	}
	stored, err := readKey(filepath.Join(dir, RenewKeyFile))
	if err != nil || !stored.Equal(fresh) {
		t.Fatalf("renew.key holds another key than the one returned (%v)", err)
	}
}

// A 90-day pair is due at day 60 of its life, to the second.
func TestAPairIsDueAtTwoThirdsOfItsLife(t *testing.T) {
	t.Parallel()
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	p := &certPair{leaf: &x509.Certificate{NotBefore: start, NotAfter: start.Add(90 * 24 * time.Hour)}}
	at := start.Add(60 * 24 * time.Hour)
	if !p.renewAfter().Equal(at) {
		t.Fatalf("renewAfter = %v, want %v", p.renewAfter(), at)
	}
	for _, tc := range []struct {
		offset time.Duration
		due    bool
	}{{-time.Second, false}, {0, true}, {time.Second, true}} {
		if got := p.due(at.Add(tc.offset)); got != tc.due {
			t.Errorf("due(day 60 %+v) = %v, want %v", tc.offset, got, tc.due)
		}
	}
}
