package ingest_test

import (
	"strings"
	"testing"
	"time"
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

// A hostile policy error far over its cap is cut before the scrubber runs,
// so it costs what a short one does (04b): the uncut 512 KiB takes the
// scrubber seconds.
func TestCleanCutsBeforeItScrubs(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("password=x ", (512<<10)/11)
	snap := &agentv1.Snapshot{Policy: &agentv1.PolicyInfo{Error: long}, Facts: &agentv1.MachineFacts{AgentVersion: long}}
	start := time.Now()
	ingest.Clean(snap)
	if d := time.Since(start); d > time.Second {
		t.Errorf("Clean took %v on 2 x 512 KiB, want well under a second", d)
	}
	if n := len(snap.GetPolicy().GetError()); n > 256 || n == 0 {
		t.Errorf("policy error %d bytes, want 1 to 256", n)
	}
}

// A Snapshot without facts, resources or policy is left alone.
func TestCleanOfAnEmptySnapshot(t *testing.T) {
	t.Parallel()
	if got := ingest.Clean(&agentv1.Snapshot{}); got != (ingest.Cleaned{}) {
		t.Errorf("Clean(empty) = %+v, want zero", got)
	}
}
