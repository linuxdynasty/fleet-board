package api

import (
	"encoding/hex"
	"strings"
	"testing"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"google.golang.org/protobuf/proto"
)

// storedImportSpec is a stored repo spec: enabled, reaching code host
// "github", like CreateRepo leaves it.
func storedImportSpec(enabled bool) *hubv1.RepoSpec {
	return &hubv1.RepoSpec{
		Repo: &typev1.RepoRef{
			Host: "github.com", Path: "acme/api",
			CodeHost: &typev1.IntegrationId{Value: "github"},
		},
		Enabled: enabled,
	}
}

func TestPlanConfigHashIsStableAndSensitive(t *testing.T) {
	t.Parallel()
	lines := []*hubv1.ConfigChange{
		{
			Entity: hubv1.ConfigEntity_CONFIG_ENTITY_REPO, Name: "github.com/acme/api",
			Kind: hubv1.ConfigChangeKind_CONFIG_CHANGE_KIND_CREATE,
		},
		{
			Entity: hubv1.ConfigEntity_CONFIG_ENTITY_REPO, Name: "github.com/acme/web",
			Kind:          hubv1.ConfigChangeKind_CONFIG_CHANGE_KIND_UPDATE,
			ChangedFields: []string{"spec.enabled", "spec.consent"},
		},
	}
	first, second := planConfigHash(lines), planConfigHash([]*hubv1.ConfigChange{lines[1], lines[0]})
	if first != second {
		t.Errorf("hash depends on line order: %q vs %q", first, second)
	}
	raw, err := hex.DecodeString(first)
	if err != nil || len(raw) != 32 {
		t.Errorf("hash %q is not 64 hex chars of SHA-256: %v", first, err)
	}
	mutants := []func(*hubv1.ConfigChange){
		func(c *hubv1.ConfigChange) { c.Kind = hubv1.ConfigChangeKind_CONFIG_CHANGE_KIND_UNCHANGED },
		func(c *hubv1.ConfigChange) { c.Name = "github.com/acme/other" },
		func(c *hubv1.ConfigChange) { c.ChangedFields = []string{"spec.consent"} },
		func(c *hubv1.ConfigChange) {
			c.Problem = &typev1.ErrorDetail{
				Reason: typev1.ReasonCode_REASON_CODE_CONFIG_INVALID,
				Violations: []*typev1.FieldViolation{{
					Field:       "config.repos[1]",
					Reason:      typev1.ReasonCode_REASON_CODE_CONFIG_INVALID,
					Description: "no code-host integration reaches this repo",
				}},
			}
		},
	}
	for i, mutate := range mutants {
		clone := []*hubv1.ConfigChange{proto.Clone(lines[0]).(*hubv1.ConfigChange), proto.Clone(lines[1]).(*hubv1.ConfigChange)} //nolint:forcetypeassert // Clone keeps the message type
		mutate(clone[1])
		if got := planConfigHash(clone); got == first {
			t.Errorf("mutant %d leaves the hash unchanged", i)
		}
	}
	if got := planConfigHash(nil); got == first {
		t.Error("empty plan hashes like a two-line plan")
	}
}

func TestPlanConfigImportDiffsRepos(t *testing.T) {
	t.Parallel()
	old := storedImportSpec(true)
	old.Repo.Path = "acme/old"
	changedOld := storedImportSpec(false)
	changedOld.Repo.Path = "acme/old"
	existing := map[string]*hubv1.RepoSpec{
		"github.com/acme/api": storedImportSpec(true),
		"github.com/acme/old": old,
	}
	doc := &hubv1.FleetConfig{FormatVersion: 1, Repos: []*hubv1.RepoSpec{
		storedImportSpec(true),
		{Repo: &typev1.RepoRef{Host: "github.com", Path: "acme/web"}, Enabled: true},
		changedOld,
	}}
	codeHosts := map[string]string{"github.com": "github"}
	changes, err := planConfigImport(doc, existing, codeHosts, nil)
	if err != nil {
		t.Fatalf("planConfigImport: %v", err)
	}
	byName := map[string]*hubv1.ConfigChange{}
	for _, c := range changes {
		byName[c.GetName()] = c
	}
	if len(changes) != 3 {
		t.Fatalf("plan has %d lines, want 3", len(changes))
	}
	if got := byName["github.com/acme/api"].GetKind(); got != hubv1.ConfigChangeKind_CONFIG_CHANGE_KIND_UNCHANGED {
		t.Errorf("api line = %v, want UNCHANGED", got)
	}
	if got := byName["github.com/acme/old"].GetKind(); got != hubv1.ConfigChangeKind_CONFIG_CHANGE_KIND_UPDATE {
		t.Errorf("old line = %v, want UPDATE", got)
	}
	fields := byName["github.com/acme/old"].GetChangedFields()
	if len(fields) != 1 || fields[0] != "spec.enabled" {
		t.Errorf("changed_fields = %v, want [spec.enabled]", fields)
	}
	if got := byName["github.com/acme/web"].GetKind(); got != hubv1.ConfigChangeKind_CONFIG_CHANGE_KIND_CREATE {
		t.Errorf("web line = %v, want CREATE", got)
	}
}

