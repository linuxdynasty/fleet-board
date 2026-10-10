package tmux_test

import (
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"testing"

	"github.com/linuxdynasty/fleet-board/internal/agent/tmux"
)

// collect parses r to its end and renders every message, failing the test on
// any error other than io.EOF.
func collect(t *testing.T, r io.Reader, opts tmux.Options) []string {
	t.Helper()
	p := tmux.NewParser(r, opts)
	var out []string
	for i := 0; ; i++ {
		if i > 1_000_000 {
			t.Fatal("parser did not reach the end of the stream")
		}
		m, err := p.Next()
		if errors.Is(err, io.EOF) {
			if m != nil {
				t.Fatalf("Next returned %T with io.EOF", m)
			}
			return out
		}
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		out = append(out, render(m))
	}
}

// render is a one-line, hand-checkable form of a message.
func render(m tmux.Message) string {
	switch v := m.(type) {
	case tmux.Reply:
		state := "ok"
		if v.Failed {
			state = "error"
		}
		return fmt.Sprintf("reply %s %q dropped=%d", state, v.Body, v.Dropped)
	case tmux.Output:
		if v.Extended {
			return fmt.Sprintf("extended-output %v age=%v %q", v.Pane, v.Age, v.Data)
		}
		return fmt.Sprintf("output %v %q", v.Pane, v.Data)
	case tmux.Pause:
		return fmt.Sprintf("pause %v", v.Pane)
	case tmux.Continue:
		return fmt.Sprintf("continue %v", v.Pane)
	case tmux.Exit:
		return fmt.Sprintf("exit %q", v.Reason)
	case tmux.SubscriptionChanged:
		idx := "-"
		if v.WindowIndex != tmux.NoID {
			idx = strconv.Itoa(v.WindowIndex)
		}
		return fmt.Sprintf("subscription %s %v %v %s %v %q", v.Name, v.Session, v.Window, idx, v.Pane, v.Value)
	case tmux.Unknown:
		return "unknown " + v.Name
	case tmux.Discarded:
		return fmt.Sprintf("discarded %s %d", v.ErrKind, v.Bytes)
	default:
		return fmt.Sprintf("unexpected %T", m)
	}
}

func checkMessages(t *testing.T, got, want []string) {
	t.Helper()
	if strings.Join(got, "\n") == strings.Join(want, "\n") {
		return
	}
	t.Errorf("messages differ\n got:\n  %s\nwant:\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
}
