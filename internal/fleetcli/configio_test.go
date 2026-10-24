package fleetcli_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/fleetconfig"
	"github.com/linuxdynasty/fleet-board/internal/seed"
)

// writeYAML stores text under a temp dir and returns its path.
func writeYAML(t *testing.T, text string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fleet.yaml")
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestExportPrintsYAML(t *testing.T) {
	h := startHubWithExtra(t, false, seedGitHubRow)
	code, out, diagnostic := invoke(t, h, "export")
	if code != 0 {
		t.Fatalf("export = %d: %s", code, diagnostic)
	}
	if !strings.Contains(out, "format_version: 1") {
		t.Fatalf("export output = %q", out)
	}
	if _, err := fleetconfig.Decode([]byte(out)); err != nil {
		t.Fatalf("export is not a fleet.yaml: %v", err)
	}
	code, jsonOut, diagnostic := invoke(t, h, "export", "--json")
	if code != 0 {
		t.Fatalf("export --json = %d: %s", code, diagnostic)
	}
	golden(t, "export", jsonOut)
}

func TestExportRefusesAPresentToken(t *testing.T) {
	code, out, diagnostic := invokeWithToken(t, "export")
	if code != 2 || out != "" || !strings.Contains(diagnostic, "API tokens") {
		t.Fatalf("export with token = %d, %q, %q", code, out, diagnostic)
	}
	if strings.Contains(diagnostic, "sensitive-token") {
		t.Fatal("diagnostic printed token")
	}
}

func TestExportStripsStampedIDs(t *testing.T) {
	h := startHubWithExtra(t, false, seedGitHubRow)
	if code, _, diagnostic := invokeSecret(t, h, h.secret,
		"repo", "create", "github.com/acme/api", "--request-id", "cli-exp-1"); code != 0 {
		t.Fatalf("create = %d: %s", code, diagnostic)
	}
	code, out, diagnostic := invoke(t, h, "export")
	if code != 0 {
		t.Fatalf("export = %d: %s", code, diagnostic)
	}
	// code_host_ref is the portable ref (#397); a bare code_host wrapper
	// or its JSON alias would be a hub id leaking into the document.
	withoutRefs := strings.ReplaceAll(out, "code_host_ref", "")
	if strings.Contains(withoutRefs, "code_host") || strings.Contains(out, "codeHost") {
		t.Fatalf("export carries a hub id: %q", out)
	}
	if _, err := fleetconfig.Decode([]byte(out)); err != nil {
		t.Fatalf("export is not a fleet.yaml: %v", err)
	}
}

func TestImportDryRunShowsThePlan(t *testing.T) {
	h := startHubWithExtra(t, false, seedGitHubRow)
	path := writeYAML(t, "format_version: 1\nrepos:\n  - repo:\n      host: github.com\n      path: acme/web\n")
	code, out, diagnostic := invokeSecret(t, h, h.secret,
		"import", path, "--dry-run", "--request-id", "cli-imp-1")
	if code != 0 {
		t.Fatalf("dry run = %d: %s", code, diagnostic)
	}
	if !strings.Contains(out, `REPO "github.com/acme/web" CREATE`) || !strings.Contains(out, "plan_hash") {
		t.Fatalf("dry run output = %q", out)
	}
	code, jsonOut, diagnostic := invokeSecret(t, h, h.secret,
		"import", path, "--dry-run", "--request-id", "cli-imp-2", "--json")
	if code != 0 {
		t.Fatalf("dry run --json = %d: %s", code, diagnostic)
	}
	var res struct {
		Changes []struct {
			Kind string `json:"kind"`
		} `json:"changes"`
		Applied  bool   `json:"applied"`
		PlanHash string `json:"planHash"`
	}
	if err := json.Unmarshal([]byte(jsonOut), &res); err != nil {
		t.Fatal(err)
	}
	if res.Applied || res.PlanHash == "" {
		t.Fatalf("dry run = %+v, want unapplied with a hash", res)
	}
	golden(t, "import-dry-run", jsonOut)
}

// reviewedPlanHash runs the dry run the apply must quote and returns its
// hash, failing the test when the plan does not come back unapplied.
func reviewedPlanHash(t *testing.T, h localHub, path, requestID string) string {
	t.Helper()
	code, dryOut, diagnostic := invokeSecret(t, h, h.secret,
		"import", path, "--dry-run", "--request-id", requestID, "--json")
	if code != 0 {
		t.Fatalf("dry run = %d: %s", code, diagnostic)
	}
	var dry struct {
		Applied  bool   `json:"applied"`
		PlanHash string `json:"planHash"`
	}
	if err := json.Unmarshal([]byte(dryOut), &dry); err != nil {
		t.Fatal(err)
	}
	if dry.Applied || dry.PlanHash == "" {
		t.Fatalf("dry run = %+v, want unapplied with a hash", dry)
	}
	return dry.PlanHash
}

func TestImportAppliesThenRefusesAStaleHash(t *testing.T) {
	h := startHubWithExtra(t, false, seedGitHubRow)
	path := writeYAML(t, "format_version: 1\nrepos:\n  - repo:\n      host: github.com\n      path: acme/web\n")
	hash := reviewedPlanHash(t, h, path, "cli-imp-0")
	code, jsonOut, diagnostic := invokeSecret(t, h, h.secret,
		"import", path, "--request-id", "cli-imp-1",
		"--expected-plan-hash", hash, "--json")
	if code != 0 {
		t.Fatalf("apply = %d: %s", code, diagnostic)
	}
	var applied struct {
		Applied  bool   `json:"applied"`
		PlanHash string `json:"planHash"`
	}
	if err := json.Unmarshal([]byte(jsonOut), &applied); err != nil {
		t.Fatal(err)
	}
	if !applied.Applied || applied.PlanHash == "" {
		t.Fatalf("apply = %+v, want applied with a hash", applied)
	}
	code, out, diagnostic := invokeSecret(t, h, h.secret,
		"import", path, "--expected-plan-hash", applied.PlanHash,
		"--request-id", "cli-imp-2")
	if code != 1 || out != "" || !strings.Contains(diagnostic, "VERSION_MISMATCH") {
		t.Fatalf("stale hash = %d, %q, %q", code, out, diagnostic)
	}
}

func TestImportUsageErrors(t *testing.T) {
	h := startHubWithExtra(t, false, seedGitHubRow)
	path := writeYAML(t, "format_version: 1\n")
	missing := filepath.Join(t.TempDir(), "missing.yaml")
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"no file", []string{"import", "--dry-run"}},
		{"dry run with hash", []string{"import", path, "--dry-run", "--expected-plan-hash", "x"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, out, _ := invokeSecret(t, h, h.secret, tc.args...)
			if code != 2 || out != "" {
				t.Fatalf("%v = %d, %q", tc.args, code, out)
			}
		})
	}
	// An unreadable file fails the command with empty stdout; it is not
	// bad usage.
	for _, bad := range []string{missing, writeYAML(t, "format_version: 1\nfleet_values: [unclosed\n")} {
		code, out, diagnostic := invokeSecret(t, h, h.secret, "import", bad, "--dry-run")
		if code != 1 || out != "" || !strings.Contains(diagnostic, "could not read") {
			t.Fatalf("bad file %q = %d, %q, %q", bad, code, out, diagnostic)
		}
	}
}

