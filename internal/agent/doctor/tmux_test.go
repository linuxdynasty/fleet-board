package doctor_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/linuxdynasty/fleet-board/internal/agent/doctor"
)

func TestTmux(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		out    string
		err    error
		status doctor.Status
		words  []string
	}{
		{"current", "tmux 3.4", nil, doctor.OK, []string{"tmux 3.4"}},
		{"the minimum", "tmux 3.2", nil, doctor.OK, nil},
		{"letter suffix", "tmux 3.2a", nil, doctor.OK, nil},
		{"a build from git", "tmux next-3.7", nil, doctor.OK, []string{"next-3.7"}},
		{"openbsd", "tmux openbsd-7.5", nil, doctor.OK, nil},
		{"two digits", "tmux 10.1", nil, doctor.OK, nil},
		{"too old", "tmux 3.1c", nil, doctor.Fail, []string{"3.2 or newer", "install"}},
		{"much too old", "tmux 2.9", nil, doctor.Fail, []string{"3.2 or newer"}},
		{"not installed", "", nil, doctor.Fail, []string{"not found", "PATH", "3.2 or newer"}},
		{"cannot run", "", errors.New("exec: permission denied for /secret/path"), doctor.Unknown, []string{"could not run tmux -V"}},
		{"a version doctor cannot read", "tmux wat", nil, doctor.Unknown, []string{"not recognised"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := newRig(t)
			r.tmuxOut, r.tmuxErr = tc.out, tc.err
			c := r.want("tmux", tc.status, tc.words...)
			if tc.err != nil && strings.Contains(c.Reason+c.Fix, "secret") {
				t.Errorf("error text leaked: %+v", c)
			}
		})
	}
}
