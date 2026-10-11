package exec_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/audit"
	"github.com/linuxdynasty/fleet-board/internal/agent/ring"
)

// corpusOut names a file the refusal test writes every result message
// into, one JSON string per line, as internal/scrub's BenchmarkHubPassFile
// reads it (ruling fork 2). Unset, nothing is written.
const corpusOut = "FLEET_EXEC_CORPUS_OUT"

var corpus struct {
	mu   sync.Mutex
	msgs []string
}

// TestMain writes the corpus once every test has run.
func TestMain(m *testing.M) {
	code := m.Run()
	if path := os.Getenv(corpusOut); path != "" && code == 0 {
		if err := writeCorpus(path); err != nil {
			fmt.Fprintln(os.Stderr, "corpus:", err)
			code = 1
		}
	}
	os.Exit(code)
}

func writeCorpus(path string) error {
	f, err := os.Create(path) //nolint:gosec // G304: a developer-chosen output file, test only
	if err != nil {
		return err
	}
	enc := json.NewEncoder(f)
	corpus.mu.Lock()
	defer corpus.mu.Unlock()
	msgs := slices.Clone(corpus.msgs)
	slices.Sort(msgs)
	for _, m := range msgs {
		if err := enc.Encode(m); err != nil {
			return errors.Join(err, f.Close())
		}
	}
	return f.Close()
}

func keep(msg string) {
	if msg == "" {
		return
	}
	corpus.mu.Lock()
	defer corpus.mu.Unlock()
	corpus.msgs = append(corpus.msgs, msg)
}

// One test per refusal path the executor can produce (ruling fork 2): the
// status, the reason, whether it is retryable, whether it was logged and
// acked (every refusal but EPOCH_MISMATCH and SEQ_REPLAYED; fork 4 i), and
// the command.refused audit row for every REJECTED_BY_POLICY (02g).
func TestEveryRefusalPath(t *testing.T) {
	t.Parallel()
	type want struct {
		status    typev1.Status
		reason    typev1.ReasonCode
		retryable bool
		logged    bool
		audited   bool
	}
	cases := []struct {
		name  string
		setup func(r *rig, c *agentv1.Command) // before the command is given
		want  want
	}{
		{"expired", func(r *rig, c *agentv1.Command) {
			c.NotAfterTime = timestamppb.New(hub0.Add(-time.Second))
		}, want{typev1.Status_STATUS_EXPIRED, typev1.ReasonCode_REASON_CODE_EXPIRED, false, true, false}},
		{"hub time unknown", func(r *rig, _ *agentv1.Command) {
			r.clock.bad.Store(true)
		}, want{typev1.Status_STATUS_EXPIRED, typev1.ReasonCode_REASON_CODE_EXPIRED, false, true, false}},
		{"epoch mismatch", func(_ *rig, c *agentv1.Command) {
			c.Epoch = 2
		}, want{typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_EPOCH_MISMATCH, true, false, false}},
		{"seq replayed", func(r *rig, c *agentv1.Command) {
			if err := r.db.ApplyCommandSeq(context.Background(), 1, c.GetSeq()); err != nil {
				panic(err)
			}
		}, want{typev1.Status_STATUS_REJECTED_BY_POLICY, typev1.ReasonCode_REASON_CODE_SEQ_REPLAYED, false, false, true}},
		{"unsupported", func(_ *rig, c *agentv1.Command) {
			c.Requires.Name = "tmux.default_server.control"
		}, want{typev1.Status_STATUS_UNSUPPORTED, typev1.ReasonCode_REASON_CODE_UNSPECIFIED, false, true, false}},
		{"safe mode", func(r *rig, c *agentv1.Command) {
			r.pol.safe.Store(true)
			c.Kind = &agentv1.Command_Interrupt{Interrupt: &agentv1.Interrupt{}}
		}, want{typev1.Status_STATUS_REJECTED_BY_POLICY, typev1.ReasonCode_REASON_CODE_POLICY_SAFE_MODE, false, true, true}},
		{"role", func(_ *rig, c *agentv1.Command) {
			c.RequestedBy.Role = typev1.Role_ROLE_VIEWER
			c.Kind = &agentv1.Command_Interrupt{Interrupt: &agentv1.Interrupt{}}
		}, want{typev1.Status_STATUS_REJECTED_BY_POLICY, typev1.ReasonCode_REASON_CODE_ROLE_INSUFFICIENT, false, true, true}},
		{"no local confirm", func(r *rig, c *agentv1.Command) {
			r.pol.approval["Interrupt"] = true
			c.Kind = &agentv1.Command_Interrupt{Interrupt: &agentv1.Interrupt{}}
			c.NotAfterTime = timestamppb.New(hub0.Add(300 * time.Millisecond))
		}, want{typev1.Status_STATUS_REJECTED_BY_POLICY, typev1.ReasonCode_REASON_CODE_LOCAL_CONFIRM_TIMEOUT, false, true, true}},
		{"denied at the machine", func(r *rig, c *agentv1.Command) {
			r.pol.approval["Interrupt"] = true
			c.Kind = &agentv1.Command_Interrupt{Interrupt: &agentv1.Interrupt{}}
		}, want{typev1.Status_STATUS_REJECTED_BY_POLICY, typev1.ReasonCode_REASON_CODE_LOCAL_DENIED, false, true, true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			hs := []*handler{capturePane("tmux.control"), {kind: "Interrupt", caps: []*typev1.Capability{{Name: "tmux.control", Version: 1}}}}
			r := newRig(t, hs[0], hs[1])
			if tc.name == "denied at the machine" {
				r.withApprover(t, &approver{decide: false}, hs[0], hs[1])
			}
			r.start(t)
			c := cmd("c-1", 5, "tmux.control")
			tc.setup(r, c)
			r.give(t, c)
			res := r.result(t, "c-1")
			keep(res.GetMessage())
			w := tc.want
			if res.GetStatus() != w.status || res.GetReason() != w.reason || res.GetRetryable() != w.retryable {
				t.Errorf("result %v, want status %v reason %v retryable %v", res, w.status, w.reason, w.retryable)
			}
			if res.GetMessage() == "" {
				t.Error("a refusal has no message")
			}
			_, err := r.db.Command(context.Background(), "c-1")
			if logged := err == nil; logged != w.logged {
				t.Errorf("logged %v (err %v), want %v", logged, err, w.logged)
			}
			if acks, _, _ := r.count("c-1"); (acks == 1) != w.logged {
				t.Errorf("%d acks, want an ack exactly when it is logged (%v)", acks, w.logged)
			}
			if !w.logged && !errors.Is(err, ring.ErrUnknownCommand) {
				t.Errorf("an unlogged refusal: %v", err)
			}
			rows := r.audit.all()
			refused := slices.IndexFunc(rows, func(e audit.Entry) bool { return e.Action == audit.CommandRefused })
			if (refused >= 0) != w.audited {
				t.Errorf("audit rows %+v, want a command.refused row: %v", rows, w.audited)
			}
			if refused >= 0 {
				got := rows[refused]
				if got.CommandID != "c-1" || got.Reason == "" || got.RequestedBy == nil ||
					got.RequestedBy.Kind != "device" || got.RequestedBy.ID != "device:phone-1" {
					t.Errorf("audit row %+v, want command c-1, its reason and the device requester", got)
				}
			}
			for _, h := range hs {
				if h.runs.Load() != 0 {
					t.Errorf("handler %s ran for a refused command", h.kind)
				}
			}
		})
	}
}

