package api_test

import (
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

// seedTriageItem inserts one OPEN hub-state triage item through the store:
// the raise path of plan row 5 will fill the source columns; operator
// writes only move state.
func seedTriageItem(t *testing.T, e *fleetEnv, id string) {
	t.Helper()
	if _, err := statedb.InsertTriageItem(t.Context(), e.st, statedb.TriageSeed{
		ID: id, Kind: "CONDITION", Severity: "WARNING",
		Title: "agent reconnecting", Fingerprint: "fp-" + id,
		DetailKind: "note", DetailJSON: `{"text":"retrying"}`,
		Now: fleetNow,
	}); err != nil {
		t.Fatalf("InsertTriageItem: %v", err)
	}
}

func snoozeCall(t *testing.T, e *fleetEnv, secret, reqID, id string, version uint64, until *timestamppb.Timestamp) *hubv1.TriageItem {
	t.Helper()
	res, err := e.client.SnoozeTriageItem(t.Context(), withSecret(connect.NewRequest(&hubv1.SnoozeTriageItemRequest{
		RequestId: reqID, Id: &hubv1.TriageItemId{Value: id},
		ExpectedVersion: version, UntilTime: until,
	}), secret))
	if err != nil {
		t.Fatalf("SnoozeTriageItem: %v", err)
	}
	return res.Msg.GetTriageItem()
}

// TestSnoozeTriageItemSnoozesAnOpenItem is the operator happy path: an
// OPEN item becomes SNOOZED at version 2 with its wake time, and the same
// request_id replays it.
func TestSnoozeTriageItemSnoozesAnOpenItem(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"), trusted(false))
	seedTriageItem(t, e, "ti-1")
	until := timestamppb.New(fleetNow.Add(2 * time.Hour))
	got := snoozeCall(t, e, "", "sn-1", "ti-1", 1, until)
	if got.GetState() != hubv1.TriageState_TRIAGE_STATE_SNOOZED || got.GetVersion() != 2 {
		t.Errorf("snoozed = state %v version %d, want SNOOZED 2", got.GetState(), got.GetVersion())
	}
	if got.GetSnoozedUntilTime().GetSeconds() != until.GetSeconds() {
		t.Errorf("snoozed_until = %v, want %v", got.GetSnoozedUntilTime(), until)
	}
	replayed := snoozeCall(t, e, "", "sn-1", "ti-1", 1, until)
	if replayed.GetVersion() != 2 || replayed.GetState() != hubv1.TriageState_TRIAGE_STATE_SNOOZED {
		t.Errorf("replay = state %v version %d, want the stored SNOOZED 2",
			replayed.GetState(), replayed.GetVersion())
	}
}

// TestDismissTriageItemClosesAnItem proves a dismiss closes the item,
// records the operator, and refuses a second dismiss as closed.
func TestDismissTriageItemClosesAnItem(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"), trusted(false))
	seedTriageItem(t, e, "ti-9")
	res, err := e.client.DismissTriageItem(t.Context(), withSecret(connect.NewRequest(&hubv1.DismissTriageItemRequest{
		RequestId: "di-1", Id: &hubv1.TriageItemId{Value: "ti-9"},
		ExpectedVersion: 1, Note: "handled at the machine",
	}), ""))
	if err != nil {
		t.Fatalf("DismissTriageItem: %v", err)
	}
	got := res.Msg.GetTriageItem()
	if got.GetState() != hubv1.TriageState_TRIAGE_STATE_DISMISSED || got.GetVersion() != 2 {
		t.Errorf("dismissed = state %v version %d, want DISMISSED 2", got.GetState(), got.GetVersion())
	}
	if got.GetActedBy().GetId().GetValue() != "local:alice" {
		t.Errorf("acted_by = %v, want local:alice", got.GetActedBy())
	}
	_, err = e.client.DismissTriageItem(t.Context(), withSecret(connect.NewRequest(&hubv1.DismissTriageItemRequest{
		RequestId: "di-2", Id: &hubv1.TriageItemId{Value: "ti-9"}, ExpectedVersion: 2,
	}), ""))
	code, reason, _ := detail(t, err)
	if code != connect.CodeFailedPrecondition || reason != typev1.ReasonCode_REASON_CODE_TRIAGE_CLOSED {
		t.Errorf("second dismiss = %v/%v, want FAILED_PRECONDITION/TRIAGE_CLOSED", code, reason)
	}
}

// TestTriageWriteRefusals is the contract table both writes share: the
// caller, the fields, the unknown id, the stale version and the closed
// item, in 02b precedence order.
func TestTriageWriteRefusals(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"), trusted(false))
	seedTriageItem(t, e, "ti-2")
	until := timestamppb.New(fleetNow.Add(time.Hour))
	snooze := func(reqID, id string, version uint64, until *timestamppb.Timestamp) error {
		_, err := e.client.SnoozeTriageItem(t.Context(), withSecret(connect.NewRequest(&hubv1.SnoozeTriageItemRequest{
			RequestId: reqID, Id: &hubv1.TriageItemId{Value: id},
			ExpectedVersion: version, UntilTime: until,
		}), ""))
		return err
	}
	cases := []struct {
		name   string
		err    error
		code   connect.Code
		reason typev1.ReasonCode
	}{
		{
			"missing request id", snooze("", "ti-2", 1, until),
			connect.CodeInvalidArgument, typev1.ReasonCode_REASON_CODE_REQUEST_ID_MISSING,
		},
		{
			"missing item id", snooze("f-1", "", 1, until),
			connect.CodeInvalidArgument, typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
		},
		{
			"zero version", snooze("f-2", "ti-2", 0, until),
			connect.CodeInvalidArgument, typev1.ReasonCode_REASON_CODE_VERSION_MISMATCH,
		},
		{
			"missing until", snooze("f-3", "ti-2", 1, nil),
			connect.CodeInvalidArgument, typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
		},
		{
			"past until", snooze("f-4", "ti-2", 1, timestamppb.New(fleetNow.Add(-time.Hour))),
			connect.CodeInvalidArgument, typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
		},
		{
			"unknown item", snooze("f-5", "ti-missing", 1, until),
			connect.CodeNotFound, typev1.ReasonCode_REASON_CODE_RESOURCE_NOT_FOUND,
		},
		{
			"stale version", snooze("f-6", "ti-2", 9, until),
			connect.CodeFailedPrecondition, typev1.ReasonCode_REASON_CODE_VERSION_MISMATCH,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			code, reason, _ := detail(t, tc.err)
			if code != tc.code || reason != tc.reason {
				t.Errorf("refusal = %v/%v, want %v/%v", code, reason, tc.code, tc.reason)
			}
		})
	}
	if got := metadataOf(t, snooze("f-7", "ti-2", 9, until))["current_version"]; got != "1" {
		t.Errorf("stale current_version = %q, want 1", got)
	}
}

