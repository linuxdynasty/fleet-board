package doctor_test

import (
	"strings"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/agent/doctor"
	"github.com/linuxdynasty/fleet-board/internal/agent/statusfile"
)

func TestPushSocket(t *testing.T) {
	t.Parallel()
	t.Run("off and fresh warns and names the poll fallback", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		r.writeStatus(time.Second, func(s *statusfile.Status) { s.PushOff = true })
		r.want("push_socket", doctor.Warn, "push socket is off", "every 15 seconds", "Restart the agent")
	})
	t.Run("on and fresh is ok", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		r.writeStatus(time.Second, nil)
		r.want("push_socket", doctor.OK, "push socket")
	})
	t.Run("an old file says nothing about now", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		r.writeStatus(twoHours, func(s *statusfile.Status) { s.PushOff = true })
		r.want("push_socket", doctor.Unknown, "not running")
	})
	t.Run("no file is unknown", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		r.want("push_socket", doctor.Unknown, "status.json")
	})
}

func TestMDNSBrowse(t *testing.T) {
	t.Parallel()
	browse := func(result string) func(*statusfile.Status) {
		return func(s *statusfile.Status) {
			s.Browse = &statusfile.Browse{Result: result, Time: now.Add(-10 * time.Minute), Kept: 2, Dropped: 1}
		}
	}
	tests := []struct {
		name   string
		result string
		status doctor.Status
		words  []string
	}{
		{"found", statusfile.BrowseFound, doctor.OK, []string{"found a hub", "10m0s ago"}},
		{"none", statusfile.BrowseNone, doctor.Warn, []string{"found no hub", "10m0s ago", "--hub", "firewall"}},
		{"no multicast interface", statusfile.BrowseNoMulticastInterface, doctor.Warn, []string{"no network interface", "10m0s ago", "--hub", "firewall"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := newRig(t)
			r.writeStatus(time.Second, browse(tc.result))
			r.want("mdns_browse", tc.status, tc.words...)
		})
	}
	t.Run("an old file still shows the last browse, like the clock", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		r.writeStatus(twoHours, browse(statusfile.BrowseFound))
		r.want("mdns_browse", doctor.OK, "10m0s ago")
	})
	t.Run("no browse yet is unknown", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		r.writeStatus(time.Second, nil)
		r.want("mdns_browse", doctor.Unknown, "no browse since the agent started", "a cached or configured address was tried")
	})
	t.Run("a result from a newer agent is unknown", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		r.writeStatus(time.Second, browse("brand_new"))
		r.want("mdns_browse", doctor.Unknown, "does not know")
	})
	t.Run("no file is unknown", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		r.want("mdns_browse", doctor.Unknown, "status.json")
	})
}

func TestChecksBuildCannotRunYet(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	// probe_trust used to be listed here: the probe seam (row 8, PR #292)
	// now exists, so it runs a real check instead of a placeholder.
	for _, id := range []string{"harness_paths"} {
		r.want(id, doctor.NotChecked, "(row 8)")
	}
}

func TestLocalChecksOrder(t *testing.T) {
	t.Parallel()
	var ids []string
	for _, c := range doctor.Local() {
		ids = append(ids, c.ID)
	}
	got := strings.Join(ids, ",")
	if !strings.Contains(got, "connection,push_socket,mdns_browse,snapshot_size") {
		t.Errorf("the push socket and the browse follow the connection: %s", got)
	}
	if len(ids) != 24 {
		t.Errorf("%d checks, want 24", len(ids))
	}
}
