package machine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
)

func TestTimezoneFromTheLocaltimeLink(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		target  string
		want    string
		wantErr bool
	}{
		{"linux", "/usr/share/zoneinfo/America/New_York", "America/New_York", false},
		{"macos", "/var/db/timezone/zoneinfo/Europe/Berlin", "Europe/Berlin", false},
		{"relative link", "../usr/share/zoneinfo/Etc/UTC", "Etc/UTC", false},
		{"signed zone name", "/usr/share/zoneinfo/Etc/GMT+5", "Etc/GMT+5", false},
		{"no zoneinfo in the path", "/etc/custom-time", "", true},
		{"empty name", "/usr/share/zoneinfo/", "", true},
		{"dot dot", "/usr/share/zoneinfo/../../etc/passwd", "", true},
		{"control character", "/usr/share/zoneinfo/Amer\nica", "", true},
		{"space", "/usr/share/zoneinfo/New York", "", true},
		{"too long", "/usr/share/zoneinfo/" + string(make([]byte, 80)), "", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := zoneFromLink(tc.target)
			if (err != nil) != tc.wantErr || got != tc.want {
				t.Fatalf("zoneFromLink(%q) = %q, %v; want %q, err=%v", tc.target, got, err, tc.want, tc.wantErr)
			}
		})
	}
}

func TestTimezoneFallsBackToEtcTimezone(t *testing.T) {
	t.Parallel()
	notLink := func(string) (string, error) { return "", errors.New("invalid argument") }
	file := func(text string, err error) func(string) ([]byte, error) {
		return func(p string) ([]byte, error) {
			if p != "/etc/timezone" {
				return nil, fs.ErrNotExist
			}
			return []byte(text), err
		}
	}
	if got, err := readTimezone(notLink, file("Asia/Tokyo\n", nil)); err != nil || got != "Asia/Tokyo" {
		t.Fatalf("got %q, %v", got, err)
	}
	if _, err := readTimezone(notLink, file("", fs.ErrNotExist)); err == nil {
		t.Fatal("no link and no /etc/timezone must be an error, not an empty answer")
	}
	if _, err := readTimezone(notLink, file("not a zone\n", nil)); !errors.Is(err, errParse) {
		t.Fatalf("a bad /etc/timezone = %v, want errParse", err)
	}
	link := func(string) (string, error) { return "/usr/share/zoneinfo/UTC", nil }
	if got, err := readTimezone(link, file("Asia/Tokyo\n", nil)); err != nil || got != "UTC" {
		t.Fatalf("the link wins: got %q, %v", got, err)
	}
}

func TestParseOSRelease(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		in      string
		want    string
		wantErr bool
	}{
		{"ubuntu", "NAME=\"Ubuntu\"\nVERSION_ID=\"24.04\"\nID=ubuntu\n", "ubuntu 24.04", false},
		{"debian unquoted", "ID=debian\nVERSION_ID=12\n", "debian 12", false},
		{"rolling has no version", "ID=arch\nNAME=\"Arch Linux\"\n", "arch", false},
		{"single quotes", "ID='fedora'\nVERSION_ID='40'\n", "fedora 40", false},
		{"comments and blanks", "# x\n\nID=alpine\nVERSION_ID=3.20.1\n", "alpine 3.20.1", false},
		{"no id", "VERSION_ID=1\n", "", true},
		{"empty", "", "", true},
		{"odd characters", "ID=\"a b\"\nVERSION_ID=1\n", "", true},
		{"control character", "ID=ub\x1bntu\n", "", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := parseOSRelease(tc.in)
			if (err != nil) != tc.wantErr || got != tc.want {
				t.Fatalf("parseOSRelease = %q, %v; want %q, err=%v", got, err, tc.want, tc.wantErr)
			}
		})
	}
}

func TestParseTmuxVersion(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		in      string
		want    string
		wantErr bool
	}{
		{"release", "tmux 3.7c\n", "tmux 3.7c", false},
		{"plain", "tmux 3.2", "tmux 3.2", false},
		{"development build", "tmux next-3.7\n", "tmux next-3.7", false},
		{"openbsd", "tmux openbsd-7.5\n", "tmux openbsd-7.5", false},
		{"first line only", "tmux 3.4\nsecond line\n", "tmux 3.4", false},
		{"empty", "", "", true},
		{"another program", "screen 4.09\n", "", true},
		{"control characters", "tmux 3.\x1b[2J\n", "", true},
		{"too long", "tmux " + string(make([]byte, 100)), "", true},
		{"spaces in the version", "tmux 3 4\n", "", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := parseTmuxVersion([]byte(tc.in))
			if (err != nil) != tc.wantErr || got != tc.want {
				t.Fatalf("parseTmuxVersion = %q, %v; want %q, err=%v", got, err, tc.want, tc.wantErr)
			}
		})
	}
}

