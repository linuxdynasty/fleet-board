package fleetconfig_test

import (
	"os"
	"strings"
	"testing"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	"github.com/linuxdynasty/fleet-board/internal/fleetconfig"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

const allSectionsJSON = `{
  "format_version": 1,
  "hub": {"auto_dispatch": false, "log_size_cap_bytes": "9007199254740993", "triage_expiry": "3600s"},
  "fleet_values": {"max_active": 0, "default_server": "DEFAULT_SERVER_MODE_CONTROL", "spool_cap_bytes": "18446744073709551615"},
  "groups": [{"label": "yes", "priority": 2, "values": {"max_total": 3}}],
  "machines": [{"name": "café", "labels": ["a", "b"], "values": {"share_identity_email": false}}],
  "repos": [{"repo": {"host": "github.com", "path": "acme/tool"}, "worktree_mode": "REPO_WORKTREE_MODE_AGENT_ENSURES", "branch_pattern": "line one\nline two"}],
  "integrations": [{"provider_name": "github", "base_url": "https://github.com", "label": "main"}],
  "routines": [{"display_name": "sweep", "enabled": true, "done_rule": "DONE_RULE_GOAL_DONE_ONLY", "blocked_after": "7200s"}],
  "accounts": [{"harness": {"name": "codex"}, "provider": "openai", "spec": {"display_name": "work", "spend_cap_micros": "-9007199254740993", "consent": ["CONSENT_ACT_PUSH", "CONSENT_ACT_MERGE"]}}]
}`

func TestGoldenAllSections(t *testing.T) {
	t.Parallel()
	var source hubv1.FleetConfig
	if err := (protojson.UnmarshalOptions{}).Unmarshal([]byte(allSectionsJSON), &source); err != nil {
		t.Fatal(err)
	}
	before := proto.Clone(&source)
	encoded, err := fleetconfig.Encode(&source)
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile("testdata/all_sections.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != string(want) {
		t.Fatalf("Encode differed from hand-written golden:\n%s", encoded)
	}
	if !proto.Equal(&source, before) {
		t.Fatal("Encode mutated its input")
	}
	for _, fragment := range []string{
		"format_version: 1\n", "accounts:\n", "fleet_values:\n", "groups:\n", "hub:\n",
		"integrations:\n", "machines:\n", "repos:\n", "routines:\n",
		"9007199254740993", "18446744073709551615", "-9007199254740993",
		"DEFAULT_SERVER_MODE_CONTROL", "REPO_WORKTREE_MODE_AGENT_ENSURES",
		"DONE_RULE_GOAL_DONE_ONLY", "CONSENT_ACT_PUSH", "CONSENT_ACT_MERGE",
		"auto_dispatch: false", "max_active: 0", "share_identity_email: false",
		"\"yes\"", "café", "line one\\nline two", "3600s", "7200s",
	} {
		if !strings.Contains(string(encoded), fragment) {
			t.Errorf("encoded YAML lacks %q:\n%s", fragment, encoded)
		}
	}
	decoded, err := fleetconfig.Decode(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if !proto.Equal(&source, decoded) {
		t.Fatalf("decoded message changed: %s", encoded)
	}
	again, err := fleetconfig.Encode(decoded)
	if err != nil || string(again) != string(encoded) {
		t.Fatalf("second encode differs: %v", err)
	}
	if strings.Count(string(encoded), "format_version:") != 1 || !strings.HasSuffix(string(encoded), "\n") || strings.HasSuffix(string(encoded), "\n\n") {
		t.Fatal("version or trailing newline is not canonical")
	}
}

func TestProviderOwnedUserIDSurvives(t *testing.T) {
	input := []byte("format_version: 1\nhub:\n  trusted_gate:\n    users:\n      - login: alice\n        provider_user_id: \"12345\"\n")
	decoded, err := fleetconfig.Decode(input)
	if err != nil {
		t.Fatal(err)
	}
	if got := decoded.GetHub().GetTrustedGate().GetUsers()[0].GetProviderUserId(); got != "12345" {
		t.Fatalf("provider user ID = %q", got)
	}
	encoded, err := fleetconfig.Encode(decoded)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), "provider_user_id: \"12345\"") {
		t.Fatalf("provider-owned ID missing from %s", encoded)
	}
}

func TestUnquotedLargeIntegerIsExact(t *testing.T) {
	input := []byte("format_version: 1\nhub: {log_size_cap_bytes: 9007199254740993}\n")
	decoded, err := fleetconfig.Decode(input)
	if err != nil {
		t.Fatal(err)
	}
	if got := decoded.GetHub().GetLogSizeCapBytes(); got != 9007199254740993 {
		t.Fatalf("64-bit integer changed to %d", got)
	}
	encoded, err := fleetconfig.Encode(decoded)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), "log_size_cap_bytes: \"9007199254740993\"") {
		t.Fatalf("canonical 64-bit integer was not quoted: %s", encoded)
	}
}
