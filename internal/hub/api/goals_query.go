package api

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

// This file is the ListGoals query: the 02c field table, filter
// evaluation, ordering, and the query-bound page tokens. The handlers
// and the Goal assembly live in goals.go.

// goalFilterFields is the 02c Goal field table. phase, machine, origin,
// session, phase_reason, live and the times are served from the goals row
// and its stored status; account, repo, routine, work_item and attempt
// need the dispatch and repo rows, which no row stores yet, so they are
// refused until those rows land.
var goalFilterFields = map[string]filterField{
	"id":        {kind: kindString, ops: map[filterOp]bool{opEq: true, opIn: true}},
	"phase":     {kind: kindEnum, ops: map[filterOp]bool{opEq: true, opNe: true, opIn: true}, enum: goalPhases},
	"machine":   {kind: kindString, ops: map[filterOp]bool{opEq: true, opIn: true}},
	"account":   {unserved: "the hub stores no dispatched goal rows yet"},
	"repo":      {unserved: "the hub stores no dispatched goal rows yet"},
	"routine":   {unserved: "the hub stores no dispatched goal rows yet"},
	"work_item": {unserved: "the hub stores no dispatched goal rows yet"},
	"session":   {kind: kindString, ops: map[filterOp]bool{opEq: true}},
	"phase_reason": {
		kind: kindEnum, ops: map[filterOp]bool{opEq: true, opIn: true}, enum: goalPhaseReasons,
	},
	"start_time": {kind: kindTime, ops: map[filterOp]bool{
		opLt: true, opLe: true, opGt: true, opGe: true,
	}},
	"end_time": {kind: kindTime, ops: map[filterOp]bool{
		opLt: true, opLe: true, opGt: true, opGe: true,
	}},
	"live":    {kind: kindBool, ops: map[filterOp]bool{opEq: true}},
	"attempt": {unserved: "the hub stores no dispatched goal rows yet"},
	"origin":  {kind: kindEnum, ops: map[filterOp]bool{opEq: true, opIn: true}, enum: goalOrigins},
	"create_time": {kind: kindTime, ops: map[filterOp]bool{
		opEq: true, opNe: true, opLt: true, opLe: true, opGt: true, opGe: true,
	}},
	"update_time": {kind: kindTime, ops: map[filterOp]bool{
		opEq: true, opNe: true, opLt: true, opLe: true, opGt: true, opGe: true,
	}},
}

// goalPhases is the 02c GoalPhase short-name set, every value the
// contract names. Names no observed goal ever holds parse and match
// nothing until dispatch rows land, instead of failing as unknown.
var goalPhases = map[string]bool{
	"UNSPECIFIED": true, "STARTING": true, "START_FAILED": true,
	"START_UNKNOWN": true, "RUNNING": true, "WAITING": true,
	"BLOCKED_OWNER": true, "STUCK": true, "STOPPED": true,
	"COOLDOWN": true, "PAUSED": true, "LIMIT_WAIT": true,
	"VERIFYING": true, "DISAGREE": true, "MERGED": true, "CLOSED": true,
	"CLEARED": true, "PANE_LOST": true, "START_EXHAUSTED": true,
	"EXPIRED": true, "ABANDONED": true, "CANCELLED": true,
}

// goalPhaseReasons is the 02c GoalPhaseReason short-name set, every value
// the contract names.
var goalPhaseReasons = map[string]bool{
	"UNSPECIFIED": true, "BLOCKED_NO_WATCHER": true, "WATCH_DEAD": true,
	"STOPPED_TWICE": true, "SESSION_CLOSED": true,
	"LOCAL_CONFIRM_TIMEOUT": true, "PANE_LOST_TIMEOUT": true,
	"NO_PROGRESS": true, "USAGE_LIMIT": true, "RATE_LIMITED": true,
	"DONE_CLAIMED": true, "EXTERNAL_MERGE": true, "REARMED": true,
	"RESUMED": true, "START_NOT_DELIVERED": true,
	"OPERATOR_NOT_STARTED": true, "DRAINING": true, "MACHINE_REVOKED": true,
	"WORK_ITEM_CLOSED": true, "CHANGE_REQUEST_CLOSED": true,
	"OPERATOR_CLEAR": true, "OBSERVED_DONE": true, "OBSERVED_GONE": true,
}

