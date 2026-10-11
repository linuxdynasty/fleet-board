package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/linuxdynasty/fleet-board/internal/agent/doctor"
)

func fixed(s doctor.Status) doctor.Check {
	return doctor.Check{ID: "c_" + string(s), Title: "Check " + string(s), Run: func(context.Context, *doctor.Env) doctor.Outcome {
		return doctor.Outcome{Status: s, Reason: "because " + string(s)}
	}}
}

// runDoctor runs the doctor command with the given checks and returns what it
// printed and what it returned.
func runDoctor(t *testing.T, checks []doctor.Check, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	root := &cobra.Command{Use: "fleet-agent", SilenceUsage: true, SilenceErrors: true}
	dir := func() (string, error) { return t.TempDir(), nil }
	cfg := func() (string, error) { return t.TempDir(), nil }
	deps := doctorDeps{
		probeDir: func() (string, error) { return filepath.Join(t.TempDir(), "probe"), nil },
		tmux:     func(context.Context) (string, error) { return "tmux 3.4", nil },
		checks:   checks,
	}
	root.AddCommand(newDoctorCommand(dir, cfg, deps, &out))
	root.SetArgs(append([]string{"doctor"}, args...))
	err := root.ExecuteContext(t.Context())
	return out.String(), err
}

func TestDoctorExitsZeroUnlessACheckFailed(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		checks []doctor.Check
		failed bool
	}{
		{"ok warn unknown not_checked", []doctor.Check{fixed(doctor.OK), fixed(doctor.Warn), fixed(doctor.Unknown), fixed(doctor.NotChecked)}, false},
		{"one fail", []doctor.Check{fixed(doctor.OK), fixed(doctor.Fail)}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			out, err := runDoctor(t, tc.checks)
			var ce commandError
			if got := errors.As(err, &ce); got != tc.failed || (err != nil) != tc.failed {
				t.Errorf("err = %v, want failure %v", err, tc.failed)
			}
			if !strings.Contains(out, "because") {
				t.Errorf("the report goes to stdout: %q", out)
			}
		})
	}
}

func TestDoctorJSONIsOneObject(t *testing.T) {
	t.Parallel()
	out, err := runDoctor(t, []doctor.Check{fixed(doctor.OK), fixed(doctor.Fail)}, "--json")
	if err == nil {
		t.Error("a failed check must still exit 1 with --json")
	}
	var rep doctor.Report
	if jerr := json.Unmarshal([]byte(out), &rep); jerr != nil {
		t.Fatalf("not one JSON object: %v\n%s", jerr, out)
	}
	if len(rep.Checks) != 2 || rep.Summary.Fail != 1 {
		t.Errorf("report = %+v", rep)
	}
}

func TestDoctorTakesNoArguments(t *testing.T) {
	t.Parallel()
	secret := "fbe1_" + "SECRETSECRET"
	_, err := runDoctor(t, nil, secret)
	if err == nil || strings.Contains(err.Error(), "SECRETSECRET") {
		t.Errorf("err = %v, want a refusal that does not echo the argument", err)
	}
}

// The real list on a real enrolled state folder: what conn.Enroll wrote is
// what doctor reads back.
func TestDoctorReadsWhatEnrollWrote(t *testing.T) {
	t.Parallel()
	s := newStartup(t)
	var out bytes.Buffer
	root := &cobra.Command{Use: "fleet-agent", SilenceUsage: true, SilenceErrors: true}
	deps := doctorDeps{
		probeDir: func() (string, error) { return filepath.Join(t.TempDir(), "probe"), nil },
		tmux:     func(context.Context) (string, error) { return "tmux 3.4", nil },
	}
	root.AddCommand(newDoctorCommand(func() (string, error) { return s.dir, nil }, func() (string, error) { return s.cfg, nil }, deps, &out))
	root.SetArgs([]string{"doctor", "--json"})
	_ = root.ExecuteContext(t.Context())
	var rep doctor.Report
	if err := json.Unmarshal(out.Bytes(), &rep); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	got := map[string]doctor.Result{}
	for _, c := range rep.Checks {
		got[c.ID] = c
	}
	for id, want := range map[string]doctor.Status{
		"state_dir": doctor.OK, "identity": doctor.OK, "cert_expiry": doctor.OK, "cert_file": doctor.OK,
		"renew_key": doctor.OK, "policy_file": doctor.OK, "enroll": doctor.OK, "tmux": doctor.OK,
		"status_file": doctor.Unknown, "connection": doctor.Unknown,
	} {
		if got[id].Status != want {
			t.Errorf("%s = %s (%s), want %s", id, got[id].Status, got[id].Reason, want)
		}
	}
	if !strings.Contains(got["identity"].Reason, s.machine) {
		t.Errorf("identity reason %q does not name machine %s", got["identity"].Reason, s.machine)
	}
	// Doctor read the folder and changed nothing in it.
	if _, err := os.Stat(filepath.Join(s.dir, "status.json")); err == nil {
		t.Error("doctor wrote status.json")
	}
}
