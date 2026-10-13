package outbox_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/approvals"
	"github.com/linuxdynasty/fleet-board/internal/hub/outbox"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
	"github.com/linuxdynasty/fleet-board/internal/store"
)

// answerCmd is a SendKeys bound to prompt p1, due an hour after t0.
func answerCmd(id string) *agentv1.Command {
	return &agentv1.Command{
		CommandId:    &typev1.CommandId{Value: id},
		NotAfterTime: timestamppb.New(t0.Add(time.Hour)),
		IssuedTime:   timestamppb.New(t0),
		RequestedBy:  &typev1.Principal{Name: "alice", Id: &typev1.PrincipalId{Value: "local:alice"}},
		Kind: &agentv1.Command_SendKeys{SendKeys: &agentv1.SendKeys{
			Pane:            &typev1.PaneKey{Server: "fleet", ServerStartId: "s1", PaneId: "%1"},
			Keys:            []*typev1.KeyInput{{Input: &typev1.KeyInput_Literal{Literal: "1"}}},
			AnsweringPrompt: &typev1.PromptId{Value: "p1"},
			Purpose:         typev1.SendKeysPurpose_SEND_KEYS_PURPOSE_PROMPT_ANSWER,
		}},
	}
}

// holdAnswer stores a prompt approval and decides it, holding the in-flight
// answer the way DecideApproval does.
func holdAnswer(t *testing.T, s *store.Store) {
	t.Helper()
	ctx := context.Background()
	err := s.Write(ctx, func(tx *sql.Tx) error {
		a, err := approvals.CreateApproval(ctx, tx, approvals.Create{
			Kind:      hubv1.ApprovalKind_APPROVAL_KIND_PROMPT_ANSWER,
			Severity:  hubv1.Severity_SEVERITY_WARNING,
			Preview:   `allow "deploy"?`,
			MachineID: "m1", PromptID: "p1",
			Allowed:      []approvals.Choice{{Key: "1", Label: "yes"}},
			Fallback:     hubv1.ApprovalFallback_APPROVAL_FALLBACK_DENY,
			ExpireTime:   t0.Add(30 * time.Minute),
			RequiredRole: typev1.Role_ROLE_OPERATOR,
			Now:          t0,
		})
		if err != nil {
			return err
		}
		_, err = approvals.Decide(ctx, tx, approvals.DecideInput{
			ID: a.GetId().GetValue(), ExpectedVersion: 1, Decision: "1",
			Decider: &typev1.Principal{
				Id:   &typev1.PrincipalId{Value: "local:alice"},
				Kind: typev1.PrincipalKind_PRINCIPAL_KIND_LOCAL_USER,
				Name: "alice",
			},
			Actor:     statedb.Actor{ID: "local:alice", Kind: "LOCAL_USER", Name: "alice"},
			RequestID: "req-1",
			Now:       t0,
		})
		return err
	})
	if err != nil {
		t.Fatalf("hold answer: %v", err)
	}
}

func holder(t *testing.T, s *store.Store) string {
	t.Helper()
	var held string
	err := s.Read(context.Background(), func(tx *sql.Tx) error {
		var err error
		held, err = approvals.InflightHolder(context.Background(), tx, "m1", "p1")
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return held
}

// TestResultReleasesThePrompt: the answer's CommandResult clears the
// in-flight row, so the next answer for that prompt can go.
func TestResultReleasesThePrompt(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	h := newService(t, s, newFakeStreams(), fixedClock(t0))
	holdAnswer(t, s)
	if holder(t, s) == "" {
		t.Fatal("no answer held")
	}
	if _, err := enqueue(t, s, outbox.In{MachineID: "m1", Command: answerCmd("a1"), RequestID: "req-a1", Now: t0}); err != nil {
		t.Fatalf("enqueue answer: %v", err)
	}
	if _, err := h.svc.Result(context.Background(), live1, resultOf("a1", typev1.Status_STATUS_OK, "typed")); err != nil {
		t.Fatalf("Result: %v", err)
	}
	if held := holder(t, s); held != "" {
		t.Fatalf("prompt still held by %q after its result", held)
	}
}

// TestExpiryReleasesThePrompt: an answer that never lands stops blocking
// the prompt once the outbox expires it.
func TestExpiryReleasesThePrompt(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	_ = newService(t, s, newFakeStreams(), fixedClock(t0))
	holdAnswer(t, s)
	if _, err := enqueue(t, s, outbox.In{MachineID: "m1", Command: answerCmd("a1"), RequestID: "req-a1", Now: t0}); err != nil {
		t.Fatalf("enqueue answer: %v", err)
	}
	err := s.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := outbox.Expire(context.Background(), tx, "m1", t0.Add(2*time.Hour), 100)
		return err
	})
	if err != nil {
		t.Fatalf("Expire: %v", err)
	}
	if held := holder(t, s); held != "" {
		t.Fatalf("prompt still held by %q after expiry", held)
	}
}