// goalOrigins is the 02c GoalOrigin short-name set.
var goalOrigins = map[string]bool{"OBSERVED": true, "DISPATCHED": true}

// goalOrderFields is the 02c Goal order_by catalogue.
var goalOrderFields = map[string]bool{"ct": true, "ut": true, "phase_time": true, "start_time": true}

// defaultGoalOrder is the List order with no order_by: when the goal
// started (first sight), oldest first.
var defaultGoalOrder = []orderTerm{{field: "start_time"}}

// goalView is one stored goal with its decoded spec and status, so the
// filter and the order tuple read the same values the Goal carries.
type goalView struct {
	row    statedb.GoalRow
	spec   *hubv1.GoalDefinition
	status *hubv1.GoalStatus
}

// goalPhaseShort renders the stored full phase name as its 02c short name.
func goalPhaseShort(full string) string {
	return strings.TrimPrefix(full, "GOAL_PHASE_")
}

// goalReasonShort renders the stored full reason name as its short name.
func goalReasonShort(full string) string {
	return strings.TrimPrefix(full, "GOAL_PHASE_REASON_")
}

// goalLivePhases is the 02c live derivation: the ACTIVE and HELD phase
// classes of 03l. An observed goal holds no slot, but its open phases
// are in those classes, so an open observed goal reads live.
var goalLivePhases = map[string]bool{
	"STARTING": true, "START_UNKNOWN": true, "RUNNING": true,
	"STOPPED": true, "WAITING": true, "BLOCKED_OWNER": true, "STUCK": true,
	"COOLDOWN": true, "PAUSED": true, "LIMIT_WAIT": true,
	"VERIFYING": true, "DISAGREE": true, "PANE_LOST": true,
}

// goalLive reports whether the stored phase is live.
func goalLive(phase string) bool {
	return goalLivePhases[goalPhaseShort(phase)]
}

// goalPhaseTimeMs is the status phase_time in UnixMillis, 0 when unset.
func goalPhaseTimeMs(status *hubv1.GoalStatus) int64 {
	if status.GetPhaseTime() == nil {
		return 0
	}
	return status.GetPhaseTime().AsTime().UnixMilli()
}

// goalSession is the pane session the status reports, empty when none did.
func goalSession(status *hubv1.GoalStatus) string {
	return status.GetSession().GetValue()
}

func nodeMatchesGoal(n *filterNode, v goalView) bool {
	if n == nil {
		return true
	}
	switch n.op {
	case nodeAtom:
		return atomMatchesGoal(n.atom, v)
	case nodeNot:
		return !nodeMatchesGoal(n.kids[0], v)
	case nodeAnd:
		for _, k := range n.kids {
			if !nodeMatchesGoal(k, v) {
				return false
			}
		}
		return true
	case nodeOr:
		for _, k := range n.kids {
			if nodeMatchesGoal(k, v) {
				return true
			}
		}
		return false
	}
	return false
}

// atomMatchesGoal evaluates one comparison. The parse already checked the
// field, the operator and every literal kind, so an unknown field here is
// unreachable and matches nothing. An open goal has no end_time, so
// end_time comparisons match nothing until the goal closes.
func atomMatchesGoal(a filterAtom, v goalView) bool {
	switch a.field {
	case "id":
		return matchStrings(a.op, v.row.ID, a.strs)
	case "phase":
		return matchStrings(a.op, goalPhaseShort(v.row.Phase), a.strs)
	case "machine":
		return matchStrings(a.op, v.row.MachineID, a.strs)
	case "session":
		return matchStrings(a.op, goalSession(v.status), a.strs)
	case "phase_reason":
		return matchStrings(a.op, goalReasonShort(v.row.Reason), a.strs)
	case "start_time":
		return matchTimes(a.op, v.row.StartMs, a.times)
	case "end_time":
		if v.row.EndMs == nil {
			return false
		}
		return matchTimes(a.op, *v.row.EndMs, a.times)
	case "live":
		return a.bools[0] == goalLive(v.row.Phase)
	case "origin":
		return matchStrings(a.op, strings.ToUpper(v.row.Origin), a.strs)
	case "create_time":
		// create_time is the first sight: the spec never changes, so the
		// version never moves past 1.
		return matchTimes(a.op, v.row.StartMs, a.times)
	case "update_time":
		return matchTimes(a.op, v.row.ObservedMs, a.times)
	}
	return false
}

