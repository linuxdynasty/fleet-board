package policy_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/linuxdynasty/fleet-board/internal/agent/policy"
)

func TestResolveDir(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, xdg, home, want string
	}{
		{"no XDG", "", "/home/me", "/home/me/.config/fleet-agent"},
		{"absolute XDG", "/srv/cfg", "/home/me", "/srv/cfg/fleet-agent"},
		{"relative XDG is ignored", "cfg", "/home/me", "/home/me/.config/fleet-agent"},
		{"XDG with a trailing slash", "/srv/cfg/", "/home/me", "/srv/cfg/fleet-agent"},
		{"macOS gets the same folder as Linux", "", "/Users/me", "/Users/me/.config/fleet-agent"},
	}
	for _, c := range cases {
		if got := policy.ResolveDir(c.xdg, c.home); got != c.want {
			t.Errorf("%s: ResolveDir(%q, %q) = %q, want %q", c.name, c.xdg, c.home, got, c.want)
		}
	}
}

func TestEnsureDirMakesAPrivateFolderAndKeepsAnExistingOne(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	dir := filepath.Join(base, ".config", "fleet-agent")
	if err := policy.EnsureDir(dir); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(dir)
	if err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf("config folder = %v, %v; want mode 0700", fi, err)
	}
	if err := os.Chmod(dir, 0o750); err != nil { //nolint:gosec // G302: a folder needs the x bit
		t.Fatal(err)
	}
	if err := policy.EnsureDir(dir); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(dir); fi.Mode().Perm() != 0o750 {
		t.Errorf("mode = %v, want an existing folder left alone", fi.Mode().Perm())
	}
	file := filepath.Join(base, "afile")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := policy.EnsureDir(file); err == nil {
		t.Error("EnsureDir accepted a file")
	}
}