// RATE_LIMITED: every logged command_id of the kind in the last minute
// counts, refusals included; the refusal is retryable with retry_after the
// time until the window frees (ruling fork 4 i). The local limit tightens
// the default 60, never loosens it.
func TestRateLimitCountsLoggedCommandsOfTheKind(t *testing.T) {
	t.Parallel()
	r := newRig(t, capturePane("tmux.observe"))
	r.pol.limits["CapturePane"] = 2
	r.start(t)
	for i, id := range []string{"r-1", "r-2", "r-3"} {
		r.give(t, cmd(id, uint64(i+1), "tmux.observe"))
		r.result(t, id)
	}
	res := r.result(t, "r-3")
	keep(res.GetMessage())
	if res.GetStatus() != typev1.Status_STATUS_REJECTED_BY_POLICY || res.GetReason() != typev1.ReasonCode_REASON_CODE_RATE_LIMITED ||
		!res.GetRetryable() {
		t.Fatalf("third command: %v, want retryable RATE_LIMITED", res)
	}
	if d := res.GetRetryAfter().AsDuration(); d <= 50*time.Second || d > time.Minute {
		t.Errorf("retry_after %v, want just under a minute", d)
	}
	if acks, _, _ := r.count("r-3"); acks != 1 {
		t.Errorf("the rate-limited command was acked %d times, want once (it is logged)", acks)
	}
	for _, id := range []string{"r-1", "r-2"} {
		if s := r.result(t, id).GetStatus(); s != typev1.Status_STATUS_OK {
			t.Errorf("%s: %v, want OK", id, s)
		}
	}
	if got := r.metric(t, "fleet_agent_commands_refused_total", `reason="RATE_LIMITED"`); got != "1" {
		t.Errorf("refused counter %q, want 1", got)
	}
}

// approver is a fake person at the machine.
type approver struct{ decide bool }

func (a *approver) Await(context.Context, string, string) (bool, string, error) {
	return a.decide, "cli", nil
}

func (r *rig) withApprover(t *testing.T, a *approver, hs ...*handler) {
	t.Helper()
	r.buildWith(t, a, hs)
}