// goalSortKey renders one goal's order tuple as fixed strings: times as
// zero-padded millis so string order is time order.
func goalSortKey(v goalView, order []orderTerm) []string {
	key := make([]string, 0, len(order))
	for _, t := range order {
		switch t.field {
		case "ut":
			key = append(key, fmt.Sprintf("%020d", v.row.ObservedMs))
		case "phase_time":
			key = append(key, fmt.Sprintf("%020d", goalPhaseTimeMs(v.status)))
		case "start_time", "ct":
			key = append(key, fmt.Sprintf("%020d", v.row.StartMs))
		default:
			key = append(key, v.row.ID)
		}
	}
	return key
}

// goalKeyAfter reports whether the goal sorts strictly after (key, id)
// under order, with goal id as the final ascending tiebreak.
func goalKeyAfter(v goalView, order []orderTerm, key []string, id string) bool {
	mine := goalSortKey(v, order)
	for i, t := range order {
		if mine[i] == key[i] {
			continue
		}
		if t.desc {
			return mine[i] < key[i]
		}
		return mine[i] > key[i]
	}
	return v.row.ID > id
}

func goalLess(order []orderTerm, a, b goalView) bool {
	ka, kb := goalSortKey(a, order), goalSortKey(b, order)
	for i, t := range order {
		if ka[i] == kb[i] {
			continue
		}
		if t.desc {
			return ka[i] > kb[i]
		}
		return ka[i] < kb[i]
	}
	return a.row.ID < b.row.ID
}

func sortGoalViews(views []goalView, order []orderTerm) {
	sort.Slice(views, func(i, j int) bool { return goalLess(order, views[i], views[j]) })
}

// goalPageLife is how long a ListGoals page token stays valid (02b paging:
// opaque, valid for 10 min, from the same query).
const goalPageLife = 10 * time.Minute

// goalQueryHash binds a page token to the query that minted it: the raw
// filter and order_by plus the page size. A token from another query is
// rejected.
func goalQueryHash(filter, order string, size int) string {
	sum := sha256.Sum256([]byte(filter + "\x00" + order + "\x00" + fmt.Sprint(size)))
	return hex.EncodeToString(sum[:])
}

// goalCursor is a decoded ListGoals page token.
type goalCursor struct {
	key []string
	id  string
}

// goalPageToken is the minted form: version, query hash, sort key, id,
// expiry.
type goalPageToken struct {
	V   int      `json:"v"`
	Q   string   `json:"q"`
	Key []string `json:"key"`
	ID  string   `json:"id"`
	Exp int64    `json:"exp"`
}

// mintGoalToken seals a next-page token: clients treat it as opaque.
func mintGoalToken(query string, key []string, id string, now time.Time) string {
	raw, err := json.Marshal(goalPageToken{
		V: 1, Q: query, Key: key, ID: id, Exp: now.Add(goalPageLife).Unix(),
	})
	if err != nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(raw)
}

// parseGoalToken opens a page token minted by [mintGoalToken] for the same
// query and order. Anything else is an error the handler reports as
// PAGE_TOKEN_INVALID. The key length is checked so a forged short key can
// never index past the order tuple.
func parseGoalToken(token, want string, keyLen int, now time.Time) (goalCursor, error) {
	if len(token) > maxFilterBytes {
		return goalCursor{}, errors.New("api: goal page token over 4 KiB")
	}
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return goalCursor{}, err
	}
	var pt goalPageToken
	if err := json.Unmarshal(raw, &pt); err != nil {
		return goalCursor{}, err
	}
	if pt.V != 1 || pt.Q != want || len(pt.Key) != keyLen || now.Unix() > pt.Exp {
		return goalCursor{}, errors.New("api: bad goal page token")
	}
	return goalCursor{key: pt.Key, id: pt.ID}, nil
}
