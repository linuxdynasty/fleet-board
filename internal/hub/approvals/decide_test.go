package approvals_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/approvals"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
	"github.com/linuxdynasty/fleet-board/internal/store"
)

func localDecider() *typev1.Principal {
	return &typev1.Principal{
		Id:   &typev1.PrincipalId{Value: "local:alice"},
		Kind: typev1.PrincipalKind_PRINCIPAL_KIND_LOCAL_USER,
		Name: "alice",
	}
}

func actor() statedb.Actor {
	return statedb.Actor{ID: "local:alice", Kind: "LOCAL_USER", Name: "alice"}
}

// promptIn is a PROMPT_ANSWER approval for prompt "p1" on m1.
func promptIn() approvals.Create {
	in := confirmIn()
	in.Command = nil // prompts build their SendKeys at decide time
	in.Kind = hubv1.ApprovalKind_APPROVAL_KIND_PROMPT_ANSWER
	in.Preview = `allow "deploy"?`
	in.Allowed = []approvals.Choice{{Key: "1", Label: "yes"}, {Key: "2", Label: "no"}}
	in.DefaultChoice = "2"
	in.RequiredRole = typev1.Role_ROLE_OPERATOR
	in.PromptID = "p1"
	in.TriageItemID = "trg_1"
	return in
}

func mustCreate(t *testing.T, s *store.Store, in approvals.Create) string {
	t.Helper()
	var id string
	err := s.Write(context.Background(), func(tx *sql.Tx) error {
		a, err := approvals.CreateApproval(context.Background(), tx, in)
		if err != nil {
			return err
		}
		id = a.GetId().GetValue()
		return nil
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	return id
}

// auditActions returns the audit actions recorded for target, in order.
func auditActions(t *testing.T, s *store.Store, target string) []string {
	t.Helper()
	var out []string
	err := s.Read(context.Background(), func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(context.Background(),
			`SELECT action FROM audit_log WHERE target = ? ORDER BY rowid`, target)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var a string
			if err := rows.Scan(&a); err != nil {
				return err
			}
			out = append(out, a)
		}
		return rows.Err()
	})
	if err != nil {
		t.Fatalf("audit read: %v", err)
	}
	return out
}

func TestDecideApproveConfirm(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	ctx := context.Background()
	id := mustCreate(t, s, confirmIn())
	var out approvals.Outcome
	err := s.Write(ctx, func(tx *sql.Tx) error {
		var err error
		out, err = approvals.Decide(ctx, tx, approvals.DecideInput{
			ID: id, ExpectedVersion: 1, Decision: "approve",
			Decider: localDecider(), Actor: actor(), RequestID: "req-1", Now: t0,
		})
		return err
	})
	if err != nil {
		t.Fatalf("decide: %v", err)
	}
	if out.Replay {
		t.Fatal("first decide is a replay")
	}
	if !out.Sends {
		t.Fatal("approve sends, Sends is false")
	}
	if got := out.Approval; got.GetState() != hubv1.ApprovalState_APPROVAL_STATE_DECIDED || got.GetVersion() != 2 {
		t.Fatalf("state = %v version = %d, want DECIDED 2", got.GetState(), got.GetVersion())
	}
	if got := auditActions(t, s, "approvals/"+id); len(got) != 1 || got[0] != "approval.decide" {
		t.Fatalf("audit = %v, want one approval.decide", got)
	}
}

func TestDecideDenySendsNothing(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	ctx := context.Background()
	id := mustCreate(t, s, confirmIn())
	var out approvals.Outcome
	err := s.Write(ctx, func(tx *sql.Tx) error {
		var err error
		out, err = approvals.Decide(ctx, tx, approvals.DecideInput{
			ID: id, ExpectedVersion: 1, Decision: "deny",
			Decider: localDecider(), Actor: actor(), RequestID: "req-1", Now: t0,
		})
		return err
	})
	if err != nil {
		t.Fatalf("decide deny: %v", err)
	}
	if out.Sends {
		t.Fatal("deny sends, Sends is true")
	}
	if got := out.Approval.GetOperation().GetValue(); got != "" {
		t.Fatalf("deny operation = %q, want empty", got)
	}
}

