package statusfile

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/agent/conn"
)

var t0 = time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)

func keys(t *testing.T, s Status) map[string]json.RawMessage {
	t.Helper()
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestFromConnLeavesUnsetValuesOut(t *testing.T) {
	t.Parallel()
	m := keys(t, FromConn(conn.Status{}, t0))
	for _, k := range []string{"state", "peer", "failures", "clock", "ended", "snapshot_oversize_time", "browse"} {
		if _, ok := m[k]; ok {
			t.Errorf("key %q present for a status with nothing set; unknown must be left out, never 0", k)
		}
	}
	for _, k := range []string{"version", "written_time", "push_off"} {
		if _, ok := m[k]; !ok {
			t.Errorf("key %q missing", k)
		}
	}
	if string(m["written_time"]) != `"2026-10-10T12:00:00Z"` {
		t.Errorf("written_time = %s", m["written_time"])
	}
}

func TestFromConnCarriesEveryEntry(t *testing.T) {
	t.Parallel()
	in := conn.Status{
		State: conn.StateConnected, Peer: "hub.local:7443",
		Failures: map[string]conn.Failure{"10.0.0.1:7443": {Step: "dial", ErrKind: "refused", Time: t0.Add(-time.Minute)}},
		PushOff:  true, ClockOffset: -1500 * time.Millisecond, ClockOffsetTime: t0.Add(-time.Second),
		Ended: conn.EndRevoked, SnapshotOversize: t0.Add(-time.Hour),
		Browse: conn.BrowseStatus{
			Time: t0.Add(-time.Minute), Elapsed: 2500 * time.Millisecond, Kept: 2, Dropped: 3,
			DroppedBy: map[string]int{"bad_port": 3},
		},
	}
	got := FromConn(in, t0)
	if got.State != "connected" || got.Peer != "hub.local:7443" || got.Ended != "revoked" {
		t.Errorf("state/peer/ended = %q %q %q", got.State, got.Peer, got.Ended)
	}
	f, ok := got.Failures["10.0.0.1:7443"]
	if !ok || f.Step != "dial" || f.ErrKind != "refused" || f.Time == nil || !f.Time.Equal(t0.Add(-time.Minute)) {
		t.Errorf("failure = %+v", f)
	}
	if !got.PushOff {
		t.Error("push_off = false, want true")
	}
	if got.Clock == nil || got.Clock.OffsetMS != -1500 || !got.Clock.Time.Equal(t0.Add(-time.Second)) {
		t.Errorf("clock = %+v", got.Clock)
	}
	if got.SnapshotOversizeTime == nil || !got.SnapshotOversizeTime.Equal(t0.Add(-time.Hour)) {
		t.Errorf("snapshot_oversize_time = %v", got.SnapshotOversizeTime)
	}
	b := got.Browse
	if b == nil || b.Result != BrowseFound || b.ElapsedMS != 2500 || b.Kept != 2 || b.Dropped != 3 || b.DroppedBy["bad_port"] != 3 {
		t.Errorf("browse = %+v", b)
	}
}

func TestClockOffsetOfZeroIsKnownButNeverSetIsOut(t *testing.T) {
	t.Parallel()
	got := FromConn(conn.Status{ClockOffsetTime: t0}, t0)
	if got.Clock == nil || got.Clock.OffsetMS != 0 {
		t.Errorf("a measured offset of 0 ms must be kept: %+v", got.Clock)
	}
}

func TestBrowseResultIsOneOfThree(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		in   conn.BrowseStatus
		want string
	}{
		{"found", conn.BrowseStatus{Time: t0, Kept: 1}, BrowseFound},
		{"none", conn.BrowseStatus{Time: t0, Dropped: 2}, BrowseNone},
		{"none with nothing seen", conn.BrowseStatus{Time: t0}, BrowseNone},
		{"no interface", conn.BrowseStatus{Time: t0, ErrKind: "io"}, BrowseNoMulticastInterface},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			b := FromConn(conn.Status{Browse: tc.in}, t0).Browse
			if b == nil || b.Result != tc.want {
				t.Fatalf("browse = %+v, want result %q", b, tc.want)
			}
		})
	}
}

func TestFromConnCleansAndBoundsEveryString(t *testing.T) {
	t.Parallel()
	failures := map[string]conn.Failure{}
	for i := range 100 {
		failures["10.0.0."+strings.Repeat("1", i%5)+":"+string(rune('a'+i%26))+strings.Repeat("x", i)] = conn.Failure{
			Step: "dial", ErrKind: "io", Time: t0.Add(time.Duration(i) * time.Second),
		}
	}
	failures["evil\x1b[31m host\n"] = conn.Failure{Step: "Bad Step\x1b[0m", ErrKind: strings.Repeat("a", 500), Time: t0}
	in := conn.Status{State: "weird state", Ended: "nope", Peer: "a b\x00c", Failures: failures}
	got := FromConn(in, t0)
	if got.State != StateUnknown || got.Ended != StateUnknown {
		t.Errorf("state/ended = %q/%q, want the closed value %q for an unknown one", got.State, got.Ended, StateUnknown)
	}
	if len(got.Failures) > maxFailures {
		t.Errorf("%d failures kept, cap is %d", len(got.Failures), maxFailures)
	}
	all := []string{got.Peer, got.State, got.Ended}
	for a, f := range got.Failures {
		all = append(all, a, f.Step, f.ErrKind)
	}
	for _, v := range all {
		for _, r := range v {
			if r < ' ' || r == 0x7f || r == ' ' {
				t.Errorf("control or space character %q survived in %q", r, v)
			}
		}
	}
	for addr, f := range got.Failures {
		if len(addr) > maxAddr || len(f.Step) > maxToken || len(f.ErrKind) > maxToken {
			t.Errorf("unbounded: addr %d step %d kind %d", len(addr), len(f.Step), len(f.ErrKind))
		}
	}
}

