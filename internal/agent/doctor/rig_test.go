package doctor_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/agent/doctor"
	"github.com/linuxdynasty/fleet-board/internal/agent/statusfile"
)

// now is the fixed clock of every test.
var now = time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)

// fakeKey is a stand-in private key. It is built from two pieces so that no
// scanner mistakes the source for a secret, and a test that finds it in
// doctor's output has found a leak.
var fakeKey = "FAKE-KEY-" + "MATERIAL-MUST-NEVER-PRINT"

const machineID = "m_0123456789ab"

type rig struct {
	t                 *testing.T
	state, cfg, probe string
	tmuxOut           string
	tmuxErr           error
	getuid            func() int
	ca                *ecdsa.PrivateKey
	caCert            *x509.Certificate
	caPEM             string
	caDER             []byte
	checkedAt         time.Time
}

func newRig(t *testing.T) *rig {
	t.Helper()
	root := t.TempDir()
	r := &rig{t: t, state: filepath.Join(root, "state"), cfg: filepath.Join(root, "config"), probe: filepath.Join(root, "probe"), tmuxOut: "tmux 3.4", checkedAt: now}
	for _, d := range []string{r.state, r.cfg} {
		if err := os.Mkdir(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	r.ca, r.caCert, r.caDER = r.newCA()
	r.caPEM = string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: r.caDER}))
	return r
}

func (r *rig) newCA() (*ecdsa.PrivateKey, *x509.Certificate, []byte) {
	r.t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		r.t.Fatal(err)
	}
	tpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test ca"},
		NotBefore: now.Add(-48 * time.Hour), NotAfter: now.Add(5 * 365 * 24 * time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		r.t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		r.t.Fatal(err)
	}
	return key, cert, der
}

// leaf returns a client certificate PEM for cn, signed by the rig's CA.
func (r *rig) leaf(cn string, notBefore, notAfter time.Time) string {
	return r.leafBy(r.ca, r.caCert, cn, notBefore, notAfter)
}

func (r *rig) leafBy(signer *ecdsa.PrivateKey, parent *x509.Certificate, cn string, notBefore, notAfter time.Time) string {
	r.t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		r.t.Fatal(err)
	}
	tpl := &x509.Certificate{
		SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: cn},
		NotBefore: notBefore, NotAfter: notAfter, KeyUsage: x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, parent, &key.PublicKey, signer)
	if err != nil {
		r.t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

// writeIdentity writes identity.json the way conn does, with certPEM as the
// enrolled certificate and a fake key.
func (r *rig) writeIdentity(certPEM string, notAfter time.Time) {
	r.t.Helper()
	data, err := json.Marshal(map[string]any{
		"version": 1, "machine_id": machineID, "machine_name": "box", "fleet_id": "f_1",
		"ca_fingerprint": "x", "cert_not_after": notAfter, "salt_version": 1, "account_salt": "AAAA",
		"cert_pem": certPEM, "ca_pem": r.caPEM, "key_pem": fakeKey, "enrolled_time": now,
	})
	if err != nil {
		r.t.Fatal(err)
	}
	r.write(r.state, "identity.json", string(data), 0o600)
}

// enroll writes a good identity whose certificate lives for 90 days and
// has lived for `age`.
func (r *rig) enroll(age time.Duration) {
	r.t.Helper()
	nb := now.Add(-age)
	na := nb.Add(90 * 24 * time.Hour)
	r.writeIdentity(r.leaf(machineID, nb, na), na)
}

func (r *rig) write(dir, name, content string, mode os.FileMode) {
	r.t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), mode); err != nil {
		r.t.Fatal(err)
	}
	if err := os.Chmod(p, mode); err != nil {
		r.t.Fatal(err)
	}
}

// writeStatus writes status.json, written `ago` before now.
func (r *rig) writeStatus(ago time.Duration, edit func(*statusfile.Status)) {
	r.t.Helper()
	s := statusfile.Status{Version: 1, WrittenTime: now.Add(-ago)}
	if edit != nil {
		edit(&s)
	}
	data, err := json.Marshal(s)
	if err != nil {
		r.t.Fatal(err)
	}
	r.write(r.state, statusfile.StatusFile, string(data), 0o600)
}

func (r *rig) opts() doctor.Options {
	return doctor.Options{
		StateDir: r.state, ConfigDir: r.cfg, ProbeDir: r.probe,
		Now:         func() time.Time { return r.checkedAt },
		Getuid:      r.getuid,
		TmuxVersion: func(context.Context) (string, error) { return r.tmuxOut, r.tmuxErr },
	}
}

func (r *rig) run() doctor.Report {
	r.t.Helper()
	return doctor.Run(r.t.Context(), r.opts(), doctor.Local()...)
}

// get runs every local check and returns the one with id.
func (r *rig) get(id string) doctor.Result {
	r.t.Helper()
	for _, c := range r.run().Checks {
		if c.ID == id {
			return c
		}
	}
	r.t.Fatalf("no check %q", id)
	return doctor.Result{}
}

// want asserts a check's status and that its reason and fix contain the words.
func (r *rig) want(id string, status doctor.Status, words ...string) doctor.Result {
	r.t.Helper()
	c := r.get(id)
	if c.Status != status {
		r.t.Errorf("%s: status = %s (%q, fix %q), want %s", id, c.Status, c.Reason, c.Fix, status)
	}
	text := c.Reason + " | " + c.Fix
	for _, w := range words {
		if !strings.Contains(text, w) {
			r.t.Errorf("%s: %q does not mention %q", id, text, w)
		}
	}
	return c
}

// twoHours is well past the two minutes after which a status file is old.
const twoHours = 2 * time.Hour