func TestCheckRepoFolder(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	repos := filepath.Join(home, "repos")
	if err := os.Mkdir(repos, 0o755); err != nil { //nolint:gosec // G301: a test folder
		t.Fatal(err)
	}
	file := filepath.Join(home, "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	ok := map[string]string{repos: repos, "~/repos": repos, repos + "/": repos, "  " + repos + "\n": repos}
	for in, want := range ok {
		got, err := policy.CheckRepoFolder(in, home)
		if err != nil || got != want {
			t.Errorf("CheckRepoFolder(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"repos", "~/missing", file, home + "/nope", "~other/repos"} {
		if got, err := policy.CheckRepoFolder(in, home); err == nil {
			t.Errorf("CheckRepoFolder(%q) = %q, want an error", in, got)
		}
	}
}

func TestTheStarterFileIsValidAndPutsTheReposFolderInTheCeiling(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	repos := filepath.Join(base, "repos")
	if err := os.Mkdir(repos, 0o755); err != nil { //nolint:gosec // G301: a test folder
		t.Fatal(err)
	}
	dir := filepath.Join(base, "cfg", "fleet-agent")
	res, err := policy.WriteStarter(dir, repos)
	if err != nil || !res.Written || res.EmptyRoots {
		t.Fatalf("WriteStarter = %+v, %v", res, err)
	}
	path := filepath.Join(dir, policy.File)
	fi, err := os.Stat(path)
	if err != nil || fi.Mode().Perm() != 0o644 {
		t.Fatalf("policy.yaml = %v, %v; want mode 0644", fi, err)
	}
	if di, _ := os.Stat(dir); di.Mode().Perm() != 0o700 {
		t.Errorf("config folder mode = %v, want 0700", di.Mode().Perm())
	}
	data, _ := os.ReadFile(path) //nolint:gosec // G304: a test folder
	got := policy.Parse(data)
	if got.Problem != nil {
		t.Fatalf("the starter does not parse: %v\n%s", got.Problem, data)
	}
	p := got.Policy
	if len(p.AllowedRepoRoots) != 1 || p.AllowedRepoRoots[0] != repos || p.SchemaVersion != "1" {
		t.Errorf("starter = %+v, want schema 1 and the repos folder", p)
	}
	if p.MaxActive != nil || p.DefaultServer != nil || p.AllowedHarnesses != nil {
		t.Errorf("starter states a default as a live line: %+v", p)
	}
	ents, _ := os.ReadDir(dir)
	if len(ents) != 1 {
		t.Errorf("config folder holds %d entries, want only policy.yaml", len(ents))
	}
}

func TestTheStarterShowsEveryDefaultAsACommentThatIsValidWhenUncommented(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if _, err := policy.WriteStarter(dir, ""); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, policy.File)) //nolint:gosec // G304: a test folder
	_, defaults, found := strings.Cut(string(data), policy.StarterDefaultsMarker+"\n")
	if !found {
		t.Fatalf("no %q line in the starter:\n%s", policy.StarterDefaultsMarker, data)
	}
	var live []string
	for line := range strings.Lines(defaults) {
		line = strings.TrimRight(line, "\n")
		if rest, ok := strings.CutPrefix(line, "# "); ok && !strings.HasPrefix(rest, "-") {
			live = append(live, rest)
		} else if line == "#" {
			break // what follows is prose
		}
	}
	doc := "schema_version: \"1\"\n" + strings.Join(live, "\n") + "\n"
	res := policy.Parse([]byte(doc))
	if res.Problem != nil {
		t.Fatalf("the defaults, uncommented, do not parse: %v\n%s", res.Problem, doc)
	}
	p := res.Policy
	set := map[string]bool{
		"default_server": p.DefaultServer != nil && *p.DefaultServer == policy.ServerControl,
		"max_active":     p.MaxActive != nil && *p.MaxActive == 2, "max_total": p.MaxTotal != nil && *p.MaxTotal == 4,
		"max_windows": p.MaxWindows != nil && *p.MaxWindows == 12, "allowed_harnesses": len(p.AllowedHarnesses) == 5,
		"local_approval": p.LocalApproval != nil && len(p.LocalApproval) == 0, "rate_limits": len(p.RateLimits) == 1 && p.RateLimits[0].PerMinute == 60,
		"cert_lifetime": p.CertLifetime != nil && p.CertLifetime.Hours() == 2160, "attach_idle_timeout": p.AttachIdleTimeout != nil && p.AttachIdleTimeout.Minutes() == 30,
		"idle_threshold": p.IdleThreshold != nil && p.IdleThreshold.Minutes() == 2, "spool_cap_bytes": p.SpoolCapBytes != nil && *p.SpoolCapBytes == 2<<30,
		"block_on_battery": p.BlockOnBattery != nil && !*p.BlockOnBattery, "share_identity_email": p.ShareIdentityEmail != nil && !*p.ShareIdentityEmail,
		"window_name_template": p.WindowNameTemplate != nil && *p.WindowNameTemplate == "{harness} {work}",
	}
	for k, ok := range set {
		if !ok {
			t.Errorf("%s is missing or not its default in the starter's defaults block", k)
		}
	}
}

func TestTheStarterWithNoReposFolderHasAnEmptyList(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	res, err := policy.WriteStarter(dir, "")
	if err != nil || !res.Written || !res.EmptyRoots {
		t.Fatalf("WriteStarter = %+v, %v", res, err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, policy.File)) //nolint:gosec // G304: a test folder
	p := policy.Parse(data).Policy
	if p == nil || p.AllowedRepoRoots == nil || len(p.AllowedRepoRoots) != 0 {
		t.Errorf("allowed_repo_roots = %#v, want a stated empty list", p)
	}
}

func TestWriteStarterNeverOverwritesAnExistingFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, policy.File)
	const mine = "schema_version: \"1\"\nmax_active: 5 # mine\n"
	if err := os.WriteFile(path, []byte(mine), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := policy.WriteStarter(dir, t.TempDir())
	if err != nil || res.Written || !res.Existed {
		t.Fatalf("WriteStarter = %+v, %v; want Existed and not Written", res, err)
	}
	if got, _ := os.ReadFile(path); string(got) != mine { //nolint:gosec // G304: a test folder
		t.Errorf("policy.yaml = %q, want it untouched", got)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Errorf("mode changed to %v", fi.Mode().Perm())
	}
	ents, _ := os.ReadDir(dir)
	if len(ents) != 1 {
		t.Errorf("config folder holds %d entries; a temp file was left", len(ents))
	}
}

func TestTheStarterQuotesTheReposFolderSoItCannotInjectKeys(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	evil := filepath.Join(base, `x", "/etc`)
	if err := os.Mkdir(evil, 0o755); err != nil { //nolint:gosec // G301: a test folder
		t.Skipf("this file system refuses the name: %v", err)
	}
	dir := filepath.Join(base, "cfg")
	if _, err := policy.WriteStarter(dir, evil); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, policy.File)) //nolint:gosec // G304: a test folder
	res := policy.Parse(data)
	if res.Problem != nil || len(res.Policy.AllowedRepoRoots) != 1 || res.Policy.AllowedRepoRoots[0] != evil {
		t.Errorf("Parse = %+v: the folder name changed the file's meaning\n%s", res, data)
	}
}

func TestWriteStarterRefusesAFolderNameItCannotKeepWhole(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if _, err := policy.WriteStarter(dir, "/tmp/a\nmax_active: 16"); err == nil {
		t.Error("WriteStarter accepted a folder name with a line break")
	}
	if _, err := os.Stat(filepath.Join(dir, policy.File)); err == nil {
		t.Error("a file was written anyway")
	}
}
