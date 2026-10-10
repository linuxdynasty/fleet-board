package statusfile

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/conn"
	"github.com/linuxdynasty/fleet-board/internal/telemetry"
)

func TestEnrollCodeIsAClosedWord(t *testing.T) {
	t.Parallel()
	refused := &conn.RefusedError{Code: connect.CodePermissionDenied, Reason: typev1.ReasonCode_REASON_CODE_ENROLL_TOKEN_EXPIRED, Message: "free text\x1b[31m"}
	for _, tc := range []struct {
		name         string
		err          error
		code, reason string
	}{
		{"refused", fmt.Errorf("enroll: %w", refused), "refused", "ENROLL_TOKEN_EXPIRED"},
		{"untrusted", fmt.Errorf("x: %w", conn.ErrHubUntrusted), "hub_untrusted", ""},
		{"bad response", conn.ErrBadResponse, "bad_response", ""},
		{"hub failed", conn.ErrHubFailed, "hub_failed", ""},
		{"already", conn.ErrAlreadyEnrolled, "already_enrolled", ""},
		{"loose", conn.ErrLooseMode, "loose_mode", ""},
		{"token", conn.ErrTokenMalformed, "token_malformed", ""},
		{"no hub", conn.ErrNoHub, "no_hub", ""},
		{"address", conn.ErrHubAddress, "hub_address", ""},
		{"none found", conn.ErrNoHubFound, "no_hub_found", ""},
		{"deadline", context.DeadlineExceeded, "timeout", ""},
		{"canceled", context.Canceled, "interrupted", ""},
		{"anything else", errors.New("secret fbe1_token text"), "other", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			code, reason := EnrollCodeOf(tc.err)
			if code != tc.code || reason != tc.reason {
				t.Errorf("EnrollCodeOf = %q, %q; want %q, %q", code, reason, tc.code, tc.reason)
			}
		})
	}
}

func quietTel(t *testing.T) (*telemetry.Telemetry, *lockedBuilder) {
	t.Helper()
	logs := &lockedBuilder{}
	tel, err := telemetry.New(telemetry.Config{Service: telemetry.Agent, Version: "t", InstanceID: "b", LogOutput: logs})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tel.Shutdown(context.Background()) })
	return tel, logs
}

// stateDir is a state folder as EnsureDir accepts it (t.TempDir is 0755).
func stateDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestRecordEnrollWritesTheCodeAndNothingElse(t *testing.T) {
	t.Parallel()
	dir := stateDir(t)
	tel, _ := quietTel(t)
	refused := &conn.RefusedError{Code: connect.CodePermissionDenied, Reason: typev1.ReasonCode_REASON_CODE_ENROLL_TOKEN_EXPIRED, Message: "token fbe1_SECRET at /home/u"}
	if err := RecordEnroll(tel, dir, t0, fmt.Errorf("hub 10.1.2.3:7443: %w", refused)); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, EnrollFile)) //nolint:gosec // G304: a file in the test's temp folder
	if err != nil {
		t.Fatal(err)
	}
	for _, leak := range []string{"SECRET", "fbe1_", "10.1.2.3", "/home"} {
		if strings.Contains(string(raw), leak) {
			t.Errorf("enroll.json leaks %q:\n%s", leak, raw)
		}
	}
	got, err := ReadEnroll(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got.Code != "refused" || got.Reason != "ENROLL_TOKEN_EXPIRED" || !got.WrittenTime.Equal(t0) || got.Version != 1 {
		t.Errorf("enroll.json = %+v", got)
	}
	fi, _ := os.Stat(filepath.Join(dir, EnrollFile))
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("mode = %04o", fi.Mode().Perm())
	}
}

func TestRecordEnrollKeepsOnlyTheLastFailure(t *testing.T) {
	t.Parallel()
	dir := stateDir(t)
	tel, _ := quietTel(t)
	_ = RecordEnroll(tel, dir, t0, conn.ErrHubFailed)
	_ = RecordEnroll(tel, dir, t0.Add(time.Hour), conn.ErrNoHub)
	got, err := ReadEnroll(dir)
	if err != nil || got.Code != "no_hub" || !got.WrittenTime.Equal(t0.Add(time.Hour)) {
		t.Errorf("got %+v, %v", got, err)
	}
}

func TestASuccessfulEnrollClearsTheOldFailure(t *testing.T) {
	t.Parallel()
	dir := stateDir(t)
	tel, _ := quietTel(t)
	_ = RecordEnroll(tel, dir, t0, conn.ErrHubFailed)
	if err := RecordEnroll(tel, dir, t0, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadEnroll(dir); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("err = %v, want the file gone", err)
	}
	if err := RecordEnroll(tel, dir, t0, nil); err != nil {
		t.Errorf("clearing a missing file = %v, want nil", err)
	}
}

func TestRecordEnrollMakesTheStateFolderWhenItIsMissing(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "state")
	tel, _ := quietTel(t)
	if err := RecordEnroll(tel, dir, t0, conn.ErrNoHub); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(dir)
	if err != nil || fi.Mode().Perm() != 0o700 {
		t.Errorf("state folder = %v, %v; want mode 0700", fi, err)
	}
	if got, err := ReadEnroll(dir); err != nil || got.Code != "no_hub" {
		t.Errorf("got %+v, %v", got, err)
	}
}

func TestRecordEnrollFailureIsLoggedAndCounted(t *testing.T) {
	t.Parallel()
	tel, logs := quietTel(t)
	loose := t.TempDir()
	if err := os.Chmod(loose, 0o755); err != nil { //nolint:gosec // G302: the test makes the folder loose on purpose
		t.Fatal(err)
	}
	err := RecordEnroll(tel, loose, t0, conn.ErrHubFailed)
	if !errors.Is(err, conn.ErrLooseMode) {
		t.Fatalf("RecordEnroll into a folder open to others = %v, want ErrLooseMode", err)
	}
	if _, err := os.Stat(filepath.Join(loose, EnrollFile)); err == nil {
		t.Error("enroll.json was written into a loose folder")
	}
	line := lineOf(t, logs.String(), "status.write_failed")
	for _, want := range []string{`"file":"enroll.json"`, `"step":"dir"`, `"err_kind":"constraint"`} {
		if !strings.Contains(line, want) {
			t.Errorf("line lacks %s: %s", want, line)
		}
	}
	if got := sample(metricText(t, tel), "fleet_agent_status_writes_total", `file="enroll.json"`, `result="failed"`); got != "1" {
		t.Errorf("writes_total{enroll.json,failed} = %q, want 1", got)
	}
}

func TestReadEnrollCleansAndRefuses(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	write(t, dir, EnrollFile, `{"version":1,"written_time":"2026-10-10T12:00:00Z","code":"\u001b[2Jevil","reason":"lower \u001b"}`)
	got, err := ReadEnroll(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got.Code != StateUnknown || strings.ContainsAny(got.Reason, "\x1b ") {
		t.Errorf("got %+v, want the code unknown and a clean reason", got)
	}
	write(t, dir, EnrollFile, `{"version":3,"written_time":"2026-10-10T12:00:00Z","code":"other"}`)
	if _, err := ReadEnroll(dir); !errors.Is(err, ErrVersion) {
		t.Errorf("err = %v, want ErrVersion", err)
	}
	write(t, dir, EnrollFile, `{"version":1,"code":"other"}`)
	if _, err := ReadEnroll(dir); !errors.Is(err, ErrDamaged) {
		t.Errorf("err = %v, want ErrDamaged", err)
	}
	if _, err := ReadEnroll(t.TempDir()); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("err = %v, want not exist", err)
	}
}
