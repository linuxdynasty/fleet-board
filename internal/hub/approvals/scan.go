package approvals

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
)

// severityName is the store spelling of a Severity.
func severityName(s hubv1.Severity) string {
	switch s {
	case hubv1.Severity_SEVERITY_INFO:
		return "INFO"
	case hubv1.Severity_SEVERITY_WARNING:
		return "WARNING"
	case hubv1.Severity_SEVERITY_CRITICAL:
		return "CRITICAL"
	default:
		return ""
	}
}

// parseSeverity reads a store severity back.
func parseSeverity(s string) hubv1.Severity {
	switch s {
	case "INFO":
		return hubv1.Severity_SEVERITY_INFO
	case "WARNING":
		return hubv1.Severity_SEVERITY_WARNING
	case "CRITICAL":
		return hubv1.Severity_SEVERITY_CRITICAL
	default:
		return hubv1.Severity_SEVERITY_UNSPECIFIED
	}
}

// parseKind reads a store kind back.
func parseKind(s string) hubv1.ApprovalKind {
	switch s {
	case "PROMPT_ANSWER":
		return hubv1.ApprovalKind_APPROVAL_KIND_PROMPT_ANSWER
	case "COMMAND_CONFIRM":
		return hubv1.ApprovalKind_APPROVAL_KIND_COMMAND_CONFIRM
	case "DISPATCH_CONFIRM":
		return hubv1.ApprovalKind_APPROVAL_KIND_DISPATCH_CONFIRM
	default:
		return hubv1.ApprovalKind_APPROVAL_KIND_UNSPECIFIED
	}
}

// parseState reads a store state back.
func parseState(s string) hubv1.ApprovalState {
	switch s {
	case "PENDING":
		return hubv1.ApprovalState_APPROVAL_STATE_PENDING
	case "DECIDED":
		return hubv1.ApprovalState_APPROVAL_STATE_DECIDED
	case "EXPIRED":
		return hubv1.ApprovalState_APPROVAL_STATE_EXPIRED
	case "WITHDRAWN":
		return hubv1.ApprovalState_APPROVAL_STATE_WITHDRAWN
	default:
		return hubv1.ApprovalState_APPROVAL_STATE_UNSPECIFIED
	}
}

// fallbackName is the store spelling of an ApprovalFallback.
func fallbackName(f hubv1.ApprovalFallback) string {
	if f == hubv1.ApprovalFallback_APPROVAL_FALLBACK_DENY {
		return "DENY"
	}
	return ""
}

// roleName is the store spelling of a Role.
func roleName(r typev1.Role) string {
	switch r {
	case typev1.Role_ROLE_OPERATOR:
		return "OPERATOR"
	case typev1.Role_ROLE_OWNER:
		return "OWNER"
	default:
		return ""
	}
}

// parseRole reads a store role back.
func parseRole(s string) typev1.Role {
	switch s {
	case "OPERATOR":
		return typev1.Role_ROLE_OPERATOR
	case "OWNER":
		return typev1.Role_ROLE_OWNER
	default:
		return typev1.Role_ROLE_UNSPECIFIED
	}
}

func paneServer(p *typev1.PaneKey) string {
	if p == nil {
		return ""
	}
	return p.GetServer()
}

func paneStartID(p *typev1.PaneKey) string {
	if p == nil {
		return ""
	}
	return p.GetServerStartId()
}

func paneID(p *typev1.PaneKey) string {
	if p == nil {
		return ""
	}
	return p.GetPaneId()
}

func choicesToProto(in []Choice) []*hubv1.ApprovalChoice {
	out := make([]*hubv1.ApprovalChoice, 0, len(in))
	for _, c := range in {
		out = append(out, &hubv1.ApprovalChoice{Key: c.Key, Label: c.Label, Destructive: c.Destructive})
	}
	return out
}

func choicesFromJSON(raw string) ([]*hubv1.ApprovalChoice, error) {
	var in []Choice
	if err := json.Unmarshal([]byte(raw), &in); err != nil {
		return nil, fmt.Errorf("approvals: decode allowed decisions: %w", err)
	}
	return choicesToProto(in), nil
}

// scanner is what sql.Row and sql.Rows share for scan.
type scanner interface {
	Scan(dest ...any) error
}

