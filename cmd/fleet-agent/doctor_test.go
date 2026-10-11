package main_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A machine that was never set up: doctor says so, line by line, and exits 1.
func TestDoctorOnAnEmptyHomeFailsWithAFixForEachProblem(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	r := agentIn(t, home, nil, "doctor")
	if r.code != 1 {
		t.Fatalf("exit = %d, want 1; stdout %q stderr %q", r.code, r.stdout, r.stderr)
	}
	for _, want := range []string{
		filepath.Join(home, ".local", "state", "fleet-agent"), filepath.Join(home, ".config", "fleet-agent"),
		"[fail]", "not enrolled", "fleet-agent enroll --token", "[not_checked]", "(row 8)", "[unknown]",
	} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("stdout lacks %q:\n%s", want, r.stdout)
		}
	}
	if !strings.Contains(r.stderr, "check(s) failed") {
		t.Errorf("stderr = %q", r.stderr)
	}
	// Doctor made nothing: no folder appeared.
	if _, err := os.Stat(filepath.Join(home, ".local")); err == nil {
		t.Error("doctor created a folder")
	}
}

func TestDoctorJSONAndFlagsAreHonoured(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	state, cfg := filepath.Join(home, "s"), filepath.Join(home, "c")
	r := agentIn(t, home, nil, "doctor", "--json", "--state-dir", state, "--config-dir", cfg)
	var rep struct {
		StateDir  string `json:"state_dir"`
		ConfigDir string `json:"config_dir"`
		Checks    []struct{ ID, Status string }
	}
	if err := json.Unmarshal([]byte(r.stdout), &rep); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, r.stdout)
	}
	if r.code != 1 || rep.StateDir != state || rep.ConfigDir != cfg || len(rep.Checks) != 20 {
		t.Errorf("exit %d, folders %q %q, %d checks", r.code, rep.StateDir, rep.ConfigDir, len(rep.Checks))
	}
}

func TestDoctorWithAnArgumentIsBadUsage(t *testing.T) {
	t.Parallel()
	r := agent(t, "doctor", "extra")
	if r.code != 2 || strings.Contains(r.stdout+r.stderr, "extra\n") {
		t.Errorf("exit %d, output %q %q; want 2 and no echo", r.code, r.stdout, r.stderr)
	}
}