// TestConfigWalkThroughCLI repeats the 13a configuration walk through the
// CLI: watch a repo, set a reserve, map a Codex account, tune settings and
// the integration, then carry the whole hub out and back in through YAML.
func TestConfigWalkThroughCLI(t *testing.T) {
	h := startHubWithExtra(t, true, seedGitHubRow)
	run := func(args ...string) (int, string, string) {
		t.Helper()
		return invokeSecret(t, h, h.secret, args...)
	}
	if code, _, diagnostic := run("repo", "create", "github.com/acme/api", "--request-id", "walk-repo"); code != 0 {
		t.Fatalf("walk repo create = %d: %s", code, diagnostic)
	}
	code, created, diagnostic := run("account", "create",
		"--harness", "codex", "--provider", "openai", "--display-name", "walk",
		"--request-id", "walk-acc", "--json")
	if code != 0 {
		t.Fatalf("walk account create = %d: %s", code, diagnostic)
	}
	id := createdAccountID(t, created)
	if code, _, diagnostic := run("account", "update", id,
		"--reserve-percent", "25", "--request-id", "walk-reserve"); code != 0 {
		t.Fatalf("walk reserve = %d: %s", code, diagnostic)
	}
	if code, _, diagnostic := run("settings", "update",
		"--max-active", "2", "--auto-dispatch", "--request-id", "walk-set"); code != 0 {
		t.Fatalf("walk settings = %d: %s", code, diagnostic)
	}
	if code, _, diagnostic := run("settings", "integration", "update",
		"--id", "github", "--label", "GitHub", "--request-id", "walk-int"); code != 0 {
		t.Fatalf("walk integration = %d: %s", code, diagnostic)
	}
	code, exported, diagnostic := invoke(t, h, "export")
	if code != 0 {
		t.Fatalf("walk export = %d: %s", code, diagnostic)
	}
	cfg, err := fleetconfig.Decode([]byte(exported))
	if err != nil {
		t.Fatalf("walk export decode: %v", err)
	}
	if len(cfg.GetRepos()) != 1 || cfg.GetRepos()[0].GetRepo().GetPath() != "acme/api" {
		t.Fatalf("walk export repos = %+v, want acme/api", cfg.GetRepos())
	}
	var walked *hubv1.AccountConfig
	for _, a := range cfg.GetAccounts() {
		if a.GetSpec().GetDisplayName() == "walk" {
			walked = a
		}
	}
	if walked == nil || walked.GetSpec().GetReservePercent() != 25 {
		t.Fatalf("walk export accounts = %+v, want the walk account at reserve 25", cfg.GetAccounts())
	}
	if !cfg.GetHub().GetAutoDispatch() || cfg.GetFleetValues().GetMaxActive() != 2 {
		t.Fatalf("walk export hub/values = %+v/%+v", cfg.GetHub(), cfg.GetFleetValues())
	}
	if len(cfg.GetIntegrations()) != 1 || cfg.GetIntegrations()[0].GetLabel() != "GitHub" {
		t.Fatalf("walk export integrations = %+v", cfg.GetIntegrations())
	}
	// The exported hub re-imports to an empty plan.
	exportPath := writeYAML(t, exported)
	code, planJSON, diagnostic := run("import", exportPath,
		"--dry-run", "--request-id", "walk-plan", "--json")
	if code != 0 {
		t.Fatalf("walk dry run = %d: %s", code, diagnostic)
	}
	var plan struct {
		Changes []struct {
			Entity string `json:"entity"`
			Name   string `json:"name"`
			Kind   string `json:"kind"`
		} `json:"changes"`
	}
	if err := json.Unmarshal([]byte(planJSON), &plan); err != nil {
		t.Fatal(err)
	}
	if len(plan.Changes) == 0 {
		t.Fatal("walk plan has no lines, want one per configured entity")
	}
	for _, c := range plan.Changes {
		if c.Kind != "CONFIG_CHANGE_KIND_UNCHANGED" {
			t.Errorf("walk plan line %+v is not unchanged", c)
		}
	}
	// A changed document plans, applies, and then refuses its old hash.
	changed, err := fleetconfig.Decode([]byte(exported))
	if err != nil {
		t.Fatal(err)
	}
	changed.Repos = append(changed.GetRepos(), &hubv1.RepoSpec{
		Repo: &typev1.RepoRef{Host: "github.com", Path: "acme/web"},
	})
	changedYAML, err := fleetconfig.Encode(changed)
	if err != nil {
		t.Fatal(err)
	}
	changedPath := writeYAML(t, string(changedYAML))
	code, dryOut, diagnostic := run("import", changedPath, "--dry-run", "--request-id", "walk-dry")
	if code != 0 || !strings.Contains(dryOut, `REPO "github.com/acme/web" CREATE`) {
		t.Fatalf("walk changed dry run = %d, %q, %q", code, dryOut, diagnostic)
	}
	hash := reviewedPlanHash(t, h, changedPath, "walk-plan-hash")
	code, appliedJSON, diagnostic := run("import", changedPath,
		"--request-id", "walk-apply", "--expected-plan-hash", hash, "--json")
	if code != 0 {
		t.Fatalf("walk apply = %d: %s", code, diagnostic)
	}
	var applied struct {
		Applied  bool   `json:"applied"`
		PlanHash string `json:"planHash"`
	}
	if err := json.Unmarshal([]byte(appliedJSON), &applied); err != nil {
		t.Fatal(err)
	}
	if !applied.Applied {
		t.Fatalf("walk apply = %+v, want applied", applied)
	}
	code, applyOut, diagnostic := run("import", changedPath,
		"--expected-plan-hash", applied.PlanHash, "--request-id", "walk-stale")
	if code != 1 || applyOut != "" || !strings.Contains(diagnostic, "VERSION_MISMATCH") {
		t.Fatalf("walk stale hash = %d, %q, %q", code, applyOut, diagnostic)
	}
	// A stale version is refused on the writes too.
	code, staleOut, diagnostic := run("settings", "update",
		"--max-active", "3", "--expected-version", "99", "--request-id", "walk-stale-ver")
	if code != 1 || staleOut != "" || !strings.Contains(diagnostic, "VERSION_MISMATCH") {
		t.Fatalf("walk stale version = %d, %q, %q", code, staleOut, diagnostic)
	}
	// Map the Codex account to its machine, then work the machines.
	if code, _, diagnostic := run("account", "update", id,
		"--spec-json", `{"bindings":[{"machine":{"value":"`+seed.MachineLaptop+`"},"profile":""}]}`,
		"--update-mask", "spec.bindings", "--request-id", "walk-bind"); code != 0 {
		t.Fatalf("walk bindings = %d: %s", code, diagnostic)
	}
	code, addOut, diagnostic := run("machine", "add", "--name", "walkextra", "--request-id", "walk-add")
	if code != 0 || !strings.Contains(addOut, "fleet-agent enroll --token fbe1_") {
		t.Fatalf("walk machine add = %d, %q, %q", code, addOut, diagnostic)
	}
	if code, _, diagnostic := run("machine", "rename", seed.MachineLaptop,
		"--display-name", "walk-laptop", "--expected-version", "1", "--request-id", "walk-ren"); code != 0 {
		t.Fatalf("walk rename = %d: %s", code, diagnostic)
	}
	if code, _, diagnostic := run("machine", "label", seed.MachineLaptop,
		"--labels", "walk,desk", "--expected-version", "2", "--request-id", "walk-lab"); code != 0 {
		t.Fatalf("walk label = %d: %s", code, diagnostic)
	}
}