// record is one approvals row: the wire message plus the prompt id the
// in-flight answer is keyed on and the sent command (neither has a wire
// field carrying it).
type record struct {
	approval  *hubv1.Approval
	promptID  string
	operation string
}

func scan(s scanner) (record, error) {
	var rec record
	var id string
	var version int64
	var kind, state, severity, preview string
	var machineID, sessionID, paneServer, paneStartID, paneID, goalID, triageItemID, promptID string
	var allowed, defaultChoice, fallback, requiredRole string
	var expireMillis int64
	var decidedByID, decidedByKind, decidedByName, decision string
	var decidedMillis sql.NullInt64
	var operation string
	var createMillis, updateMillis int64
	if err := s.Scan(&id, &version, &kind, &state, &severity, &preview,
		&machineID, &sessionID, &paneServer, &paneStartID, &paneID, &goalID, &triageItemID, &promptID, &operation,
		&allowed, &defaultChoice, &fallback, &expireMillis, &requiredRole,
		&decidedByID, &decidedByKind, &decidedByName, &decision, &decidedMillis,
		&createMillis, &updateMillis); err != nil {
		return rec, err
	}
	choices, err := choicesFromJSON(allowed)
	if err != nil {
		return rec, err
	}
	rec.promptID = promptID
	rec.operation = operation
	// Message fields stay absent when the row holds nothing for them, so
	// has_machine/has_pane and friends tell "no value" from "empty value".
	a := &hubv1.Approval{
		Id:               &hubv1.ApprovalId{Value: id},
		Version:          uint64(version), //nolint:gosec // G115: approvals.version has CHECK >= 1
		CreateTime:       timestamppb.New(time.UnixMilli(createMillis).UTC()),
		UpdateTime:       timestamppb.New(time.UnixMilli(updateMillis).UTC()),
		Kind:             parseKind(kind),
		State:            parseState(state),
		Severity:         parseSeverity(severity),
		Preview:          preview,
		AllowedDecisions: choices,
		DefaultChoice:    defaultChoice,
		ExpireTime:       timestamppb.New(time.UnixMilli(expireMillis).UTC()),
		RequiredRole:     parseRole(requiredRole),
		Decision:         decision,
	}
	if machineID != "" {
		a.Machine = &typev1.MachineId{Value: machineID}
	}
	if sessionID != "" {
		a.Session = &typev1.SessionId{Value: sessionID}
	}
	if paneServer != "" || paneStartID != "" || paneID != "" {
		a.Pane = &typev1.PaneKey{Server: paneServer, ServerStartId: paneStartID, PaneId: paneID}
	}
	if goalID != "" {
		a.Goal = &typev1.GoalId{Value: goalID}
	}
	if triageItemID != "" {
		a.TriageItem = &hubv1.TriageItemId{Value: triageItemID}
	}
	if operation != "" {
		a.Operation = &typev1.CommandId{Value: operation}
	}
	if fallback == "DENY" {
		a.Fallback = hubv1.ApprovalFallback_APPROVAL_FALLBACK_DENY
	}
	if decidedByID != "" || decision != "" {
		a.DecidedBy = &typev1.Principal{
			Id:   &typev1.PrincipalId{Value: decidedByID},
			Kind: principalKind(decidedByKind),
			Name: decidedByName,
		}
	}
	if decidedMillis.Valid {
		a.DecidedTime = timestamppb.New(time.UnixMilli(decidedMillis.Int64).UTC())
	}
	rec.approval = a
	return rec, nil
}

// kindNumber stores a principal kind as its number, exact across renames.
func kindNumber(k typev1.PrincipalKind) string {
	return strconv.Itoa(int(k.Number()))
}

// principalKind reads a stored principal kind number back.
func principalKind(s string) typev1.PrincipalKind {
	n, err := strconv.Atoi(s)
	if err != nil {
		return typev1.PrincipalKind_PRINCIPAL_KIND_UNSPECIFIED
	}
	return typev1.PrincipalKind(n) //nolint:gosec // G115: stored by kindNumber from a valid enum
}

// isConflict reports a UNIQUE violation on the approvals id.
func isConflict(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed: approvals.id")
}