// TestDismissNoteLimits proves the 02b size rule on the dismiss note: over
// 512 bytes is FIELD_TOO_LARGE and control characters are refused.
func TestDismissNoteLimits(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"), trusted(false))
	seedTriageItem(t, e, "ti-3")
	dismiss := func(reqID, note string) error {
		_, err := e.client.DismissTriageItem(t.Context(), withSecret(connect.NewRequest(&hubv1.DismissTriageItemRequest{
			RequestId: reqID, Id: &hubv1.TriageItemId{Value: "ti-3"},
			ExpectedVersion: 1, Note: note,
		}), ""))
		return err
	}
	_, reason, _ := detail(t, dismiss("n-1", strings.Repeat("n", 513)))
	if reason != typev1.ReasonCode_REASON_CODE_FIELD_TOO_LARGE {
		t.Errorf("long note reason = %v, want FIELD_TOO_LARGE", reason)
	}
	code, reason, _ := detail(t, dismiss("n-2", "done\x00here"))
	if code != connect.CodeInvalidArgument || reason != typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT {
		t.Errorf("control note = %v/%v, want INVALID_ARGUMENT/INVALID_ARGUMENT", code, reason)
	}
}

// TestTriageWritesNeedTheirScope proves a caller without APPROVALS cannot
// snooze or dismiss: the agent principal is refused before any state is
// touched.
func TestTriageWritesNeedTheirScope(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, &typev1.Principal{
		Kind: typev1.PrincipalKind_PRINCIPAL_KIND_API_TOKEN,
		Id:   &typev1.PrincipalId{Value: "token/1"},
	}, trusted(false))
	_, err := e.client.SnoozeTriageItem(t.Context(), connect.NewRequest(&hubv1.SnoozeTriageItemRequest{
		RequestId: "s-1", Id: &hubv1.TriageItemId{Value: "ti-1"},
		ExpectedVersion: 1, UntilTime: timestamppb.New(fleetNow.Add(time.Hour)),
	}))
	code, reason, _ := detail(t, err)
	if code != connect.CodePermissionDenied || reason != typev1.ReasonCode_REASON_CODE_SCOPE_MISSING {
		t.Errorf("machine snooze = %v/%v, want PERMISSION_DENIED/SCOPE_MISSING", code, reason)
	}
}
