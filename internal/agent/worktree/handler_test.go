package worktree_test

import (
	"context"
	"testing"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/exec"
	"github.com/linuxdynasty/fleet-board/internal/agent/worktree"
)

func handlerByKind(t *testing.T, m *worktree.Manager) map[string]exec.Handler {
	t.Helper()
	out := map[string]exec.Handler{}
	for _, h := range m.Handlers() {
		if out[h.Kind()] != nil {
			t.Fatalf("two handlers for %s", h.Kind())
		}
		out[h.Kind()] = h
	}
	return out
}

func TestHandlerKindsAndCapabilities(t *testing.T) {
	t.Parallel()
	m, _ := testManager(t, []string{t.TempDir()}, repoTable{})
	handlers := handlerByKind(t, m)
	want := map[string]string{
		"EnsureWorktree": "worktree",
		"PruneWorktree":  "worktree",
		"EnsureScratch":  "scratch",
		"CollectScratch": "scratch",
	}
	if len(handlers) != len(want) {
		t.Fatalf("handlers = %v, want %d", handlers, len(want))
	}
	for kind, capName := range want {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			h := handlers[kind]
			if h == nil {
				t.Fatalf("no handler for %s", kind)
			}
			caps := h.Capabilities()
			if len(caps) != 1 || caps[0].GetName() != capName || caps[0].GetVersion() != 1 {
				t.Fatalf("capabilities = %v, want [%s v1]", caps, capName)
			}
			if h.Class(nil) != exec.RepeatSafe {
				t.Fatalf("class = %v, want repeat_safe", h.Class(nil))
			}
			// A handler that gets another kind fails closed.
			other := &agentv1.Command{Kind: &agentv1.Command_EnsureSession{EnsureSession: &agentv1.EnsureSession{}}}
			res := h.Run(context.Background(), exec.Request{Command: other})
			if res.GetStatus() != typev1.Status_STATUS_FAILED || res.GetReason() != typev1.ReasonCode_REASON_CODE_INTERNAL {
				t.Fatalf("wrong kind: %v/%v", res.GetStatus(), res.GetReason())
			}
		})
	}
}

func TestHandlersDispatch(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	t.Run("scratch through the handler", func(t *testing.T) {
		t.Parallel()
		m, _ := testManager(t, []string{t.TempDir()}, repoTable{})
		h := handlerByKind(t, m)["EnsureScratch"]
		cmd := &agentv1.Command{Kind: &agentv1.Command_EnsureScratch{EnsureScratch: &agentv1.EnsureScratch{
			Goal:  &typev1.GoalId{Value: "goalh1"},
			Files: []*agentv1.ScratchFile{scratchFile("in.json", "{}\n")},
		}}}
		if res := h.Run(ctx, exec.Request{Command: cmd}); res.GetStatus() != typev1.Status_STATUS_OK {
			t.Fatalf("handler ensure scratch: %v", res)
		}
	})
	t.Run("worktree through the handler", func(t *testing.T) {
		t.Parallel()
		top := realTop(t)
		m, _ := testManager(t, []string{top}, repoTable{})
		h := handlerByKind(t, m)["EnsureWorktree"]
		cmd := &agentv1.Command{Kind: &agentv1.Command_EnsureWorktree{EnsureWorktree: ensureCmd("goalh2", "fleet/h2", "", "")}}
		res := h.Run(ctx, exec.Request{Command: cmd})
		if res.GetStatus() != typev1.Status_STATUS_FAILED || res.GetReason() != typev1.ReasonCode_REASON_CODE_CHECKOUT_MISSING {
			t.Fatalf("status = %v/%v, want FAILED/CHECKOUT_MISSING", res.GetStatus(), res.GetReason())
		}
	})
}
