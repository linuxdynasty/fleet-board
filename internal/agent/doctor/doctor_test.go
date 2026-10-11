package doctor_test

import (
	"bytes"
	"context"
	"encoding/json"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/linuxdynasty/fleet-board/internal/agent/doctor"
)

func TestEveryLocalCheckIsListedOnceWithAStatusAndAReason(t *testing.T) {
	t.Parallel()
	r := newRig(t) // a bare, half-set-up machine: most checks have something to say
	rep := r.run()
	want := []string{
		"state_dir", "config_dir", "identity", "cert_expiry", "cert_file", "renew_key",
		"policy_file", "policy_roots", "policy_harnesses", "policy_watch",
		"status_file", "connection", "push_socket", "mdns_browse", "snapshot_size", "clock", "enroll",
		"tmux", "probe_folder", "hub_handshake", "linger", "harness_paths", "probe_trust", "two_pane_folders",
	}
	var got []string
	valid := []doctor.Status{doctor.OK, doctor.Warn, doctor.Fail, doctor.Unknown, doctor.NotChecked}
	for _, c := range rep.Checks {
		got = append(got, c.ID)
		if !slices.Contains(valid, c.Status) {
			t.Errorf("%s: status %q is not one of the five words", c.ID, c.Status)
		}
		if c.Reason == "" || c.Title == "" {
			t.Errorf("%s: empty reason or title: %+v", c.ID, c)
		}
		if c.Status != doctor.OK && c.Status != doctor.NotChecked && c.Fix == "" && c.ID != "clock" {
			t.Errorf("%s: %s with no fix line", c.ID, c.Status)
		}
	}
	if !slices.Equal(got, want) {
		t.Errorf("checks = %v, want %v", got, want)
	}
}

func TestChecksThatNeedLaterRowsAreListedAsNotChecked(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.want("harness_paths", doctor.NotChecked, "this build cannot check it yet (row 8)")
	r.want("probe_trust", doctor.NotChecked, "this build cannot check it yet (row 8)")
	r.want("two_pane_folders", doctor.NotChecked, "this build cannot check it yet (row 9)")
}

func TestExitFollowsTheWorstStatus(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		statuses []doctor.Status
		failed   bool
	}{
		{"all ok", []doctor.Status{doctor.OK, doctor.OK}, false},
		{"warn is not a failure", []doctor.Status{doctor.OK, doctor.Warn}, false},
		{"unknown is not a failure", []doctor.Status{doctor.Unknown}, false},
		{"not_checked is not a failure", []doctor.Status{doctor.NotChecked}, false},
		{"one fail", []doctor.Status{doctor.OK, doctor.Fail, doctor.Warn}, true},
		{"nothing", nil, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var cs []doctor.Check
			for i, s := range tc.statuses {
				cs = append(cs, doctor.Check{ID: "c" + strconv.Itoa(i), Title: "t", Run: func(context.Context, *doctor.Env) doctor.Outcome {
					return doctor.Outcome{Status: s, Reason: "r"}
				}})
			}
			rep := doctor.Run(t.Context(), doctor.Options{}, cs...)
			if rep.Failed() != tc.failed {
				t.Errorf("Failed() = %v, want %v", rep.Failed(), tc.failed)
			}
		})
	}
}

func TestACheckThatPanicsIsUnknownAndTheRestStillRun(t *testing.T) {
	t.Parallel()
	secret := "panic-text-" + "must-not-print"
	rep := doctor.Run(t.Context(), doctor.Options{},
		doctor.Check{ID: "boom", Title: "t", Run: func(context.Context, *doctor.Env) doctor.Outcome { panic(secret) }},
		doctor.Check{ID: "next", Title: "t", Run: func(context.Context, *doctor.Env) doctor.Outcome {
			return doctor.Outcome{Status: doctor.OK, Reason: "fine"}
		}},
	)
	if len(rep.Checks) != 2 || rep.Checks[0].Status != doctor.Unknown || rep.Checks[1].Status != doctor.OK {
		t.Fatalf("checks = %+v", rep.Checks)
	}
	if strings.Contains(rep.Checks[0].Reason, secret) || rep.Checks[0].Reason == "" {
		t.Errorf("reason = %q, want a fixed sentence without the panic text", rep.Checks[0].Reason)
	}
}

