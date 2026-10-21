package fleetcli_test

import (
	"crypto/sha256"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
	"github.com/linuxdynasty/fleet-board/internal/seed"
)

// seedRevocableMachine enrolls a machine with a lowercase-hex serial, the
// shape a real enroll produces. The shared seedMachine uses a readable fake
// serial, which the revoked-serials file refuses to stage, so revoke tests
// seed their own machines here.
func seedRevocableMachine(t *testing.T, state, id, serial, hostname string) {
	t.Helper()
	st, err := statedb.Open(t.Context(), state, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	now := time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC)
	hash := sha256.Sum256([]byte("cli-revoke-" + id))
	if err := statedb.InsertEnrollToken(t.Context(), st, statedb.EnrollToken{
		SecretHash: hash, MachineName: hostname, ExpectedHostname: hostname + ".lan",
		NotAfter: now.Add(time.Hour), Created: now, Principal: "local:seed",
		RequestID: "cli-revoke-enroll-" + id, RequestHash: hash,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := statedb.Enroll(t.Context(), st, statedb.NewMachine{
		Key:       statedb.EnrollKey{SecretHash: hash, InstallID: "cli-revoke-install-" + id, CSRKeyHash: hash},
		MachineID: id, CertDER: []byte("cli-revoke-cert-" + id), Serial: serial,
		CertNotAfter: now.Add(time.Hour), ReportedHostname: hostname + ".lan",
		Facts: `{"hostname":"` + hostname + `.lan"}`, Now: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.Write(t.Context(), func(tx *sql.Tx) error {
		_, _, err := statedb.MarkOnline(t.Context(), tx, id, now)
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestMachineAddPrintsTheEnrollCommand(t *testing.T) {
	h := startHubWithExtra(t, false, seedGitHubRow)
	code, out, diagnostic := invokeSecret(t, h, h.secret,
		"machine", "add", "--name", "media", "--request-id", "cli-madd-1")
	if code != 0 {
		t.Fatalf("add = %d: %s", code, diagnostic)
	}
	if !strings.Contains(out, "fleet-agent enroll --token fbe1_") {
		t.Fatalf("add output = %q", out)
	}
	code, jsonOut, diagnostic := invokeSecret(t, h, h.secret,
		"machine", "add", "--name", "media", "--request-id", "cli-madd-2", "--json")
	if code != 0 {
		t.Fatalf("add --json = %d: %s", code, diagnostic)
	}
	if !strings.Contains(jsonOut, `"token":"fbe1_`) {
		t.Fatalf("add JSON = %s", jsonOut)
	}
	golden(t, "machine-add", jsonOut)
}

func TestMachineRenameAndLabel(t *testing.T) {
	h := startHubWithExtra(t, true, seedGitHubRow)
	code, out, diagnostic := invokeSecret(t, h, h.secret,
		"machine", "rename", seed.MachineLaptop, "--display-name", "renamed",
		"--expected-version", "1", "--request-id", "cli-mren-1", "--json")
	if code != 0 {
		t.Fatalf("rename = %d: %s", code, diagnostic)
	}
	if !strings.Contains(out, `"displayName":"renamed"`) || !strings.Contains(out, `"version":"2"`) {
		t.Fatalf("rename output = %s", out)
	}
	golden(t, "machine-rename", out)
	code, out, diagnostic = invokeSecret(t, h, h.secret,
		"machine", "label", seed.MachineLaptop, "--labels", "desk,home",
		"--expected-version", "2", "--request-id", "cli-mlab-1", "--json")
	if code != 0 {
		t.Fatalf("label = %d: %s", code, diagnostic)
	}
	if !strings.Contains(out, `"desk"`) || !strings.Contains(out, `"home"`) {
		t.Fatalf("label output = %s", out)
	}
}

func TestMachineRevokeAndForget(t *testing.T) {
	const doomed, live = "01jclidoomed0000000000001", "01jclilive00000000000002"
	h := startHubWithExtra(t, false, func(t *testing.T, state string) {
		t.Helper()
		seedGitHubRow(t, state)
		seedRevocableMachine(t, state, doomed, "ab12cd34ef56ab12cd34ef56ab12cd34", "doomed")
		seedRevocableMachine(t, state, live, "cd34ef56ab12cd34ef56ab12cd34ef56ab12", "live")
	})
	code, out, _ := invokeSecret(t, h, h.secret,
		"machine", "revoke", doomed, "--request-id", "cli-mrev-0")
	if code != 2 || out != "" {
		t.Fatalf("revoke without --yes = %d, %q", code, out)
	}
	code, out, diagnostic := invokeSecret(t, h, h.secret,
		"machine", "revoke", doomed, "--reason", "stolen",
		"--expected-version", "1", "--request-id", "cli-mrev-1", "--yes", "--json")
	if code != 0 {
		t.Fatalf("revoke = %d: %s", code, diagnostic)
	}
	if !strings.Contains(out, "REVOKED") {
		t.Fatalf("revoke output = %s", out)
	}
	golden(t, "machine-revoke", out)
	// Forgetting a live machine is refused; only a revoked one goes.
	code, out, diagnostic = invokeSecret(t, h, h.secret,
		"machine", "forget", live, "--request-id", "cli-mdel-0", "--yes")
	if code != 1 || out != "" || !strings.Contains(diagnostic, "MACHINE_NOT_REVOKED") {
		t.Fatalf("forget live = %d, %q, %q", code, out, diagnostic)
	}
	code, out, diagnostic = invokeSecret(t, h, h.secret,
		"machine", "forget", doomed,
		"--expected-version", "2", "--request-id", "cli-mdel-1", "--yes")
	if code != 0 || !strings.Contains(out, doomed) {
		t.Fatalf("forget = %d, %q, %q", code, out, diagnostic)
	}
}

func TestMachineStaleVersionIsRefused(t *testing.T) {
	h := startHubWithExtra(t, true, seedGitHubRow)
	code, out, diagnostic := invokeSecret(t, h, h.secret,
		"machine", "rename", seed.MachineLaptop, "--display-name", "stale",
		"--expected-version", "99", "--request-id", "cli-mren-9")
	if code != 1 || out != "" || !strings.Contains(diagnostic, "VERSION_MISMATCH") {
		t.Fatalf("stale = %d, %q, %q", code, out, diagnostic)
	}
}

func TestMachineUsageErrors(t *testing.T) {
	h := startHubWithExtra(t, true, seedGitHubRow)
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"add without name", []string{"machine", "add", "--request-id", "x"}},
		{"add bad ttl", []string{"machine", "add", "--name", "m", "--ttl", "soon"}},
		{"rename without name", []string{"machine", "rename", seed.MachineLaptop, "--request-id", "x"}},
		{"rename without id", []string{"machine", "rename", "--display-name", "x"}},
		{"label without labels", []string{"machine", "label", seed.MachineLaptop, "--request-id", "x"}},
		{"forget without yes", []string{"machine", "forget", seed.MachineLaptop}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, out, _ := invokeSecret(t, h, h.secret, tc.args...)
			if code != 2 || out != "" {
				t.Fatalf("%v = %d, %q", tc.args, code, out)
			}
		})
	}
}
