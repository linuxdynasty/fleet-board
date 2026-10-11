package conn_test

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/agent/conn"
	"github.com/linuxdynasty/fleet-board/internal/agent/conn/conntest"
	"github.com/linuxdynasty/fleet-board/internal/agent/events"
	"github.com/linuxdynasty/fleet-board/internal/agent/ring"
	"github.com/linuxdynasty/fleet-board/internal/telemetry"
)

// agent is one agent state folder and its log lines.
type agent struct {
	dir  string
	logs *conntest.Buffer
	tel  *telemetry.Telemetry
	db   *ring.DB
	em   *events.Emitter
}

// newAgent names a state folder that does not exist yet; Enroll creates it.
func newAgent(t *testing.T) *agent {
	t.Helper()
	logs := &conntest.Buffer{}
	tel, err := telemetry.New(telemetry.Config{Service: telemetry.Agent, Version: "test", InstanceID: "boot-test", LogOutput: logs})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tel.Shutdown(context.Background()) })
	return &agent{dir: filepath.Join(t.TempDir(), "fleet-agent"), logs: logs, tel: tel}
}

func (a *agent) enroll(t *testing.T, hub, token string) (conn.Result, error) {
	t.Helper()
	return a.enrollWith(t, hub, token, nil)
}

// enrollWith runs Enroll with the default tries but millisecond waits, after
// set adjusts the options.
func (a *agent) enrollWith(t *testing.T, hub, token string, set func(*conn.Options)) (conn.Result, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	opts := conn.Options{
		Dir: a.dir, Hub: hub, Token: token, Version: "test", Telemetry: a.tel,
		Retry: conn.Retry{FirstWait: time.Millisecond, MaxWait: 5 * time.Millisecond},
	}
	if set != nil {
		set(&opts)
	}
	return conn.Enroll(ctx, opts)
}

func (a *agent) events(t *testing.T, prefix string) []map[string]any {
	t.Helper()
	return conntest.Lines(t, a.logs.String(), prefix)
}

func perm(t *testing.T, path string) fs.FileMode {
	t.Helper()
	fi, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Mode().Perm()
}

func exists(t *testing.T, path string) bool {
	t.Helper()
	_, err := os.Lstat(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		t.Fatal(err)
	}
	return err == nil
}

// onePEM decodes the single PEM block of type typ in in.
func onePEM(t *testing.T, in []byte, typ string) []byte {
	t.Helper()
	b, rest := pem.Decode(in)
	if b == nil || b.Type != typ || len(rest) != 0 {
		t.Fatalf("not one %s PEM block: %q", typ, in)
	}
	return b.Bytes
}

// chainsToCA checks the stored client certificate against caDER alone, the
// way the hub checks it at the handshake.
func chainsToCA(t *testing.T, id conn.Identity, caDER []byte) *x509.Certificate {
	t.Helper()
	leaf, err := x509.ParseCertificate(onePEM(t, []byte(id.CertPEM), "CERTIFICATE"))
	if err != nil {
		t.Fatal(err)
	}
	root, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(root)
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: roots, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err != nil {
		t.Errorf("the stored certificate does not chain to the hub's CA: %v", err)
	}
	if sha256.Sum256(onePEM(t, []byte(id.CAPEM), "CERTIFICATE")) != sha256.Sum256(caDER) {
		t.Error("the pinned CA is not the hub's CA")
	}
	return leaf
}
