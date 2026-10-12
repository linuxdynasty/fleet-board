package statusfile

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/agent/conn"
)

// status.json names the tmux socket folder the agent watches, and says
// tmux_watch: "off" when that watch could not be set (row-7 ruling fork
// 4(b)); a working watch leaves tmux_watch out.
func TestTmuxWatchIsWritten(t *testing.T) {
	t.Parallel()
	r, _ := newRig(t)
	_ = r.waitWrite(t)
	got := r.read(t)
	if got.TmuxWatch != "" || got.TmuxFolder != tmuxFolder {
		t.Fatalf("tmux_watch %q, tmux_folder %q with the watch working", got.TmuxWatch, got.TmuxFolder)
	}
	r.clock.wantTimer(t, time.Minute)
	r.tmuxOff.Store(true)
	r.w.Notify()
	r.clock.wantTimer(t, time.Second)
	r.clock.Advance(time.Second)
	if err := r.waitWrite(t); err != nil {
		t.Fatal(err)
	}
	if got := r.read(t); got.TmuxWatch != TmuxWatchOff || got.TmuxFolder != tmuxFolder {
		t.Errorf("tmux_watch %q, tmux_folder %q; want off and the folder", got.TmuxWatch, got.TmuxFolder)
	}
}

// Only the closed word survives a read of tmux_watch, and only an absolute,
// printable, bounded path survives in tmux_folder (a damaged or hostile
// file reads as unknown).
func TestReadCleansTmuxWatch(t *testing.T) {
	t.Parallel()
	tests := []struct{ watch, folder, wantWatch, wantFolder string }{
		{"off", "/tmp/tmux-501", "off", "/tmp/tmux-501"},
		{"", "", "", ""},
		{"on", "relative/tmux-501", "unknown", ""},
		{"off", "/tmp/\u001b[31mred", "off", ""},
		{"off", "/" + strings.Repeat("a", 300), "off", ""},
	}
	for _, tc := range tests {
		dir := t.TempDir()
		st := FromConn(conn.Status{State: conn.StateConnected}, t0)
		st.TmuxWatch, st.TmuxFolder = tc.watch, tc.folder
		b, err := json.Marshal(st)
		if err != nil {
			t.Fatal(err)
		}
		write(t, dir, StatusFile, string(b))
		got, err := Read(dir)
		if err != nil {
			t.Fatal(err)
		}
		if got.TmuxWatch != tc.wantWatch || got.TmuxFolder != tc.wantFolder {
			t.Errorf("(%q, %q) read as (%q, %q), want (%q, %q)", tc.watch, tc.folder, got.TmuxWatch, got.TmuxFolder, tc.wantWatch, tc.wantFolder)
		}
	}
}
