package worktree_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/worktree"
	"github.com/linuxdynasty/fleet-board/internal/proc"
)

func scratchFile(name, content string) *agentv1.ScratchFile {
	return &agentv1.ScratchFile{Name: name, Content: []byte(content)}
}

func testConfig(t *testing.T, roots []string, lookup repoTable, state string) worktree.Config {
	t.Helper()
	runner, err := proc.NewRunner(nil)
	if err != nil {
		t.Fatal(err)
	}
	caller, err := proc.For("git")
	if err != nil {
		t.Fatal(err)
	}
	return worktree.Config{
		Runner: runner,
		Caller: caller,
		Roots:  func() []string { return roots },
		LookupRepo: func(repo *typev1.RepoRef) (string, error) {
			top, ok := lookup[repo.GetHost()+"/"+repo.GetPath()]
			if !ok {
				return "", os.ErrNotExist
			}
			return top, nil
		},
		StateDir: func() string { return state },
		Now:      func() time.Time { return time.Now() },
	}
}

func scratchManager(t *testing.T, state string) *worktree.Manager {
	t.Helper()
	var released []string
	cfg := testConfig(t, []string{state}, repoTable{}, state)
	cfg.ReleaseByPath = func(path string) { released = append(released, path) }
	m, err := worktree.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestEnsureScratch(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cases := []struct {
		name       string
		goal       string
		files      []*agentv1.ScratchFile
		wantStatus typev1.Status
		wantReason typev1.ReasonCode
	}{
		{
			name: "writes files",
			goal: "goal1",
			files: []*agentv1.ScratchFile{
				scratchFile("input.json", "{\"n\":1}\n"),
				scratchFile("notes.md", "# hi\n"),
			},
			wantStatus: typev1.Status_STATUS_OK,
		},
		{name: "no files is OK", goal: "goal2", wantStatus: typev1.Status_STATUS_OK},
		{
			name:       "bad goal id refused",
			goal:       "Bad_Id!",
			files:      []*agentv1.ScratchFile{scratchFile("a.json", "{}")},
			wantStatus: typev1.Status_STATUS_FAILED,
			wantReason: typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
		},
		{
			name:       "traversal refused",
			goal:       "goal4",
			files:      []*agentv1.ScratchFile{scratchFile("../x.md", "x")},
			wantStatus: typev1.Status_STATUS_FAILED,
			wantReason: typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
		},
		{
			name:       "separator refused",
			goal:       "goal5",
			files:      []*agentv1.ScratchFile{scratchFile("a/b.md", "x")},
			wantStatus: typev1.Status_STATUS_FAILED,
			wantReason: typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
		},
		{
			name:       "wrong suffix refused",
			goal:       "goal6",
			files:      []*agentv1.ScratchFile{scratchFile("run.txt", "x")},
			wantStatus: typev1.Status_STATUS_FAILED,
			wantReason: typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
		},
		{
			name:       "uppercase refused",
			goal:       "goal7",
			files:      []*agentv1.ScratchFile{scratchFile("Notes.md", "x")},
			wantStatus: typev1.Status_STATUS_FAILED,
			wantReason: typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
		},
		{
			name:       "empty name refused",
			goal:       "goal8",
			files:      []*agentv1.ScratchFile{scratchFile("", "x")},
			wantStatus: typev1.Status_STATUS_FAILED,
			wantReason: typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
		},
		{
			name: "too many files refused",
			goal: "goal9",
			files: []*agentv1.ScratchFile{
				scratchFile("a1.json", "{}"), scratchFile("a2.json", "{}"),
				scratchFile("a3.json", "{}"), scratchFile("a4.json", "{}"),
				scratchFile("a5.json", "{}"), scratchFile("a6.json", "{}"),
				scratchFile("a7.json", "{}"), scratchFile("a8.json", "{}"),
				scratchFile("a9.json", "{}"),
			},
			wantStatus: typev1.Status_STATUS_FAILED,
			wantReason: typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
		},
		{
			name:       "single file over cap refused",
			goal:       "goal10",
			files:      []*agentv1.ScratchFile{{Name: "big.json", Content: make([]byte, 512*1024+1)}},
			wantStatus: typev1.Status_STATUS_FAILED,
			wantReason: typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
		},
		{
			name: "total over cap refused",
			goal: "goal11",
			files: []*agentv1.ScratchFile{
				{Name: "b1.json", Content: make([]byte, 400*1024)},
				{Name: "b2.json", Content: make([]byte, 400*1024)},
			},
			wantStatus: typev1.Status_STATUS_FAILED,
			wantReason: typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := scratchManager(t, t.TempDir())
			res := m.EnsureScratch(ctx, &agentv1.EnsureScratch{
				Goal:  &typev1.GoalId{Value: tc.goal},
				Files: tc.files,
			})
			if res.GetStatus() != tc.wantStatus || res.GetReason() != tc.wantReason {
				t.Fatalf("status = %v/%v, want %v/%v (%s)", res.GetStatus(), res.GetReason(), tc.wantStatus, tc.wantReason, res.GetMessage())
			}
		})
	}
}

