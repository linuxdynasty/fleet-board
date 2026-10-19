package fleetconfig_test

import (
	"strings"
	"testing"

	"github.com/linuxdynasty/fleet-board/internal/fleetconfig"
)

func TestDecodeRejectsInvalidDocument(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		text string
	}{
		{"empty", ""},
		{"scalar", "1\n"},
		{"sequence", "- format_version: 1\n"},
		{"missing version", "groups: []\n"},
		{"unknown version", "format_version: 2\n"},
		{"string version", "format_version: \"1\"\n"},
		{"duplicate", "format_version: 1\nformat_version: 1\n"},
		{"mixed spelling", "format_version: 1\nformatVersion: 1\n"},
		{"nested unknown", "format_version: 1\ngroups:\n  - label: x\n    secret_token: canary-secret\n"},
		{"nested mixed spelling", "format_version: 1\nfleet_values: {max_active: 1, maxActive: 1}\n"},
		{"non-string key", "format_version: 1\n1: x\n"},
		{"explicit bad bool", "format_version: 1\nhub: {auto_dispatch: !!bool Yes}\n"},
		{"multiple documents", "format_version: 1\n---\nformat_version: 1\n"},
		{"merge key", "format_version: 1\nhub: {<<: {auto_dispatch: true}}\n"},
		{"anchor", "format_version: 1\ngroups: &x []\n"},
		{"alias", "format_version: 1\ngroups: *x\n"},
		{"custom tag", "format_version: 1\ngroups: !custom []\n"},
		{"octal", "format_version: 1\ngroups: [{priority: 0o12}]\n"},
		{"hex", "format_version: 1\ngroups: [{priority: 0x12}]\n"},
		{"nan", "format_version: 1\ngroups: [{priority: .nan}]\n"},
		{"numeric enum", "format_version: 1\nrepos: [{worktree_mode: 1}]\n"},
		{"unknown enum", "format_version: 1\nrepos: [{worktree_mode: REPO_WORKTREE_MODE_UNKNOWN}]\n"},
		{"bad number", "format_version: 1\ngroups: [{priority: 01}]\n"},
		{"yaml null shorthand", "format_version: 1\ngroups: [{priority: ~}]\n"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			value, err := fleetconfig.Decode([]byte(tc.text))
			if err == nil || value != nil {
				t.Fatalf("Decode accepted %s", tc.name)
			}
			if strings.Contains(err.Error(), "canary-secret") || len(err.Error()) > 180 {
				t.Fatalf("unsafe error: %q", err)
			}
		})
	}
}

func TestDecodeAcceptsCamelCaseAliases(t *testing.T) {
	t.Parallel()
	got, err := fleetconfig.Decode([]byte("formatVersion: 1\nfleetValues:\n  maxActive: 0\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got.GetFleetValues().MaxActive == nil || got.GetFleetValues().GetMaxActive() != 0 {
		t.Fatal("explicit optional zero was lost")
	}
}