func TestValidUsername(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]bool{
		"alice": true, "svc_agent": true, "a.b-c": true, "user$": true, "r00t": true,
		"": false, "a b": false, "a@b": false, `DOM\user`: false, "a\nb": false, "-x": false,
	} {
		if got := validUsername(in); got != want {
			t.Errorf("validUsername(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestHashFile(t *testing.T) {
	t.Parallel()
	p := filepath.Join(t.TempDir(), "bin")
	body := []byte("not really a binary")
	if err := os.WriteFile(p, body, 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(body)
	got, err := hashFile(p)
	if err != nil || got != hex.EncodeToString(sum[:]) {
		t.Fatalf("hashFile = %q, %v; want %x", got, err, sum)
	}
	if _, err := hashFile(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("a missing file must be an error")
	}
}

func fakeSystem() system {
	return system{
		readlink:   func(string) (string, error) { return "/usr/share/zoneinfo/Europe/Paris", nil },
		readFile:   func(string) ([]byte, error) { return nil, fs.ErrNotExist },
		exists:     func(p string) bool { return p == "/.dockerenv" },
		osVersion:  func() (string, error) { return "ubuntu 24.04", nil },
		memTotal:   func(context.Context) (uint64, error) { return 8 << 30, nil },
		username:   func() (string, error) { return "tester", nil },
		binaryHash: func() (string, error) { return "abc123", nil },
		tmux:       func(context.Context) (string, error) { return "tmux 3.7c", nil },
	}
}

func newTestCollector(t *testing.T, s system) *Collector {
	t.Helper()
	m, err := NewMetrics(nil)
	if err != nil {
		t.Fatal(err)
	}
	return newCollector(t.Context(), s, m)
}

func TestFillSetsEveryB1FactAndKeepsWhatItWasGiven(t *testing.T) {
	t.Parallel()
	c := newTestCollector(t, fakeSystem())
	f := &agentv1.MachineFacts{Hostname: "h", Os: "linux", BootId: "boot-9", CpuCount: 4}
	c.Fill(t.Context(), f)
	if f.GetOsVersion() != "ubuntu 24.04" || f.GetRamBytes() != 8<<30 || f.GetTimezone() != "Europe/Paris" ||
		f.GetAgentBinaryHash() != "abc123" || f.GetTmuxVersion() != "tmux 3.7c" || f.GetUsername() != "tester" || !f.GetContainer() {
		t.Fatalf("facts = %v", f)
	}
	if f.GetHostname() != "h" || f.GetBootId() != "boot-9" || f.GetCpuCount() != 4 || f.GetOs() != "linux" {
		t.Fatalf("Fill changed a fact it does not own: %v", f)
	}
}

// A fact that cannot be read stays empty, not a guess, and a later Fill tries
// again; a static fact that was read is not read twice.
func TestFillLeavesAFailedFactEmptyAndRetriesIt(t *testing.T) {
	t.Parallel()
	s := fakeSystem()
	hashCalls, memCalls := 0, 0
	fail := true
	s.binaryHash = func() (string, error) {
		hashCalls++
		if fail {
			return "", fs.ErrPermission
		}
		return "def456", nil
	}
	s.memTotal = func(context.Context) (uint64, error) { memCalls++; return 8 << 30, nil }
	s.tmux = func(context.Context) (string, error) { return "", errParse }
	c := newTestCollector(t, s)
	f := &agentv1.MachineFacts{}
	c.Fill(t.Context(), f)
	if f.GetAgentBinaryHash() != "" || f.GetTmuxVersion() != "" {
		t.Fatalf("a failed reading must stay empty: hash %q tmux %q", f.GetAgentBinaryHash(), f.GetTmuxVersion())
	}
	fail = false
	f2 := &agentv1.MachineFacts{}
	c.Fill(t.Context(), f2)
	if f2.GetAgentBinaryHash() != "def456" {
		t.Fatalf("hash after recovery = %q", f2.GetAgentBinaryHash())
	}
	c.Fill(t.Context(), &agentv1.MachineFacts{})
	if memCalls != 1 {
		t.Fatalf("RAM was read %d times, want once (a static fact)", memCalls)
	}
	// NewCollector reads it (fails), Fill 1 retries (fails), Fill 2 retries
	// (works), Fill 3 does not read it again.
	if hashCalls != 3 {
		t.Fatalf("hash reads = %d, want 3", hashCalls)
	}
}

func TestFillDoesNotTouchAFactItCannotReadTwice(t *testing.T) {
	t.Parallel()
	s := fakeSystem()
	reads := 0
	s.tmux = func(context.Context) (string, error) { reads++; return "tmux 3.7c", nil }
	c := newTestCollector(t, s)
	for range 3 {
		c.Fill(t.Context(), &agentv1.MachineFacts{})
	}
	if reads != 3 {
		t.Fatalf("tmux read %d times, want once per Fill (it can be installed later)", reads)
	}
}

func TestContainerIsFalseWithoutAMarker(t *testing.T) {
	t.Parallel()
	s := fakeSystem()
	s.exists = func(string) bool { return false }
	f := &agentv1.MachineFacts{}
	newTestCollector(t, s).Fill(t.Context(), f)
	if f.GetContainer() {
		t.Fatal("container is true without /.dockerenv or /run/.containerenv")
	}
}

// A zone that cannot be read leaves timezone empty; it is not guessed as UTC.
func TestFillLeavesTheTimezoneEmptyWhenItCannotBeRead(t *testing.T) {
	t.Parallel()
	s := fakeSystem()
	s.readlink = func(string) (string, error) { return "", fs.ErrNotExist }
	f := &agentv1.MachineFacts{Timezone: "stale"}
	newTestCollector(t, s).Fill(t.Context(), f)
	if f.GetTimezone() != "" {
		t.Fatalf("timezone = %q, want empty", f.GetTimezone())
	}
}

// Either marker file makes the machine a container.
func TestContainerFollowsEitherMarkerFile(t *testing.T) {
	t.Parallel()
	for _, marker := range []string{"/.dockerenv", "/run/.containerenv"} {
		s := fakeSystem()
		s.exists = func(p string) bool { return p == marker }
		f := &agentv1.MachineFacts{}
		newTestCollector(t, s).Fill(t.Context(), f)
		if !f.GetContainer() {
			t.Errorf("container is false with %s present", marker)
		}
	}
}