func TestPlanConfigImportRejectsBadRepoEntries(t *testing.T) {
	t.Parallel()
	doc := &hubv1.FleetConfig{FormatVersion: 1, Repos: []*hubv1.RepoSpec{
		{Repo: &typev1.RepoRef{Host: "github.com", Path: "acme/api"}, Enabled: true},
		{Repo: &typev1.RepoRef{Host: "github.com", Path: "acme/api"}, Enabled: true},
		{Repo: &typev1.RepoRef{Host: "github.com", Path: "ACME/API.GIT"}, Enabled: true},
		{Repo: &typev1.RepoRef{Host: "example.com", Path: "acme/api"}, Enabled: true},
	}}
	changes, err := planConfigImport(doc, nil, map[string]string{"github.com": "github"}, nil)
	if err != nil {
		t.Fatalf("planConfigImport: %v", err)
	}
	if len(changes) != 4 {
		t.Fatalf("plan has %d lines, want 4", len(changes))
	}
	if got := changes[0].GetKind(); got != hubv1.ConfigChangeKind_CONFIG_CHANGE_KIND_CREATE {
		t.Errorf("first entry = %v, want CREATE", got)
	}
	problemText := func(c *hubv1.ConfigChange) string {
		text := ""
		for _, v := range c.GetProblem().GetViolations() {
			text += v.GetField() + ": " + v.GetDescription() + "; "
		}
		return text
	}
	for i, want := range []string{"duplicated", "lowercase"} {
		c := changes[i+1]
		if c.GetKind() != hubv1.ConfigChangeKind_CONFIG_CHANGE_KIND_SKIPPED {
			t.Errorf("entry %d kind = %v, want SKIPPED", i+1, c.GetKind())
		}
		if reason := c.GetProblem().GetReason(); reason != typev1.ReasonCode_REASON_CODE_CONFIG_INVALID {
			t.Errorf("entry %d reason = %v, want CONFIG_INVALID", i+1, reason)
		}
		if text := problemText(c); !strings.Contains(text, want) {
			t.Errorf("entry %d problem = %q, want it to mention %q", i+1, text, want)
		}
	}
	// A repo no integration reaches is a waivable skip, not a validation
	// failure: allow_skipped applies the rest.
	missing := changes[3]
	if missing.GetKind() != hubv1.ConfigChangeKind_CONFIG_CHANGE_KIND_SKIPPED {
		t.Errorf("missing integration kind = %v, want SKIPPED", missing.GetKind())
	}
	if reason := missing.GetProblem().GetReason(); reason != typev1.ReasonCode_REASON_CODE_RESOURCE_NOT_FOUND {
		t.Errorf("missing integration reason = %v, want RESOURCE_NOT_FOUND", reason)
	}
	if text := problemText(missing); !strings.Contains(text, "code-host") {
		t.Errorf("missing integration problem = %q, want it to name the code host", text)
	}
}

func TestPlanConfigImportMarksUnsupportedSections(t *testing.T) {
	t.Parallel()
	doc := &hubv1.FleetConfig{
		FormatVersion: 1,
		Hub:           &hubv1.HubSettings{AutoDispatch: proto.Bool(true)},
		Groups:        []*hubv1.GroupConfig{{Label: "g"}},
		Accounts:      []*hubv1.AccountConfig{{Spec: &hubv1.AccountSpec{DisplayName: "codex"}}},
	}
	changes, err := planConfigImport(doc, nil, nil, nil)
	if err != nil {
		t.Fatalf("planConfigImport: %v", err)
	}
	byName := map[string]*hubv1.ConfigChange{}
	for _, c := range changes {
		byName[c.GetName()] = c
	}
	for _, want := range []string{"hub", "g", "codex"} {
		c := byName[want]
		if c == nil || c.GetKind() != hubv1.ConfigChangeKind_CONFIG_CHANGE_KIND_SKIPPED ||
			c.GetProblem().GetReason() != typev1.ReasonCode_REASON_CODE_CONFIG_INVALID {
			t.Errorf("line %q = %v, want SKIPPED with CONFIG_INVALID", want, c)
		}
	}
}

func TestPlanConfigImportEnforcesTheLineCap(t *testing.T) {
	t.Parallel()
	doc := &hubv1.FleetConfig{FormatVersion: 1}
	for i := 0; i < importPlanLineCap+1; i++ {
		doc.Machines = append(doc.Machines, &hubv1.MachineConfig{Name: "m"})
	}
	if _, err := planConfigImport(doc, nil, nil, nil); err == nil {
		t.Fatalf("plan over %d lines accepted", importPlanLineCap)
	}
}
