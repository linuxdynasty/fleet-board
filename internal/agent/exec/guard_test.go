package exec_test

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"testing"

	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	fexec "github.com/linuxdynasty/fleet-board/internal/agent/exec"
	"github.com/linuxdynasty/fleet-board/internal/agent/ring"
)

// withKind sets the arm of Command.kind whose message is named kind.
func withKind(t *testing.T, c *agentv1.Command, kind string) {
	t.Helper()
	m := c.ProtoReflect()
	fields := m.Descriptor().Oneofs().ByName("kind").Fields()
	for i := range fields.Len() {
		f := fields.Get(i)
		if string(f.Message().Name()) == kind {
			m.Set(f, m.NewField(f))
			return
		}
	}
	t.Fatalf("no kind %s", kind)
}

// field99 is c with the reserved RunExec number 99 on the wire: an agent
// that reads it sees no kind arm at all.
func field99(t *testing.T, c *agentv1.Command) *agentv1.Command {
	t.Helper()
	c.Kind = nil
	b, err := proto.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	b = protowire.AppendTag(b, 99, protowire.BytesType)
	b = protowire.AppendBytes(b, []byte("tmux kill-server"))
	out := &agentv1.Command{}
	if err := proto.Unmarshal(b, out); err != nil {
		t.Fatal(err)
	}
	return out
}

// Principle 4 guard (row 6): with no handler advertising a capability,
// every kind arm, an empty kind and the reserved field 99 are answered
// UNSUPPORTED with the capability they asked for. The command is logged,
// acked and answered once (ruling fork 4 i), and nothing else happens: a
// spy handler registered for every kind, advertising nothing, never runs.
func TestEveryKindIsUnsupportedWithNoSideEffect(t *testing.T) {
	t.Parallel()
	var spies []fexec.Handler
	var all []*handler
	for _, k := range fexec.Kinds() {
		h := &handler{kind: k}
		spies, all = append(spies, h), append(all, h)
	}
	r := newRig(t, spies...)
	r.start(t)
	cases := append(fexec.Kinds(), "nil kind", "field 99")
	for i, k := range cases {
		id := fmt.Sprintf("g-%d", i)
		c := cmd(id, uint64(i+1), "tmux.control")
		switch k {
		case "nil kind":
			c.Kind = nil
		case "field 99":
			c = field99(t, c)
		default:
			withKind(t, c, k)
		}
		r.give(t, c)
		res := r.result(t, id)
		if res.GetStatus() != typev1.Status_STATUS_UNSUPPORTED || res.GetMissingCapability().GetName() != "tmux.control" {
			t.Errorf("%s: result %v, want UNSUPPORTED missing tmux.control", k, res)
		}
		if acks, _, results := r.count(id); acks != 1 || results != 1 {
			t.Errorf("%s: %d acks and %d results, want one of each", k, acks, results)
		}
		e, err := r.db.Command(context.Background(), id)
		if err != nil || e.Status != ring.StatusFinished {
			t.Errorf("%s: log entry %+v err %v, want finished", k, e, err)
		}
		want := k
		if strings.Contains(k, " ") {
			want = "unknown"
		}
		if e.Kind != want {
			t.Errorf("%s: logged kind %q, want %q", k, e.Kind, want)
		}
	}
	for _, h := range all {
		if h.runs.Load() != 0 || h.resumes.Load() != 0 {
			t.Errorf("handler %s ran", h.kind)
		}
	}
	if len(r.ex.Capabilities()) != 0 {
		t.Errorf("row 6 advertises %v, want nothing", r.ex.Capabilities())
	}
}

// Row 6 registers no handler: the production list is empty, so nothing is
// advertised and no command can reach a pane.
func TestNoHandlerIsRegisteredInRow6(t *testing.T) {
	t.Parallel()
	if hs := fexec.Handlers(); len(hs) != 0 {
		t.Errorf("Handlers() = %d handlers, want none until the rows that build them", len(hs))
	}
}

// The executor never reaches tmux on its own (principle 4): neither it nor
// anything it imports, at any depth, is internal/agent/tmux or below. Only
// a handler, registered by a later row, may.
func TestExecDoesNotImportTmux(t *testing.T) {
	t.Parallel()
	out, err := exec.CommandContext(t.Context(), "go", "list", "-deps", ".").Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			t.Fatalf("go list: %v: %s", err, ee.Stderr)
		}
		t.Fatalf("go list: %v", err)
	}
	const tmux = "github.com/linuxdynasty/fleet-board/internal/agent/tmux"
	n := 0
	for _, p := range strings.Fields(string(out)) {
		n++
		if p == tmux || strings.HasPrefix(p, tmux+"/") {
			t.Errorf("exec depends on %s", p)
		}
	}
	if n < 10 {
		t.Fatalf("go list returned %d packages: the check read nothing", n)
	}
}