func TestPrintedTextIsBoundedAndHasNoControlCharacters(t *testing.T) {
	t.Parallel()
	nasty := "a\x1b[31mred\x1b[0m\nline2\r\x00\u202eevil\u2066 " + strings.Repeat("x", 5000)
	rep := doctor.Run(t.Context(), doctor.Options{StateDir: "/s\x1b]0;t\x07", ConfigDir: "/c\n"},
		doctor.Check{ID: "n", Title: "t", Run: func(context.Context, *doctor.Env) doctor.Outcome {
			return doctor.Outcome{Status: doctor.Warn, Reason: nasty, Fix: nasty}
		}})
	var text, js bytes.Buffer
	if err := doctor.WriteText(&text, rep); err != nil {
		t.Fatal(err)
	}
	if err := doctor.WriteJSON(&js, rep); err != nil {
		t.Fatal(err)
	}
	for name, out := range map[string]string{"text": text.String(), "json": js.String()} {
		for _, r := range out {
			if r == '\n' || r == '\t' {
				continue
			}
			if r < 0x20 || r == 0x7f || (r >= 0x202a && r <= 0x202e) || (r >= 0x2066 && r <= 0x2069) {
				t.Fatalf("%s output has the control character %U", name, r)
			}
		}
		if len(out) > 4000 {
			t.Errorf("%s output is %d bytes: the reason was not bounded", name, len(out))
		}
	}
	if !strings.Contains(text.String(), "red") {
		t.Errorf("the readable part of the text was lost: %q", text.String())
	}
}

func TestTextHasOneLinePerCheckAndAFixLineWhenThereIsOne(t *testing.T) {
	t.Parallel()
	rep := doctor.Run(t.Context(), doctor.Options{StateDir: "/state", ConfigDir: "/cfg"},
		doctor.Check{ID: "a", Title: "Alpha", Run: func(context.Context, *doctor.Env) doctor.Outcome {
			return doctor.Outcome{Status: doctor.OK, Reason: "all good"}
		}},
		doctor.Check{ID: "b", Title: "Beta", Run: func(context.Context, *doctor.Env) doctor.Outcome {
			return doctor.Outcome{Status: doctor.Fail, Reason: "broken", Fix: "mend it"}
		}},
	)
	var buf bytes.Buffer
	if err := doctor.WriteText(&buf, rep); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, w := range []string{"/state", "/cfg", "ok", "Alpha", "all good", "fail", "Beta", "broken", "fix: mend it", "1 ok", "1 fail"} {
		if !strings.Contains(out, w) {
			t.Errorf("text lacks %q:\n%s", w, out)
		}
	}
	if strings.Contains(out, "fix: \n") || strings.Count(out, "fix:") != 1 {
		t.Errorf("an ok check must have no fix line:\n%s", out)
	}
}

func TestJSONHasTheFoldersTheChecksAndTheCounts(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	var buf bytes.Buffer
	if err := doctor.WriteJSON(&buf, r.run()); err != nil {
		t.Fatal(err)
	}
	var got struct {
		StateDir  string `json:"state_dir"`
		ConfigDir string `json:"config_dir"`
		Summary   map[string]int
		Checks    []struct {
			ID, Title, Status, Reason string
			Fix                       string `json:"fix"`
		}
	}
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, buf.String())
	}
	if got.StateDir != r.state || got.ConfigDir != r.cfg || len(got.Checks) != 24 {
		t.Errorf("got %q %q and %d checks", got.StateDir, got.ConfigDir, len(got.Checks))
	}
	total := 0
	for _, k := range []string{"ok", "warn", "fail", "unknown", "not_checked"} {
		v, ok := got.Summary[k]
		if !ok {
			t.Errorf("summary lacks %q: %v", k, got.Summary)
		}
		total += v
	}
	if total != 24 {
		t.Errorf("summary adds up to %d, want 24", total)
	}
}

// Doctor starts nothing itself and never touches the agent's database,
// ring or tmux client: a test on the imports, in the style of machine's.
func TestDoctorImportsNoProcessNoRingAndNoTmuxClient(t *testing.T) {
	t.Parallel()
	banned := []string{
		"os/exec", "github.com/linuxdynasty/fleet-board/internal/agent/ring",
		"github.com/linuxdynasty/fleet-board/internal/agent/tmux", "github.com/linuxdynasty/fleet-board/internal/store",
	}
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		af, err := parser.ParseFile(token.NewFileSet(), f, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, im := range af.Imports {
			p, _ := strconv.Unquote(im.Path.Value)
			if slices.Contains(banned, p) || strings.HasPrefix(p, banned[2]+"/") {
				t.Errorf("%s imports %s", f, p)
			}
		}
	}
}

// Doctor never opens agent.log or agent.db (plan row 6): nothing in the
// package names them as a file to open.
func TestDoctorNeverNamesTheLogOrTheDatabaseAsAFileToOpen(t *testing.T) {
	t.Parallel()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f) //nolint:gosec // G304: a file name from Glob in this package's folder
		if err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{`"agent.db"`, `"agent.log"`, `"audit.log"`} {
			if bytes.Contains(src, []byte(name)) {
				t.Errorf("%s names %s as a string: doctor must not open it", f, name)
			}
		}
	}
}