func TestEnsureScratchRefusesLinkTarget(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	state := t.TempDir()
	m := scratchManager(t, state)
	goal := &typev1.GoalId{Value: "goal21"}
	if res := m.EnsureScratch(ctx, &agentv1.EnsureScratch{Goal: goal}); res.GetStatus() != typev1.Status_STATUS_OK {
		t.Fatalf("setup ensure: %v", res)
	}
	dir := filepath.Join(state, "scratch", "goal21")
	target := filepath.Join(t.TempDir(), "outside.json")
	if err := os.WriteFile(target, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "input.json")); err != nil {
		t.Fatal(err)
	}
	res := m.EnsureScratch(ctx, &agentv1.EnsureScratch{Goal: goal, Files: []*agentv1.ScratchFile{scratchFile("input.json", "{}\n")}})
	if res.GetStatus() != typev1.Status_STATUS_FAILED || res.GetReason() != typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT {
		t.Fatalf("status = %v/%v, want FAILED/INVALID_ARGUMENT", res.GetStatus(), res.GetReason())
	}
	if raw, err := os.ReadFile(target); err != nil || string(raw) != "{}" { //nolint:gosec // G304: the test's own temp file
		t.Fatalf("link target changed: %q %v", raw, err)
	}
}

func TestEnsureScratchWritesReplacesAndBootstraps(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	state := t.TempDir()
	m := scratchManager(t, state)
	goal := &typev1.GoalId{Value: "goal20"}

	first := m.EnsureScratch(ctx, &agentv1.EnsureScratch{Goal: goal, Files: []*agentv1.ScratchFile{scratchFile("input.json", "{\"n\":1}\n")}})
	if first.GetStatus() != typev1.Status_STATUS_OK {
		t.Fatalf("first: %v", first)
	}
	d, ok := first.GetDetail().(*agentv1.CommandResult_EnsureScratch)
	if !ok || !d.EnsureScratch.GetCreated() {
		t.Fatalf("first ensure reads created=false: %v", first)
	}
	dir := filepath.Join(state, "scratch", "goal20")
	raw, err := os.ReadFile(filepath.Join(dir, "input.json")) //nolint:gosec // G304: the test's own scratch folder
	if err != nil || string(raw) != "{\"n\":1}\n" {
		t.Fatalf("input.json = %q, err = %v", raw, err)
	}
	if fi, err := os.Stat(filepath.Join(dir, "input.json")); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("input.json stat: %v %v", fi, err)
	}
	// The scratch folder carries the board bootstrap for the goal file.
	if _, err := os.Stat(filepath.Join(dir, ".fleet-board", ".gitignore")); err != nil {
		t.Fatalf("board bootstrap missing in scratch dir: %v", err)
	}

	second := m.EnsureScratch(ctx, &agentv1.EnsureScratch{Goal: goal, Files: []*agentv1.ScratchFile{scratchFile("input.json", "{\"n\":2}\n")}})
	if second.GetStatus() != typev1.Status_STATUS_OK {
		t.Fatalf("second: %v", second)
	}
	if d2, ok := second.GetDetail().(*agentv1.CommandResult_EnsureScratch); !ok || d2.EnsureScratch.GetCreated() {
		t.Fatalf("second ensure reads created=true: %v", second)
	}
	if raw, err := os.ReadFile(filepath.Join(dir, "input.json")); err != nil || string(raw) != "{\"n\":2}\n" { //nolint:gosec // G304: the test's own scratch folder
		t.Fatalf("replaced input.json = %q, err = %v", raw, err)
	}
}