func TestDecideClosedWinsOverVersion(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	ctx := context.Background()
	id := mustCreate(t, s, confirmIn())
	decide := func(version uint64, req string) error {
		return s.Write(ctx, func(tx *sql.Tx) error {
			_, err := approvals.Decide(ctx, tx, approvals.DecideInput{
				ID: id, ExpectedVersion: version, Decision: "approve",
				Decider: localDecider(), Actor: actor(), RequestID: req, Now: t0,
			})
			return err
		})
	}
	if err := decide(1, "req-1"); err != nil {
		t.Fatalf("first decide: %v", err)
	}
	// A second decision is closed even with a wrong version: the client
	// learns "already decided", not "reload".
	if err := decide(1, "req-2"); !errors.Is(err, approvals.ErrClosed) {
		t.Fatalf("second decide = %v, want ErrClosed", err)
	}
}

func TestDecideRefusals(t *testing.T) {
	t.Parallel()
	user := &typev1.Principal{
		Id:   &typev1.PrincipalId{Value: "u-bob"},
		Kind: typev1.PrincipalKind_PRINCIPAL_KIND_USER,
		Name: "bob",
	}
	token := &typev1.Principal{
		Id:   &typev1.PrincipalId{Value: "tok-1"},
		Kind: typev1.PrincipalKind_PRINCIPAL_KIND_API_TOKEN,
		Name: "tok-1",
	}
	cases := []struct {
		name   string
		mutate func(*approvals.DecideInput)
		want   error
		in     approvals.Create
	}{
		{"version mismatch", func(d *approvals.DecideInput) { d.ExpectedVersion = 9 }, approvals.ErrVersion, confirmIn()},
		{"unknown decision", func(d *approvals.DecideInput) { d.Decision = "maybe" }, approvals.ErrDecision, confirmIn()},
		{"missing request id", func(d *approvals.DecideInput) { d.RequestID = "" }, approvals.ErrInvalid, confirmIn()},
		{"user cannot cover OWNER", func(d *approvals.DecideInput) { d.Decider = user }, approvals.ErrRole, confirmIn()},
		{"token decides nothing", func(d *approvals.DecideInput) { d.Decider = token }, approvals.ErrRole, confirmIn()},
		{"user covers OPERATOR prompt", func(d *approvals.DecideInput) { d.Decider = user }, nil, promptIn()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := newStore(t)
			ctx := context.Background()
			id := mustCreate(t, s, tc.in)
			in := approvals.DecideInput{
				ID: id, ExpectedVersion: 1, Decision: "approve",
				Decider: localDecider(), Actor: actor(), RequestID: "req-1", Now: t0,
			}
			if tc.name == "user covers OPERATOR prompt" {
				in.Decision = "1"
			}
			tc.mutate(&in)
			err := s.Write(ctx, func(tx *sql.Tx) error {
				_, err := approvals.Decide(ctx, tx, in)
				return err
			})
			if tc.want == nil {
				if err != nil {
					t.Fatalf("decide = %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, tc.want) {
				t.Fatalf("decide = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestDecideReplay(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	ctx := context.Background()
	id := mustCreate(t, s, confirmIn())
	decide := func(req, decision string, version uint64) (approvals.Outcome, error) {
		var out approvals.Outcome
		err := s.Write(ctx, func(tx *sql.Tx) error {
			var err error
			out, err = approvals.Decide(ctx, tx, approvals.DecideInput{
				ID: id, ExpectedVersion: version, Decision: decision,
				Decider: localDecider(), Actor: actor(), RequestID: req, Now: t0,
			})
			return err
		})
		return out, err
	}
	first, err := decide("req-1", "approve", 1)
	if err != nil {
		t.Fatalf("first decide: %v", err)
	}
	if first.Replay {
		t.Fatal("first decide is a replay")
	}
	// Same key, same body: the stored decision comes back, version untouched.
	second, err := decide("req-1", "approve", 1)
	if err != nil {
		t.Fatalf("replay decide: %v", err)
	}
	if !second.Replay {
		t.Fatal("repeated request_id is not a replay")
	}
	if second.Approval.GetVersion() != 2 {
		t.Fatalf("replay version = %d, want 2", second.Approval.GetVersion())
	}
	// Same key, another body: conflict, so a retry never decides twice.
	if _, err := decide("req-1", "deny", 2); !errors.Is(err, approvals.ErrRequestConflict) {
		t.Fatalf("conflicting replay = %v, want ErrRequestConflict", err)
	}
}
