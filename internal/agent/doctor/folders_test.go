package doctor_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/linuxdynasty/fleet-board/internal/agent/doctor"
)

func TestStateFolder(t *testing.T) {
	t.Parallel()
	t.Run("private and ours is ok", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		r.want("state_dir", doctor.OK, r.state, "0700")
	})
	t.Run("missing", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		if err := os.Remove(r.state); err != nil {
			t.Fatal(err)
		}
		r.want("state_dir", doctor.Fail, "does not exist", "fleet-agent enroll")
	})
	t.Run("open to other users", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		if err := os.Chmod(r.state, 0o755); err != nil { //nolint:gosec // G302: the point of the test
			t.Fatal(err)
		}
		r.want("state_dir", doctor.Fail, "0755", "chmod 700")
	})
	t.Run("owned by another user", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		r.getuid = func() int { return os.Getuid() + 1 }
		r.want("state_dir", doctor.Fail, "owned by", "another user")
	})
	t.Run("a file, not a folder", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		if err := os.Remove(r.state); err != nil {
			t.Fatal(err)
		}
		r.write(filepath.Dir(r.state), "state", "x", 0o600)
		r.want("state_dir", doctor.Fail, "not a folder")
	})
	t.Run("a link to a folder is refused like run does", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		real := filepath.Join(filepath.Dir(r.state), "real")
		if err := os.Rename(r.state, real); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(real, r.state); err != nil {
			t.Fatal(err)
		}
		r.want("state_dir", doctor.Fail, "not a folder")
	})
	t.Run("a path with a space and a quote is quoted in the fix", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		odd := filepath.Join(filepath.Dir(r.state), "it's here")
		if err := os.Rename(r.state, odd); err != nil {
			t.Fatal(err)
		}
		r.state = odd
		if err := os.Chmod(odd, 0o750); err != nil { //nolint:gosec // G302: the point of the test
			t.Fatal(err)
		}
		c := r.want("state_dir", doctor.Fail, "chmod 700 ")
		if want := `chmod 700 '` + odd[:len(odd)-len("it's here")] + `it'\''s here'`; !strings.Contains(c.Fix, want) {
			t.Errorf("fix = %q, want it to contain %q", c.Fix, want)
		}
	})
}

func TestConfigFolder(t *testing.T) {
	t.Parallel()
	t.Run("the folder used is printed", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		r.want("config_dir", doctor.OK, r.cfg)
	})
	t.Run("missing is normal before the first enroll or run", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		if err := os.Remove(r.cfg); err != nil {
			t.Fatal(err)
		}
		r.want("config_dir", doctor.OK, r.cfg, "does not exist yet", "enroll")
	})
	t.Run("others can write it", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		if err := os.Chmod(r.cfg, 0o777); err != nil { //nolint:gosec // G302: the point of the test
			t.Fatal(err)
		}
		r.want("config_dir", doctor.Fail, "0777", "chmod go-w")
	})
	t.Run("readable by others is the owner's choice", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		if err := os.Chmod(r.cfg, 0o755); err != nil { //nolint:gosec // G302: the point of the test
			t.Fatal(err)
		}
		r.want("config_dir", doctor.OK)
	})
	t.Run("owned by another user", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		r.getuid = func() int { return os.Getuid() + 1 }
		r.want("config_dir", doctor.Fail, "owned by")
	})
}

func TestProbeFolder(t *testing.T) {
	t.Parallel()
	t.Run("missing", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		r.want("probe_folder", doctor.Warn, r.probe, "does not exist", "mkdir -p -m 700")
	})
	t.Run("empty and private", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		if err := os.Mkdir(r.probe, 0o700); err != nil {
			t.Fatal(err)
		}
		r.want("probe_folder", doctor.OK, "empty")
	})
	t.Run("not empty", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		if err := os.Mkdir(r.probe, 0o700); err != nil {
			t.Fatal(err)
		}
		r.write(r.probe, "notes.txt", "x", 0o600)
		c := r.want("probe_folder", doctor.Warn, "not empty")
		if strings.Contains(c.Reason, "notes.txt") {
			t.Errorf("a name found in the folder was printed: %q", c.Reason)
		}
	})
	t.Run("open to other users", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		if err := os.Mkdir(r.probe, 0o755); err != nil { //nolint:gosec // G301: the point of the test
			t.Fatal(err)
		}
		if err := os.Chmod(r.probe, 0o755); err != nil { //nolint:gosec // G302: the point of the test
			t.Fatal(err)
		}
		r.want("probe_folder", doctor.Warn, "0755", "chmod 700")
	})
}
