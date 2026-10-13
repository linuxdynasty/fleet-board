package approvals_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/approvals"
)

// TestPromptInflightRefusesASecondAnswer is the 03d W7 rule: one answer in
// flight per prompt, a second answer refused until the result lands.
func TestPromptInflightRefusesASecondAnswer(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	ctx := context.Background()
	id1 := mustCreate(t, s, promptIn())
	id2 := mustCreate(t, s, promptIn())
	decide := func(id, req string) error {
		return s.Write(ctx, func(tx *sql.Tx) error {
			_, err := approvals.Decide(ctx, tx, approvals.DecideInput{
				ID: id, ExpectedVersion: 1, Decision: "1",
				Decider: localDecider(), Actor: actor(), RequestID: req, Now: t0,
			})
			return err
		})
	}
	if err := decide(id1, "req-1"); err != nil {
		t.Fatalf("first answer: %v", err)
	}
	var holder string
	err := s.Read(ctx, func(tx *sql.Tx) error {
		var err error
		holder, err = approvals.InflightHolder(ctx, tx, "m1", "p1")
		return err
	})
	if err != nil {
		t.Fatalf("holder: %v", err)
	}
	if holder != id1 {
		t.Fatalf("holder = %q, want %q", holder, id1)
	}
	if err := decide(id2, "req-2"); !errors.Is(err, approvals.ErrInflight) {
		t.Fatalf("second answer = %v, want ErrInflight", err)
	}
	// The result lands: the hold clears and the prompt can be answered again.
	err = s.Write(ctx, func(tx *sql.Tx) error {
		return approvals.ClearInflight(ctx, tx, "m1", "p1")
	})
	if err != nil {
		t.Fatalf("clear: %v", err)
	}
	if err := decide(id2, "req-2"); err != nil {
		t.Fatalf("answer after result: %v", err)
	}
}

// TestDenyTakesNoInflight: a deny sends nothing, so it holds no row.
func TestDenyTakesNoInflight(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	ctx := context.Background()
	in := promptIn()
	in.Allowed = append(in.Allowed, approvals.Choice{Key: "deny", Label: "leave it"})
	id := mustCreate(t, s, in)
	err := s.Write(ctx, func(tx *sql.Tx) error {
		_, err := approvals.Decide(ctx, tx, approvals.DecideInput{
			ID: id, ExpectedVersion: 1, Decision: "deny",
			Decider: localDecider(), Actor: actor(), RequestID: "req-1", Now: t0,
		})
		return err
	})
	if err != nil {
		t.Fatalf("deny: %v", err)
	}
	var holder string
	err = s.Read(ctx, func(tx *sql.Tx) error {
		var err error
		holder, err = approvals.InflightHolder(ctx, tx, "m1", "p1")
		return err
	})
	if err != nil {
		t.Fatalf("holder: %v", err)
	}
	if holder != "" {
		t.Fatalf("deny holds the prompt: %q", holder)
	}
}

// TestExpireDueDeniesOnTimeout is the acceptance rule: a CONFIRM waits, and
// past approval_timeout the DENY fallback applies and nothing is sent.
func TestExpireDueDeniesOnTimeout(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	ctx := context.Background()
	id := mustCreate(t, s, confirmIn())
	late := t0.Add(31 * time.Minute) // past the 30-minute approval_timeout
	var expired []string
	err := s.Write(ctx, func(tx *sql.Tx) error {
		var err error
		expired, err = approvals.ExpireDue(ctx, tx, late, 100)
		return err
	})
	if err != nil {
		t.Fatalf("expire: %v", err)
	}
	if len(expired) != 1 || expired[0] != id {
		t.Fatalf("expired = %v, want [%s]", expired, id)
	}
	var got *hubv1.Approval
	err = s.Read(ctx, func(tx *sql.Tx) error {
		var err error
		got, err = approvals.Get(ctx, tx, id, late)
		return err
	})
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.GetState() != hubv1.ApprovalState_APPROVAL_STATE_EXPIRED {
		t.Fatalf("state = %v, want EXPIRED", got.GetState())
	}
	if got := auditActions(t, s, "approvals/"+id); len(got) != 1 || got[0] != "approval.expire" {
		t.Fatalf("audit = %v, want one approval.expire", got)
	}
	// Deciding after expiry fails closed: the fallback already denied it.
	err = s.Write(ctx, func(tx *sql.Tx) error {
		_, err := approvals.Decide(ctx, tx, approvals.DecideInput{
			ID: id, ExpectedVersion: 2, Decision: "approve",
			Decider: localDecider(), Actor: actor(), RequestID: "req-2", Now: late,
		})
		return err
	})
	if !errors.Is(err, approvals.ErrClosed) {
		t.Fatalf("decide after expiry = %v, want ErrClosed", err)
	}
}

