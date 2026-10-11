package doctor_test

import (
	"bytes"
	"encoding/pem"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/agent/doctor"
)

func TestIdentity(t *testing.T) {
	t.Parallel()
	t.Run("enrolled", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		r.enroll(time.Hour)
		r.want("identity", doctor.OK, machineID)
	})
	t.Run("not enrolled", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		r.want("identity", doctor.Fail, "not enrolled", "fleet-agent enroll --token")
	})
	t.Run("an interrupted enroll leaves its key", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		r.write(r.state, "enroll.key", "x", 0o600)
		r.want("identity", doctor.Fail, "not enrolled", "same token")
	})
	t.Run("open to other users", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		r.enroll(time.Hour)
		if err := os.Chmod(r.state+"/identity.json", 0o644); err != nil { //nolint:gosec // G302: the point of the test
			t.Fatal(err)
		}
		r.want("identity", doctor.Fail, "0644", "chmod 600")
	})
	t.Run("damaged", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		r.write(r.state, "identity.json", "{not json", 0o600)
		c := r.want("identity", doctor.Fail, "damaged", "remove only identity.json")
		if strings.Contains(c.Reason, "not json") {
			t.Errorf("file text in the reason: %q", c.Reason)
		}
	})
	t.Run("a machine id with control characters is cleaned", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		r.write(r.state, "identity.json", `{"version":1,"machine_id":"m\u001b[2Jx","cert_pem":"a","ca_pem":"b","key_pem":"c"}`, 0o600)
		c := r.get("identity")
		if strings.ContainsRune(c.Reason, 0x1b) {
			t.Errorf("reason has an escape: %q", c.Reason)
		}
	})
}

func TestCertExpiry(t *testing.T) {
	t.Parallel()
	t.Run("early in its life", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		r.enroll(10 * 24 * time.Hour)
		r.want("cert_expiry", doctor.OK, "80 days", "2026-12-30")
	})
	t.Run("past two thirds, renewal is due", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		r.enroll(70 * 24 * time.Hour)
		r.want("cert_expiry", doctor.Warn, "20 days", "renew", "fleet-agent run")
	})
	t.Run("expired", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		r.enroll(100 * 24 * time.Hour)
		r.want("cert_expiry", doctor.Fail, "expired", "re-enroll")
	})
	t.Run("not enrolled", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		r.want("cert_expiry", doctor.Unknown, "not enrolled")
	})
	t.Run("the pair that ends last decides", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		r.enroll(100 * 24 * time.Hour) // the enrolled one ran out
		r.write(r.state, "cert.pem", r.leaf(machineID, now.Add(-time.Hour), now.Add(90*24*time.Hour))+fakeKeyBlock(), 0o600)
		r.want("cert_expiry", doctor.OK, "90 days")
	})
	t.Run("a renewed certificate that is not valid does not extend the life", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		r.enroll(100 * 24 * time.Hour)
		r.write(r.state, "cert.pem", r.leaf("someone-else", now.Add(-time.Hour), now.Add(90*24*time.Hour))+fakeKeyBlock(), 0o600)
		r.want("cert_expiry", doctor.Fail, "expired")
	})
}

func fakeKeyBlock() string {
	return string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: []byte(fakeKey)}))
}

func TestRenewedCertificateFile(t *testing.T) {
	t.Parallel()
	good := func(r *rig) {
		r.enroll(30 * 24 * time.Hour)
		r.write(r.state, "cert.pem", r.leaf(machineID, now.Add(-time.Hour), now.Add(90*24*time.Hour))+fakeKeyBlock(), 0o600)
	}
	t.Run("none yet is normal", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		r.enroll(time.Hour)
		r.want("cert_file", doctor.OK, "no renewed certificate")
	})
	t.Run("valid, and the key is not checked", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		good(r)
		r.want("cert_file", doctor.OK, "key was not read")
	})
	t.Run("open to other users", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		good(r)
		if err := os.Chmod(r.state+"/cert.pem", 0o644); err != nil { //nolint:gosec // G302: the point of the test
			t.Fatal(err)
		}
		r.want("cert_file", doctor.Fail, "0644", "chmod 600")
	})
	t.Run("damaged", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		r.enroll(time.Hour)
		r.write(r.state, "cert.pem", "garbage", 0o600)
		r.want("cert_file", doctor.Warn, "damaged", "enrolled certificate", "next renewal")
	})
	t.Run("for another machine", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		r.enroll(time.Hour)
		r.write(r.state, "cert.pem", r.leaf("other", now.Add(-time.Hour), now.Add(time.Hour))+fakeKeyBlock(), 0o600)
		r.want("cert_file", doctor.Warn, "another machine")
	})
	t.Run("signed by another CA", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		r.enroll(time.Hour)
		k, c, _ := r.newCA()
		r.write(r.state, "cert.pem", r.leafBy(k, c, machineID, now.Add(-time.Hour), now.Add(time.Hour))+fakeKeyBlock(), 0o600)
		r.want("cert_file", doctor.Warn, "pinned CA")
	})
	t.Run("with no identity there is nothing to compare with", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		r.write(r.state, "cert.pem", "x", 0o600)
		r.want("cert_file", doctor.Unknown, "not enrolled")
	})
}

func TestRenewKey(t *testing.T) {
	t.Parallel()
	t.Run("absent", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		r.want("renew_key", doctor.OK, "no renewal in progress")
	})
	t.Run("present and private", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		r.write(r.state, "renew.key", fakeKey, 0o600)
		r.want("renew_key", doctor.OK, "renewal", "not read")
	})
	t.Run("open to other users blocks renewal", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		r.write(r.state, "renew.key", fakeKey, 0o640)
		r.want("renew_key", doctor.Fail, "0640", "chmod 600", "renewal is blocked")
	})
}

// The key is never printed, whatever the check: not in the text, not in the JSON.
func TestTheKeyNeverReachesTheOutput(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.enroll(70 * 24 * time.Hour)
	r.write(r.state, "cert.pem", r.leaf(machineID, now.Add(-time.Hour), now.Add(90*24*time.Hour))+fakeKeyBlock(), 0o600)
	r.write(r.state, "renew.key", fakeKey, 0o600)
	rep := r.run()
	var text, js bytes.Buffer
	if err := doctor.WriteText(&text, rep); err != nil {
		t.Fatal(err)
	}
	if err := doctor.WriteJSON(&js, rep); err != nil {
		t.Fatal(err)
	}
	for _, out := range []string{text.String(), js.String()} {
		if strings.Contains(out, "FAKE-KEY") || strings.Contains(out, "MATERIAL") {
			t.Fatalf("key material in the output:\n%s", out)
		}
	}
}
