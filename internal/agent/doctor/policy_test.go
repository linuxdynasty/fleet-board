package doctor_test

import (
	"os"
	"strings"
	"testing"

	"github.com/linuxdynasty/fleet-board/internal/agent/doctor"
	"github.com/linuxdynasty/fleet-board/internal/agent/statusfile"
)

func (r *rig) policy(content string, mode os.FileMode) { r.write(r.cfg, "policy.yaml", content, mode) }

func TestPolicyFile(t *testing.T) {
	t.Parallel()
	t.Run("missing is a normal first run", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		r.want("policy_file", doctor.OK, "no policy.yaml", "built-in defaults")
	})
	t.Run("valid", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		r.policy("schema_version: \"1\"\nmax_active: 2\n", 0o644)
		r.want("policy_file", doctor.OK, "valid", "schema_version 1", "hash ")
	})
	t.Run("safe mode names the line and the key, never file text", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		secret := "tok-" + "SECRET-VALUE"
		r.policy("schema_version: \"1\"\nsurprise_key: "+secret+"\n", 0o644)
		c := r.want("policy_file", doctor.Fail, "safe mode", "line 2", "unknown_key")
		if strings.Contains(c.Reason+c.Fix, secret) {
			t.Errorf("file text leaked: %+v", c)
		}
	})
	t.Run("an unknown schema is safe mode", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		r.policy("schema_version: \"2\"\n", 0o644)
		r.want("policy_file", doctor.Fail, "safe mode", "schema_version")
	})
	t.Run("a file others can write could loosen the ceiling", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		r.policy("schema_version: \"1\"\n", 0o666)
		r.want("policy_file", doctor.Fail, "0666", "chmod go-w")
	})
	t.Run("a folder named policy.yaml is unreadable, not a hang", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		if err := os.Mkdir(r.cfg+"/policy.yaml", 0o700); err != nil {
			t.Fatal(err)
		}
		r.want("policy_file", doctor.Fail, "safe mode", "unreadable")
	})
}

func TestPolicyDefaultsInForce(t *testing.T) {
	t.Parallel()
	t.Run("roots empty by default", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		r.want("policy_roots", doctor.Warn, "allowed_repo_roots is empty", "no repo", "allowed_repo_roots")
	})
	t.Run("roots set", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		root := t.TempDir()
		r.policy("allowed_repo_roots: ["+root+"]\n", 0o644)
		r.want("policy_roots", doctor.OK, root)
	})
	t.Run("roots in safe mode are unknown", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		r.policy("bogus: 1\n", 0o644)
		r.want("policy_roots", doctor.Unknown, "safe mode")
	})
	t.Run("harnesses with no opinion", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		r.want("policy_harnesses", doctor.OK, "every harness", "row 8")
	})
	t.Run("harnesses listed", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		r.policy("allowed_harnesses: [claude, codex]\n", 0o644)
		r.want("policy_harnesses", doctor.OK, "claude, codex")
	})
	t.Run("an explicit empty list allows none", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		r.policy("allowed_harnesses: []\n", 0o644)
		r.want("policy_harnesses", doctor.Warn, "no harness")
	})
}

func TestPolicyWatch(t *testing.T) {
	t.Parallel()
	t.Run("off", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		r.writeStatus(0, func(s *statusfile.Status) { s.PolicyWatch = statusfile.PolicyWatchOff })
		r.want("policy_watch", doctor.Warn, "off", "inotify")
	})
	t.Run("working", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		r.writeStatus(0, nil)
		r.want("policy_watch", doctor.OK, "no problem")
	})
	t.Run("no status file", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		r.want("policy_watch", doctor.Unknown, "status.json")
	})
	t.Run("an old status file says nothing about now", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		r.writeStatus(twoHours, nil)
		r.want("policy_watch", doctor.Unknown, "not running")
	})
}