// TestExportThenImportPlansNothing proves the CLI round trip: exporting a
// hub and importing the same document back plans no change.
func TestExportThenImportPlansNothing(t *testing.T) {
	h := startHubWithExtra(t, true, seedGitHubRow)
	code, exported, diagnostic := invoke(t, h, "export")
	if code != 0 {
		t.Fatalf("export = %d: %s", code, diagnostic)
	}
	path := writeYAML(t, exported)
	code, jsonOut, diagnostic := invokeSecret(t, h, h.secret,
		"import", path, "--dry-run", "--request-id", "cli-roundtrip", "--json")
	if code != 0 {
		t.Fatalf("round-trip dry run = %d: %s", code, diagnostic)
	}
	var res struct {
		Changes []struct {
			Entity string `json:"entity"`
			Name   string `json:"name"`
			Kind   string `json:"kind"`
		} `json:"changes"`
	}
	if err := json.Unmarshal([]byte(jsonOut), &res); err != nil {
		t.Fatal(err)
	}
	for _, c := range res.Changes {
		if c.Kind != "CONFIG_CHANGE_KIND_UNCHANGED" {
			t.Errorf("round-trip line %+v is not unchanged", c)
		}
	}
}

// TestImportWithoutHashNeedsAReviewedPlan proves a bare import applies
// nothing: the server only applies the hash of a plan the owner already
// saw, so the command fails and names the --dry-run then
// --expected-plan-hash flow instead of silently applying a fresh plan.
func TestImportWithoutHashNeedsAReviewedPlan(t *testing.T) {
	h := startHubWithExtra(t, false, seedGitHubRow)
	path := writeYAML(t, "format_version: 1\nrepos:\n  - repo:\n      host: github.com\n      path: acme/web\n")
	code, out, diagnostic := invokeSecret(t, h, h.secret,
		"import", path, "--request-id", "cli-imp-plain")
	if code != 1 || out != "" || !strings.Contains(diagnostic, "expected-plan-hash") {
		t.Fatalf("plain import = %d, %q, %q, want a refusal naming expected-plan-hash", code, out, diagnostic)
	}
}

