package release_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/linuxdynasty/fleet-board/internal/release"
)

// The swap replaces the installed binary with the verified archive. A bad
// archive installs nothing: the old binary stays byte for byte the same.

func pack(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(body))}); err != nil {
			t.Fatalf("tar header: %v", err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatalf("tar write: %v", err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("tar close: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	return buf.Bytes()
}

func TestSwapInstallsTheBinary(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	old := []byte("old-binary")
	if err := os.WriteFile(filepath.Join(dir, "fleetd"), old, 0o600); err != nil {
		t.Fatalf("seed old binary: %v", err)
	}
	s := release.FileSwapper{Dir: dir}
	if err := s.Swap(pack(t, map[string]string{"fleetd": "new-binary"}), "fleetd"); err != nil {
		t.Fatalf("Swap: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "fleetd")) //nolint:gosec // G304: the test's own t.TempDir file
	if err != nil {
		t.Fatalf("read swapped binary: %v", err)
	}
	if string(got) != "new-binary" {
		t.Errorf("swapped binary = %q, want %q", got, "new-binary")
	}
	info, err := os.Stat(filepath.Join(dir, "fleetd"))
	if err != nil {
		t.Fatalf("stat swapped binary: %v", err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Errorf("swapped binary mode = %o, want 755", info.Mode().Perm())
	}
}

// badArchive builds the archive for one refusal case.
func badArchive(t *testing.T, kind string) []byte {
	t.Helper()
	switch kind {
	case "empty":
		return nil
	case "not-archive":
		return []byte("hello")
	case "two-files":
		return pack(t, map[string]string{"fleetd": "x", "extra": "y"})
	case "wrong-name":
		return pack(t, map[string]string{"fleet-agent": "x"})
	case "absolute":
		return pack(t, map[string]string{"/tmp/fleetd": "x"})
	case "parent":
		return pack(t, map[string]string{"../fleetd": "x"})
	case "nested":
		return pack(t, map[string]string{"bin/fleetd": "x"})
	case "unknown-program":
		return pack(t, map[string]string{"fleet-evil": "x"})
	default:
		t.Fatalf("unknown archive kind %q", kind)
		return nil
	}
}

func TestSwapRefusalsLeaveTheOldBinary(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		program string
		kind    string
	}{
		{name: "unknown program", program: "fleet-evil", kind: "unknown-program"},
		{name: "empty archive", program: "fleetd", kind: "empty"},
		{name: "not an archive", program: "fleetd", kind: "not-archive"},
		{name: "two files", program: "fleetd", kind: "two-files"},
		{name: "wrong name", program: "fleetd", kind: "wrong-name"},
		{name: "absolute path", program: "fleetd", kind: "absolute"},
		{name: "parent escape", program: "fleetd", kind: "parent"},
		{name: "nested path", program: "fleetd", kind: "nested"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			old := []byte("old-binary")
			if err := os.WriteFile(filepath.Join(dir, "fleetd"), old, 0o600); err != nil {
				t.Fatalf("seed old binary: %v", err)
			}
			s := release.FileSwapper{Dir: dir}
			if err := s.Swap(badArchive(t, tc.kind), tc.program); err == nil {
				t.Fatal("Swap succeeded, want an error")
			}
			got, err := os.ReadFile(filepath.Join(dir, "fleetd")) //nolint:gosec // G304: the test's own t.TempDir file
			if err != nil {
				t.Fatalf("read old binary: %v", err)
			}
			if string(got) != string(old) {
				t.Errorf("old binary changed to %q after a refused swap", got)
			}
		})
	}
}

func TestServiceRestartCommands(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name        string
		goos        string
		program     string
		wantPath    string
		wantService string
		wantErr     bool
	}{
		{name: "mac agent", goos: "darwin", program: "fleet-agent", wantPath: "launchctl", wantService: "com.fleet-board.fleet-agent"},
		{name: "mac hub", goos: "darwin", program: "fleetd", wantPath: "launchctl", wantService: "com.fleet-board.fleetd"},
		{name: "linux agent", goos: "linux", program: "fleet-agent", wantPath: "systemctl", wantService: "fleet-agent.service"},
		{name: "linux hub", goos: "linux", program: "fleetd", wantPath: "systemctl", wantService: "fleetd.service"},
		{name: "unknown system is refused", goos: "windows", program: "fleetd", wantErr: true},
		{name: "unknown program is refused", goos: "linux", program: "fleet", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var gotPath string
			var gotArgs []string
			r := release.ServiceRestarter{
				GOOS: tc.goos,
				Run: func(_ context.Context, path string, args ...string) error {
					gotPath, gotArgs = path, args
					return nil
				},
			}
			err := r.Restart(tc.program)
			if tc.wantErr {
				if err == nil {
					t.Fatal("Restart succeeded, want an error")
				}
				return
			}
			if err != nil {
				t.Fatalf("Restart: %v", err)
			}
			if gotPath != tc.wantPath {
				t.Errorf("restart path = %q, want %q", gotPath, tc.wantPath)
			}
			found := false
			for _, a := range gotArgs {
				if a == tc.wantService || containsService(a, tc.wantService) {
					found = true
				}
			}
			if !found {
				t.Errorf("restart args = %v, want them to name %q", gotArgs, tc.wantService)
			}
		})
	}
}

func containsService(arg, service string) bool {
	return len(arg) >= len(service) && (arg == service || (len(arg) > len(service) && arg[len(arg)-len(service):] == service))
}
