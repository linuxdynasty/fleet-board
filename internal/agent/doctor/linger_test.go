package doctor_test

import (
	"context"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/agent/doctor"
)

func lingerOpts(r *rig, goos string, fn func(ctx context.Context) (string, error), user string) doctor.Options {
	o := r.opts()
	o.GOOS = goos
	o.Linger = fn
	o.LingerUser = user
	return o
}

func runLinger(r *rig, goos string, fn func(ctx context.Context) (string, error), user string) doctor.Result {
	t := r.t
	t.Helper()
	rep := doctor.Run(t.Context(), lingerOpts(r, goos, fn, user), doctor.Local()...)
	for _, c := range rep.Checks {
		if c.ID == "linger" {
			return c
		}
	}
	t.Fatal(`no check "linger"`)
	return doctor.Result{}
}

func TestLingerIsNotNeededOnMacOS(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	called := false
	got := runLinger(r, "darwin", func(ctx context.Context) (string, error) {
		called = true
		return "Linger=no\n", nil
	}, "owner")
	if got.Status != doctor.OK {
		t.Errorf("status = %s (%q), want ok on darwin", got.Status, got.Reason)
	}
	if called {
		t.Error("linger ran loginctl on darwin")
	}
}

func TestLingerOnWhenEnabled(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	got := runLinger(r, "linux", func(ctx context.Context) (string, error) {
		return "Linger=yes\n", nil
	}, "owner")
	if got.Status != doctor.OK {
		t.Errorf("status = %s (%q), want ok", got.Status, got.Reason)
	}
}

func TestLingerOffFailsWithFix(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	got := runLinger(r, "linux", func(ctx context.Context) (string, error) {
		return "Linger=no\n", nil
	}, "owner")
	if got.Status != doctor.Fail {
		t.Fatalf("status = %s (%q), want fail", got.Status, got.Reason)
	}
	text := got.Reason + " | " + got.Fix
	for _, w := range []string{"enable-linger", "owner"} {
		found := false
		for i := 0; i+len(w) <= len(text); i++ {
			if text[i:i+len(w)] == w {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("%q does not mention %q", text, w)
		}
	}
}

func TestLingerWithoutAFuncIsUnknown(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	got := runLinger(r, "linux", nil, "owner")
	if got.Status != doctor.Unknown {
		t.Errorf("status = %s, want unknown", got.Status)
	}
	_ = time.Now
}