// TestCollectScratch covers the read half: the verifier reads back the one
// file the goal wrote.
func TestCollectScratch(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	const body = "line one\nline two\nline three\n"
	cases := []struct {
		name string
		// setup writes the scratch dir and returns the file name to collect.
		setup    func(t *testing.T, dir string) string
		maxBytes uint32
		// want checks the OK result; nil means SCRATCH_MISSING or INVALID_ARGUMENT.
		want     func(t *testing.T, got *agentv1.CollectScratchResult)
		wantFail typev1.ReasonCode
	}{
		{
			name: "round trip",
			setup: func(t *testing.T, dir string) string {
				t.Helper()
				if err := os.WriteFile(filepath.Join(dir, "out.md"), []byte(body), 0o600); err != nil {
					t.Fatal(err)
				}
				return "out.md"
			},
			want: func(t *testing.T, got *agentv1.CollectScratchResult) {
				t.Helper()
				if got.GetText() != body || got.GetTruncated() || got.GetSizeBytes() != uint64(len(body)) {
					t.Fatalf("got %+v", got)
				}
			},
		},
		{
			name: "truncates at max bytes",
			setup: func(t *testing.T, dir string) string {
				t.Helper()
				if err := os.WriteFile(filepath.Join(dir, "out.md"), []byte(body), 0o600); err != nil {
					t.Fatal(err)
				}
				return "out.md"
			},
			maxBytes: 4,
			want: func(t *testing.T, got *agentv1.CollectScratchResult) {
				t.Helper()
				if got.GetText() != body[:4] || !got.GetTruncated() || got.GetSizeBytes() != uint64(len(body)) {
					t.Fatalf("got %+v", got)
				}
			},
		},
		{
			name: "zero means the 64KiB cap",
			setup: func(t *testing.T, dir string) string {
				t.Helper()
				big := strings.Repeat("a", 70*1024)
				if err := os.WriteFile(filepath.Join(dir, "big.md"), []byte(big), 0o600); err != nil {
					t.Fatal(err)
				}
				return "big.md"
			},
			want: func(t *testing.T, got *agentv1.CollectScratchResult) {
				t.Helper()
				if len(got.GetText()) != 65536 || !got.GetTruncated() || got.GetSizeBytes() != 70*1024 {
					t.Fatalf("len = %d truncated = %v size = %d", len(got.GetText()), got.GetTruncated(), got.GetSizeBytes())
				}
			},
		},
		{
			name: "missing file",
			setup: func(t *testing.T, dir string) string {
				t.Helper()
				return "absent.md"
			},
			wantFail: typev1.ReasonCode_REASON_CODE_SCRATCH_MISSING,
		},
		{
			name: "symlink never followed",
			setup: func(t *testing.T, dir string) string {
				t.Helper()
				target := filepath.Join(t.TempDir(), "secret.md")
				if err := os.WriteFile(target, []byte("s"), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, filepath.Join(dir, "link.md")); err != nil {
					t.Fatal(err)
				}
				return "link.md"
			},
			wantFail: typev1.ReasonCode_REASON_CODE_SCRATCH_MISSING,
		},
		{
			name: "directory refused",
			setup: func(t *testing.T, dir string) string {
				t.Helper()
				if err := os.MkdirAll(filepath.Join(dir, "sub.md"), 0o700); err != nil {
					t.Fatal(err)
				}
				return "sub.md"
			},
			wantFail: typev1.ReasonCode_REASON_CODE_SCRATCH_MISSING,
		},
		{
			name: "bad name refused",
			setup: func(t *testing.T, dir string) string {
				t.Helper()
				return "../x.md"
			},
			wantFail: typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
		},
		{
			name: "control characters stripped but newline and tab kept",
			setup: func(t *testing.T, dir string) string {
				t.Helper()
				if err := os.WriteFile(filepath.Join(dir, "ctl.md"), []byte("a\x00b\x07c\nd\te\x1bf\rg"), 0o600); err != nil {
					t.Fatal(err)
				}
				return "ctl.md"
			},
			want: func(t *testing.T, got *agentv1.CollectScratchResult) {
				t.Helper()
				if got.GetText() != "abc\nd\tefg" {
					t.Fatalf("text = %q", got.GetText())
				}
			},
		},
		{
			name: "invalid UTF-8 stays valid",
			setup: func(t *testing.T, dir string) string {
				t.Helper()
				if err := os.WriteFile(filepath.Join(dir, "bin.md"), []byte("ok \xff\xfe\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				return "bin.md"
			},
			want: func(t *testing.T, got *agentv1.CollectScratchResult) {
				t.Helper()
				if !utf8.ValidString(got.GetText()) || !strings.HasPrefix(got.GetText(), "ok ") {
					t.Fatalf("text = %q", got.GetText())
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			state := t.TempDir()
			m := scratchManager(t, state)
			goal := &typev1.GoalId{Value: "goal30"}
			if res := m.EnsureScratch(ctx, &agentv1.EnsureScratch{Goal: goal}); res.GetStatus() != typev1.Status_STATUS_OK {
				t.Fatalf("setup ensure: %v", res)
			}
			name := tc.setup(t, filepath.Join(state, "scratch", "goal30"))
			res := m.CollectScratch(ctx, &agentv1.CollectScratch{Goal: goal, Name: name, MaxBytes: tc.maxBytes})
			if tc.want == nil {
				if res.GetStatus() != typev1.Status_STATUS_FAILED || res.GetReason() != tc.wantFail {
					t.Fatalf("status = %v/%v, want FAILED/%v", res.GetStatus(), res.GetReason(), tc.wantFail)
				}
				return
			}
			if res.GetStatus() != typev1.Status_STATUS_OK {
				t.Fatalf("collect: %v", res)
			}
			d, ok := res.GetDetail().(*agentv1.CommandResult_CollectScratch)
			if !ok || d.CollectScratch == nil {
				t.Fatalf("no CollectScratch detail: %v", res)
			}
			tc.want(t, d.CollectScratch)
		})
	}
}
