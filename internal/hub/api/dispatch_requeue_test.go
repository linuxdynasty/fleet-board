package api_test

import (
	"context"
	"database/sql"
	"testing"

	"connectrpc.com/connect"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
)

// setWorkItemStage parks a mirror row in the given stage and manual
// reason, like a cleared or exhausted goal left it.
func setWorkItemStage(t *testing.T, e *fleetEnv, key string, stage hubv1.WorkStage, reason hubv1.WorkManualReason) {
	t.Helper()
	err := e.st.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.ExecContext(context.Background(),
			`UPDATE work_items SET stage = ?, manual_reason = ? WHERE key = ?`,
			int32(stage), int32(reason), key)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

// parkManual parks the seeded item in MANUAL_ONLY with the given reason
// and returns its version, like a cleared or exhausted goal left it.
func parkManual(t *testing.T, e *fleetEnv, reason hubv1.WorkManualReason) uint64 {
	t.Helper()
	setWorkItemStage(t, e, "github-1/ISSUE/acme/web/7398",
		hubv1.WorkStage_WORK_STAGE_MANUAL_ONLY, reason)
	got, err := e.client.GetWorkItem(context.Background(), connect.NewRequest(&hubv1.GetWorkItemRequest{
		Id: &typev1.WorkItemKey{
			Integration: &typev1.IntegrationId{Value: "github-1"},
			Kind:        typev1.WorkItemKind_WORK_ITEM_KIND_ISSUE,
			Project:     "acme/web", Key: "7398",
		},
	}))
	if err != nil {
		t.Fatalf("GetWorkItem: %v", err)
	}
	return got.Msg.GetWorkItem().GetVersion()
}

func requeue(t *testing.T, e *fleetEnv, version uint64) (*connect.Response[hubv1.RequeueWorkItemResponse], error) {
	t.Helper()
	return e.client.RequeueWorkItem(context.Background(), connect.NewRequest(&hubv1.RequeueWorkItemRequest{
		RequestId: "req-requeue-1",
		Id: &typev1.WorkItemKey{
			Integration: &typev1.IntegrationId{Value: "github-1"},
			Kind:        typev1.WorkItemKind_WORK_ITEM_KIND_ISSUE,
			Project:     "acme/web", Key: "7398",
		},
		ExpectedVersion: version,
	}))
}

// TestRequeueWorkItemPutsClearedItemBackToReady is the 03h requeue: a
// MANUAL_ONLY item returns to READY, cleared of its manual reason, with
// an audit row, so the next scheduler pass may take it again.
func TestRequeueWorkItemPutsClearedItemBackToReady(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"))
	autoSeed(t, e)
	version := parkManual(t, e, hubv1.WorkManualReason_WORK_MANUAL_REASON_GOAL_CLEARED)

	res, err := requeue(t, e, version)
	if err != nil {
		t.Fatalf("RequeueWorkItem: %v", err)
	}
	if got := res.Msg.GetWorkItem().GetStage(); got != hubv1.WorkStage_WORK_STAGE_READY {
		t.Errorf("stage = %v, want READY", got)
	}
	if got := res.Msg.GetWorkItem().GetManualReason(); got != hubv1.WorkManualReason_WORK_MANUAL_REASON_UNSPECIFIED {
		t.Errorf("manual reason = %v, want cleared", got)
	}
	if got := auditActionsFor(t, e, "work_items/github-1/ISSUE/acme/web/7398"); len(got) == 0 {
		t.Error("no audit for the work item, want work_item.requeue")
	}
}

// TestRequeueWorkItemRefusesUntrustedGate proves the gate holds: a click
// never bypasses an UNTRUSTED or EDITED_AFTER_TRUST item.
func TestRequeueWorkItemRefusesUntrustedGate(t *testing.T) {
	t.Parallel()
	for _, reason := range []hubv1.WorkManualReason{
		hubv1.WorkManualReason_WORK_MANUAL_REASON_UNTRUSTED,
		hubv1.WorkManualReason_WORK_MANUAL_REASON_EDITED_AFTER_TRUST,
	} {
		t.Run(reason.String(), func(t *testing.T) {
			t.Parallel()
			e := newFleetEnv(t, localUser("alice"))
			autoSeed(t, e)
			version := parkManual(t, e, reason)
			_, err := requeue(t, e, version)
			if code, why, _ := detail(t, err); code != connect.CodeFailedPrecondition ||
				why != typev1.ReasonCode_REASON_CODE_REQUEUE_NOT_ALLOWED {
				t.Errorf("requeue: %v %v, want FAILED_PRECONDITION REQUEUE_NOT_ALLOWED", code, why)
			}
		})
	}
}

// TestRequeueWorkItemRefusesNonManual proves only MANUAL_ONLY items
// requeue: a READY item is already where the fleet would take it.
func TestRequeueWorkItemRefusesNonManual(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"))
	autoSeed(t, e)
	got, err := e.client.GetWorkItem(context.Background(), connect.NewRequest(&hubv1.GetWorkItemRequest{
		Id: &typev1.WorkItemKey{
			Integration: &typev1.IntegrationId{Value: "github-1"},
			Kind:        typev1.WorkItemKind_WORK_ITEM_KIND_ISSUE,
			Project:     "acme/web", Key: "7398",
		},
	}))
	if err != nil {
		t.Fatalf("GetWorkItem: %v", err)
	}
	_, err = requeue(t, e, got.Msg.GetWorkItem().GetVersion())
	if code, why, _ := detail(t, err); code != connect.CodeFailedPrecondition ||
		why != typev1.ReasonCode_REASON_CODE_REQUEUE_NOT_ALLOWED {
		t.Errorf("requeue READY: %v %v, want FAILED_PRECONDITION REQUEUE_NOT_ALLOWED", code, why)
	}
}

// TestRequeueWorkItemRefusesStaleVersion proves the version check: a
// requeue read before another writer's change is refused, so the
// operator reads again.
func TestRequeueWorkItemRefusesStaleVersion(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"))
	autoSeed(t, e)
	version := parkManual(t, e, hubv1.WorkManualReason_WORK_MANUAL_REASON_GOAL_CLEARED)
	_, err := requeue(t, e, version+1)
	if code, why, _ := detail(t, err); code != connect.CodeFailedPrecondition ||
		why != typev1.ReasonCode_REASON_CODE_VERSION_MISMATCH {
		t.Errorf("requeue stale: %v %v, want FAILED_PRECONDITION VERSION_MISMATCH", code, why)
	}
}

// TestRequeueWorkItemReplaysSameRequestId is 02b idempotency: retrying
// a committed requeue with the same request_id returns the item instead
// of refusing the now-READY item.
func TestRequeueWorkItemReplaysSameRequestId(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"))
	autoSeed(t, e)
	version := parkManual(t, e, hubv1.WorkManualReason_WORK_MANUAL_REASON_GOAL_CLEARED)

	first, err := requeue(t, e, version)
	if err != nil {
		t.Fatalf("first RequeueWorkItem: %v", err)
	}
	second, err := requeue(t, e, version)
	if err != nil {
		t.Fatalf("requeue retry: %v, want the stored item", err)
	}
	if got := second.Msg.GetWorkItem().GetStage(); got != hubv1.WorkStage_WORK_STAGE_READY {
		t.Errorf("retry stage = %v, want READY", got)
	}
	if first.Msg.GetWorkItem().GetVersion() != second.Msg.GetWorkItem().GetVersion() {
		t.Errorf("retry version = %v, want the stored %v",
			second.Msg.GetWorkItem().GetVersion(), first.Msg.GetWorkItem().GetVersion())
	}
	if got := auditActionsFor(t, e, "work_items/github-1/ISSUE/acme/web/7398"); len(got) != 1 {
		t.Errorf("audits = %v, want one work_item.requeue: a replay audits nothing new", got)
	}
}

// TestRequeueWorkItemConflictsOnDifferentBody proves the same
// request_id with a different body is REQUEST_ID_CONFLICT, not a
// second mutation.
func TestRequeueWorkItemConflictsOnDifferentBody(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"))
	autoSeed(t, e)
	version := parkManual(t, e, hubv1.WorkManualReason_WORK_MANUAL_REASON_GOAL_CLEARED)

	if _, err := requeue(t, e, version); err != nil {
		t.Fatalf("first RequeueWorkItem: %v", err)
	}
	_, err := e.client.RequeueWorkItem(context.Background(), connect.NewRequest(&hubv1.RequeueWorkItemRequest{
		RequestId: "req-requeue-1",
		Id: &typev1.WorkItemKey{
			Integration: &typev1.IntegrationId{Value: "github-1"},
			Kind:        typev1.WorkItemKind_WORK_ITEM_KIND_ISSUE,
			Project:     "acme/web", Key: "7398",
		},
		ExpectedVersion: version + 99,
	}))
	if code, why, _ := detail(t, err); code != connect.CodeAlreadyExists ||
		why != typev1.ReasonCode_REASON_CODE_REQUEST_ID_CONFLICT {
		t.Errorf("requeue changed body: %v %v, want ALREADY_EXISTS REQUEST_ID_CONFLICT", code, why)
	}
}

// TestRequeueWorkItemDenied proves the SESSIONS_WRITE scope: no caller,
// no requeue.
func TestRequeueWorkItemDenied(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"))
	autoSeed(t, e)
	version := parkManual(t, e, hubv1.WorkManualReason_WORK_MANUAL_REASON_GOAL_CLEARED)

	anon := newFleetEnv(t, nil)
	_, err := anon.client.RequeueWorkItem(context.Background(), connect.NewRequest(&hubv1.RequeueWorkItemRequest{
		RequestId: "req-requeue-1",
		Id: &typev1.WorkItemKey{
			Integration: &typev1.IntegrationId{Value: "github-1"},
			Kind:        typev1.WorkItemKind_WORK_ITEM_KIND_ISSUE,
			Project:     "acme/web", Key: "7398",
		},
		ExpectedVersion: version,
	}))
	if code, _, _ := detail(t, err); code != connect.CodeUnauthenticated {
		t.Errorf("requeue anon: %v, want UNAUTHENTICATED", code)
	}
}
