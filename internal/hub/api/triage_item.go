package api

import (
	"fmt"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/timestamppb"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

// msTimestamp renders Unix milliseconds as a Timestamp, nil when unset:
// a dismissed item carries no wake time and a snoozed one no acted time.
func msTimestamp(ms int64) *timestamppb.Timestamp {
	if ms == 0 {
		return nil
	}
	return timestamppb.New(millisTime(ms))
}

func triageKindOf(kind string) hubv1.TriageKind {
	if v, ok := hubv1.TriageKind_value["TRIAGE_KIND_"+kind]; ok {
		return hubv1.TriageKind(v)
	}
	return hubv1.TriageKind_TRIAGE_KIND_UNSPECIFIED
}

func triageSeverityOf(severity string) hubv1.Severity {
	if v, ok := hubv1.Severity_value["SEVERITY_"+severity]; ok {
		return hubv1.Severity(v)
	}
	return hubv1.Severity_SEVERITY_UNSPECIFIED
}

func triageStateOf(state string) hubv1.TriageState {
	if v, ok := hubv1.TriageState_value["TRIAGE_STATE_"+state]; ok {
		return hubv1.TriageState(v)
	}
	return hubv1.TriageState_TRIAGE_STATE_UNSPECIFIED
}

// setTriageDetail rebuilds the item's detail arm from its stored kind
// and protojson. An empty kind is an item with no card yet.
func setTriageDetail(item *hubv1.TriageItem, kind, raw string) error {
	switch kind {
	case "":
		return nil
	case "prompt":
		var d hubv1.PromptDetail
		if err := protojson.Unmarshal([]byte(raw), &d); err != nil {
			return fmt.Errorf("api: decode prompt detail: %w", err)
		}
		item.Detail = &hubv1.TriageItem_Prompt{Prompt: &d}
	case "condition":
		var d hubv1.ConditionDetail
		if err := protojson.Unmarshal([]byte(raw), &d); err != nil {
			return fmt.Errorf("api: decode condition detail: %w", err)
		}
		item.Detail = &hubv1.TriageItem_Condition{Condition: &d}
	case "note":
		var d hubv1.NoteDetail
		if err := protojson.Unmarshal([]byte(raw), &d); err != nil {
			return fmt.Errorf("api: decode note detail: %w", err)
		}
		item.Detail = &hubv1.TriageItem_Note{Note: &d}
	case "system":
		var d hubv1.SystemDetail
		if err := protojson.Unmarshal([]byte(raw), &d); err != nil {
			return fmt.Errorf("api: decode system detail: %w", err)
		}
		item.Detail = &hubv1.TriageItem_System{System: &d}
	case "suggestion":
		var d hubv1.SuggestionDetail
		if err := protojson.Unmarshal([]byte(raw), &d); err != nil {
			return fmt.Errorf("api: decode suggestion detail: %w", err)
		}
		item.Detail = &hubv1.TriageItem_Suggestion{Suggestion: &d}
	default:
		return fmt.Errorf("api: unknown triage detail kind %q", kind)
	}
	return nil
}

func triageMachineID(v string) *typev1.MachineId {
	if v == "" {
		return nil
	}
	return &typev1.MachineId{Value: v}
}

func triageSessionID(v string) *typev1.SessionId {
	if v == "" {
		return nil
	}
	return &typev1.SessionId{Value: v}
}

func triageGoalID(v string) *typev1.GoalId {
	if v == "" {
		return nil
	}
	return &typev1.GoalId{Value: v}
}

func triageAccountID(v string) *typev1.AccountId {
	if v == "" {
		return nil
	}
	return &typev1.AccountId{Value: v}
}

// triageActedBy rebuilds the operator the write recorded. The role is
// output-only here: the audit row keeps the full principal.
func triageActedBy(a statedb.Actor) *typev1.Principal {
	if a.ID == "" {
		return nil
	}
	var kind typev1.PrincipalKind
	if v, ok := typev1.PrincipalKind_value["PRINCIPAL_KIND_"+a.Kind]; ok {
		kind = typev1.PrincipalKind(v)
	}
	return &typev1.Principal{
		Id: &typev1.PrincipalId{Value: a.ID}, Kind: kind, Name: a.Name,
	}
}

func triageLastOperation(v string) *typev1.CommandId {
	if v == "" {
		return nil
	}
	return &typev1.CommandId{Value: v}
}

// triageItem answers the TriageItem of a stored row. A mapping failure
// (a detail the hub cannot decode) is the hub's own corruption: the
// write already committed, so the caller gets INTERNAL, not a refusal.
func triageItem(stored statedb.TriageStored) (*hubv1.TriageItem, error) {
	item := &hubv1.TriageItem{
		Id:               &hubv1.TriageItemId{Value: stored.ID},
		Version:          stored.Version,
		CreateTime:       msTimestamp(stored.CreateMs),
		UpdateTime:       msTimestamp(stored.UpdateMs),
		Kind:             triageKindOf(stored.Kind),
		Severity:         triageSeverityOf(stored.Severity),
		State:            triageStateOf(stored.State),
		Fingerprint:      stored.Fingerprint,
		Title:            stored.Title,
		Machine:          triageMachineID(stored.MachineID),
		Session:          triageSessionID(stored.SessionID),
		Goal:             triageGoalID(stored.GoalID),
		Account:          triageAccountID(stored.AccountID),
		SnoozedUntilTime: msTimestamp(stored.SnoozedUntilMs),
		ExpireTime:       msTimestamp(stored.ExpireMs),
		ActedBy:          triageActedBy(stored.ActedBy),
		ActedTime:        msTimestamp(stored.ActedMs),
		LastOperation:    triageLastOperation(stored.LastOperation),
	}
	if err := setTriageDetail(item, stored.DetailKind, stored.DetailJSON); err != nil {
		return nil, err
	}
	return item, nil
}