// TestGetExpiresLazily: a client polling the approval sees EXPIRED without
// waiting for the housekeeping pass.
func TestGetExpiresLazily(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	ctx := context.Background()
	id := mustCreate(t, s, confirmIn())
	late := t0.Add(2 * time.Hour)
	var got *hubv1.Approval
	err := s.Write(ctx, func(tx *sql.Tx) error {
		var err error
		got, err = approvals.Get(ctx, tx, id, late)
		return err
	})
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.GetState() != hubv1.ApprovalState_APPROVAL_STATE_EXPIRED {
		t.Fatalf("state = %v, want EXPIRED", got.GetState())
	}
}

// TestWithdrawRetiresAGonePrompt: answering a prompt that left the screen
// fails, and nothing is typed.
func TestWithdrawRetiresAGonePrompt(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	ctx := context.Background()
	id := mustCreate(t, s, promptIn())
	var withdrawn *hubv1.Approval
	err := s.Write(ctx, func(tx *sql.Tx) error {
		var err error
		withdrawn, err = approvals.Withdraw(ctx, tx, id, actor(), t0)
		return err
	})
	if err != nil {
		t.Fatalf("withdraw: %v", err)
	}
	if withdrawn.GetState() != hubv1.ApprovalState_APPROVAL_STATE_WITHDRAWN {
		t.Fatalf("state = %v, want WITHDRAWN", withdrawn.GetState())
	}
	if got := auditActions(t, s, "approvals/"+id); len(got) != 1 || got[0] != "approval.withdraw" {
		t.Fatalf("audit = %v, want one approval.withdraw", got)
	}
	// A late answer is refused: the prompt is gone, nothing is typed.
	err = s.Write(ctx, func(tx *sql.Tx) error {
		_, err := approvals.Decide(ctx, tx, approvals.DecideInput{
			ID: id, ExpectedVersion: 2, Decision: "1",
			Decider: localDecider(), Actor: actor(), RequestID: "req-1", Now: t0,
		})
		return err
	})
	if !errors.Is(err, approvals.ErrClosed) {
		t.Fatalf("decide after withdraw = %v, want ErrClosed", err)
	}
	var holder string
	err = s.Read(ctx, func(tx *sql.Tx) error {
		var err error
		holder, err = approvals.InflightHolder(ctx, tx, "m1", "p1")
		return err
	})
	if err != nil {
		t.Fatalf("holder: %v", err)
	}
	if holder != "" {
		t.Fatalf("withdrawn approval holds the prompt: %q", holder)
	}
}

// TestWithdrawRefusals: only a pending prompt approval withdraws.
func TestWithdrawRefusals(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	ctx := context.Background()
	confirmID := mustCreate(t, s, confirmIn())
	if err := s.Write(ctx, func(tx *sql.Tx) error {
		_, err := approvals.Withdraw(ctx, tx, confirmID, actor(), t0)
		return err
	}); !errors.Is(err, approvals.ErrInvalid) {
		t.Fatalf("withdraw confirm = %v, want ErrInvalid", err)
	}
	if err := s.Write(ctx, func(tx *sql.Tx) error {
		_, err := approvals.Withdraw(ctx, tx, "appr_missing", actor(), t0)
		return err
	}); !errors.Is(err, approvals.ErrUnknown) {
		t.Fatalf("withdraw missing = %v, want ErrUnknown", err)
	}
}

// TestAttachOperation records the command a decision sent.
func TestAttachOperation(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	ctx := context.Background()
	id := mustCreate(t, s, confirmIn())
	err := s.Write(ctx, func(tx *sql.Tx) error {
		if _, err := approvals.Decide(ctx, tx, approvals.DecideInput{
			ID: id, ExpectedVersion: 1, Decision: "approve",
			Decider: localDecider(), Actor: actor(), RequestID: "req-1", Now: t0,
		}); err != nil {
			return err
		}
		return approvals.AttachOperation(ctx, tx, id, "cmd_1", t0)
	})
	if err != nil {
		t.Fatalf("decide+attach: %v", err)
	}
	var got *hubv1.Approval
	err = s.Read(ctx, func(tx *sql.Tx) error {
		var err error
		got, err = approvals.Get(ctx, tx, id, t0)
		return err
	})
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.GetOperation().GetValue() != "cmd_1" {
		t.Fatalf("operation = %q, want cmd_1", got.GetOperation().GetValue())
	}
	pending := mustCreate(t, s, confirmIn())
	if err := s.Write(ctx, func(tx *sql.Tx) error {
		return approvals.AttachOperation(ctx, tx, pending, "cmd_2", t0)
	}); !errors.Is(err, approvals.ErrClosed) {
		t.Fatalf("attach to pending = %v, want ErrClosed", err)
	}
}
