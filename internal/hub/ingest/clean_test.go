package ingest_test

import (
	"strings"
	"testing"
	"unicode/utf8"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/ingest"
)

// Clean caps every string of the facts, the resources and the policy at
// its cap, drops control characters and invalid UTF-8, and scrubs the
// version strings and the policy error; a hostname that only looks like a
// secret is capped, never scrubbed.
func TestCleanCapsAndScrubsTheSnapshotsText(t *testing.T) {
	t.Parallel()
	token := "ghp_" + strings.Repeat("A", 36)
	snap := &agentv1.Snapshot{
		Facts: &agentv1.MachineFacts{
			Hostname: "host\x00name" + strings.Repeat("h", 300), AgentVersion: "1.0 " + token, OsVersion: "15.1",
			TmuxVersion: "tmux 3.5\n", PolicySchemaVersion: "1",
		},
		Resources: &agentv1.ResourceSample{Disk: make([]*agentv1.DiskFree, 40)},
		Policy:    &agentv1.PolicyInfo{Hash: "abc", Error: "bad key " + token + " \xff at line 3"},
	}
	for i := range snap.GetResources().GetDisk() {
		snap.Resources.Disk[i] = &agentv1.DiskFree{Path: "/data/" + strings.Repeat("p", 2000)}
	}
	got := ingest.Clean(snap)
	f, p := snap.GetFacts(), snap.GetPolicy()
	if want := "hostname" + strings.Repeat("h", 248); f.GetHostname() != want {
		t.Errorf("hostname %q, want the control byte dropped and 256 bytes", f.GetHostname())
	}
	if f.GetAgentVersion() != "1.0 ghp_[redacted]" || f.GetTmuxVersion() != "tmux 3.5" || f.GetOsVersion() != "15.1" {
		t.Errorf("versions %q %q %q", f.GetAgentVersion(), f.GetTmuxVersion(), f.GetOsVersion())
	}
	if p.GetError() != "bad key ghp_[redacted]  at line 3" || p.GetHash() != "abc" {
		t.Errorf("policy %q %q", p.GetError(), p.GetHash())
	}
	if n := len(snap.GetResources().GetDisk()); n != 32 {
		t.Errorf("%d disk entries, want the 32 the agent reads at most", n)
	}
	if path := snap.GetResources().GetDisk()[0].GetPath(); len(path) != 1024 || !utf8.ValidString(path) {
		t.Errorf("disk path %d bytes, want 1024", len(path))
	}
	if got.Scrubbed != 2 || got.Cut == 0 {
		t.Errorf("Clean = %+v, want 2 scrubbed fields and some cut", got)
	}
}

// Hostile text far over its cap (the slowest shape for the scrubber, from
// internal/scrub's hub pass benchmark: glued keys of alternating kinds,
// then a long run of "--n") is cut to the field's 256-byte cap before the
// scrubber runs (04b): the scrubber is handed at most 256 bytes per field.
func TestCleanCutsToTheCapBeforeItScrubs(t *testing.T) {
	t.Parallel()
	aws, google := "AKIA"+strings.Repeat("Z", 16), "AIza"+strings.Repeat("x", 35)
	keys := aws + google + aws + google + " "
	hostile := keys + strings.Repeat("--n", (160<<10)/3)
	snap := &agentv1.Snapshot{
		Facts: &agentv1.MachineFacts{
			OsVersion: hostile, AgentVersion: hostile, TmuxVersion: hostile, PolicySchemaVersion: hostile,
		},
		Policy: &agentv1.PolicyInfo{Error: hostile, SchemaVersion: hostile},
	}
	got := ingest.Clean(snap)
	if got.ScrubBytes > 6*256 || got.ScrubBytes == 0 {
		t.Errorf("the scrubber was handed %d bytes for 6 fields, want at most 6 x 256", got.ScrubBytes)
	}
	for _, v := range []string{snap.GetFacts().GetOsVersion(), snap.GetPolicy().GetError()} {
		if len(v) > 256 || strings.Contains(v, "AKIAZZ") {
			t.Errorf("field %q (%d bytes), want at most 256 bytes and no key", v[:min(len(v), 40)], len(v))
		}
	}
}

// A cut never splits a character, and invisible format characters (a
// direction override, a zero-width space) and line or paragraph
// separators are dropped like control characters (02b, display spoofing).
func TestCleanCutsOnACharacterAndDropsFormatCharacters(t *testing.T) {
	t.Parallel()
	wide := "a" + strings.Repeat("é", 200)
	snap := &agentv1.Snapshot{
		Facts:  &agentv1.MachineFacts{Hostname: "lap\u202etop\u200b x\u2028\u2029", Username: wide, AgentVersion: wide},
		Policy: &agentv1.PolicyInfo{Hash: wide},
	}
	ingest.Clean(snap)
	if got := snap.GetFacts().GetHostname(); got != "laptop x" {
		t.Errorf("hostname %q, want laptop x", got)
	}
	want := "a" + strings.Repeat("é", 127) // 255 bytes: a 256th would split an é
	for name, v := range map[string]string{
		"username": snap.GetFacts().GetUsername(), "agent_version": snap.GetFacts().GetAgentVersion(),
		"policy hash": snap.GetPolicy().GetHash(),
	} {
		if v != want || !utf8.ValidString(v) {
			t.Errorf("%s %d bytes (valid %v), want the 255-byte prefix on a character boundary", name, len(v), utf8.ValidString(v))
		}
	}
}

// A Snapshot without facts, resources or policy is left alone.
func TestCleanOfAnEmptySnapshot(t *testing.T) {
	t.Parallel()
	if got := ingest.Clean(&agentv1.Snapshot{}); got != (ingest.Cleaned{}) {
		t.Errorf("Clean(empty) = %+v, want zero", got)
	}
}