// TestImportPlanQuotesUntrustedNames proves a document name with terminal
// control characters never reaches the terminal raw: the plan line quotes
// the name at the output boundary.
func TestImportPlanQuotesUntrustedNames(t *testing.T) {
	h := startHubWithExtra(t, false, seedGitHubRow)
	path := writeYAML(t, "format_version: 1\naccounts:\n  - harness:\n      name: codex\n    provider: openai\n    spec:\n      display_name: \"\\u001b[2Jpwned\"\n")
	code, out, diagnostic := invokeSecret(t, h, h.secret,
		"import", path, "--dry-run", "--request-id", "cli-imp-quote")
	if code != 0 {
		t.Fatalf("dry run = %d: %s", code, diagnostic)
	}
	if strings.Contains(out, "\x1b") {
		t.Fatalf("plan printed a raw escape sequence: %q", out)
	}
	if !strings.Contains(out, `"\x1b[2Jpwned"`) {
		t.Fatalf("plan did not quote the name: %q", out)
	}
}

// TestExportAfterMappingRendersPortableRefs proves #397: once a Codex
// account is mapped to its machine, export renders the binding with the
// machine display name instead of refusing a portable gap.
func TestExportAfterMappingRendersPortableRefs(t *testing.T) {
	h := startHubWithExtra(t, true, seedGitHubRow)
	code, created, diagnostic := invokeSecret(t, h, h.secret,
		"account", "create", "--harness", "codex", "--provider", "openai",
		"--display-name", "mapped", "--request-id", "cli-exp-map-1", "--json")
	if code != 0 {
		t.Fatalf("create = %d: %s", code, diagnostic)
	}
	id := createdAccountID(t, created)
	if code, _, diagnostic := invokeSecret(t, h, h.secret,
		"account", "update", id,
		"--spec-json", `{"bindings":[{"machine":{"value":"`+seed.MachineLaptop+`"},"profile":""}]}`,
		"--update-mask", "spec.bindings", "--request-id", "cli-exp-map-2"); code != 0 {
		t.Fatalf("bindings update = %d: %s", code, diagnostic)
	}
	code, out, diagnostic := invoke(t, h, "export")
	if code != 0 {
		t.Fatalf("mapped export = %d: %s", code, diagnostic)
	}
	if !strings.Contains(out, "machine_display_name: \"laptop\"") {
		t.Fatalf("exported bindings lack the machine display name: %q", out)
	}
	if strings.Contains(out, "machine:\n") {
		t.Fatalf("exported bindings still carry a machine id wrapper: %q", out)
	}
}