func TestFromConnReducesAHostileFailureToClosedWords(t *testing.T) {
	t.Parallel()
	in := conn.Status{Failures: map[string]conn.Failure{
		"evil\x1b[31m host\n:7443": {Step: "Bad Step\x1b[0m", ErrKind: strings.Repeat("a", 500), Time: t0},
	}}
	got := FromConn(in, t0).Failures
	f, ok := got["evil[31mhost:7443"]
	if len(got) != 1 || !ok || f.Step != "other" || f.ErrKind != "other" {
		t.Errorf("failures = %+v, want one entry, the address reduced to its host characters and both words \"other\"", got)
	}
}

func TestFromConnKeepsTheNewestFailuresWhenOverTheCap(t *testing.T) {
	t.Parallel()
	failures := map[string]conn.Failure{}
	for i := range maxFailures + 10 {
		failures["h"+string(rune('a'+i%26))+string(rune('a'+i/26))+":1"] = conn.Failure{Step: "dial", ErrKind: "io", Time: t0.Add(time.Duration(i) * time.Second)}
	}
	got := FromConn(conn.Status{Failures: failures}, t0)
	if len(got.Failures) != maxFailures {
		t.Fatalf("kept %d, want %d", len(got.Failures), maxFailures)
	}
	for addr, f := range got.Failures {
		if f.Time.Before(t0.Add(10 * time.Second)) {
			t.Errorf("kept an older failure %s at %v over a newer one", addr, f.Time)
		}
	}
}

func write(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestReadRoundTripsAndIgnoresUnknownKeys(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	want := FromConn(conn.Status{State: conn.StateBackoff, PushOff: true}, t0)
	b, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	// A key a later row adds (policy_watch, row 6b) must not break an older reader.
	withExtra := strings.Replace(string(b), "{", `{"policy_watch":"off","future":{"a":1},`, 1)
	write(t, dir, StatusFile, withExtra)
	got, err := Read(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != "backoff" || !got.PushOff || !got.WrittenTime.Equal(t0) {
		t.Errorf("read = %+v", got)
	}
}

func TestReadErrors(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if _, err := Read(dir); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("missing file: err = %v, want fs.ErrNotExist", err)
	}
	for _, tc := range []struct {
		name, content string
		want          error
	}{
		{"damaged", `{"version":1,`, ErrDamaged},
		{"empty", ``, ErrDamaged},
		{"other version", `{"version":2,"written_time":"2026-10-10T12:00:00Z"}`, ErrVersion},
		{"no version", `{"written_time":"2026-10-10T12:00:00Z"}`, ErrVersion},
		{"no time", `{"version":1}`, ErrDamaged},
		{"too big", `{"version":1,"peer":"` + strings.Repeat("a", maxFile) + `"}`, ErrDamaged},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := t.TempDir()
			write(t, d, StatusFile, tc.content)
			if _, err := Read(d); !errors.Is(err, tc.want) {
				t.Errorf("err = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestReadRefusesASymlink(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	target := filepath.Join(t.TempDir(), "elsewhere")
	write(t, filepath.Dir(target), "elsewhere", `{"version":1,"written_time":"2026-10-10T12:00:00Z"}`)
	if err := os.Symlink(target, filepath.Join(dir, StatusFile)); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(dir); !errors.Is(err, ErrDamaged) {
		t.Errorf("err = %v, want ErrDamaged for a symlink", err)
	}
}

func TestReadCleansTextAFileCarries(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	write(t, dir, StatusFile, `{"version":1,"written_time":"2026-10-10T12:00:00Z","state":"\u001b[2Jhacked",`+
		`"peer":"hub\u001b]0;x\u0007","failures":{"h\u001b:1":{"step":"dial\n","err_kind":"io"}}}`)
	got, err := Read(dir)
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if strings.ContainsAny(string(b), "\x1b\x07") || strings.Contains(string(b), `\u001b`) || strings.Contains(string(b), `\u0007`) {
		t.Errorf("control characters survived the read: %s", b)
	}
	if got.State != StateUnknown {
		t.Errorf("state = %q, want %q", got.State, StateUnknown)
	}
}

func TestAge(t *testing.T) {
	t.Parallel()
	s := FromConn(conn.Status{}, t0)
	if got := s.Age(t0.Add(90 * time.Second)); got != 90*time.Second {
		t.Errorf("age = %v", got)
	}
	if got := s.Age(t0.Add(-time.Hour)); got != 0 {
		t.Errorf("age with a clock behind the file = %v, want 0", got)
	}
}
