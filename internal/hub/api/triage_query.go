package api

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

// This file is the ListTriageItems query: the 02c field table, filter
// evaluation, ordering, and the query-bound page tokens. The handlers
// and the TriageItem assembly live in triage.go (the item itself in
// triage_item.go).

// triageFilterFields is the 02c TriageItem field table. Every listed
// field reads a stored hub-state column: the hub raises items with
// their machine, session, goal and account refs, so nothing is refused.
var triageFilterFields = map[string]filterField{
	"id":       {kind: kindString, ops: map[filterOp]bool{opEq: true, opIn: true}},
	"state":    {kind: kindEnum, ops: map[filterOp]bool{opEq: true, opNe: true, opIn: true}, enum: triageStates},
	"kind":     {kind: kindEnum, ops: map[filterOp]bool{opEq: true, opIn: true}, enum: triageKinds},
	"severity": {kind: kindEnum, ops: map[filterOp]bool{opEq: true, opGe: true, opIn: true}, enum: triageSeverities},
	"machine":  {kind: kindString, ops: map[filterOp]bool{opEq: true, opIn: true}},
	"session":  {kind: kindString, ops: map[filterOp]bool{opEq: true, opIn: true}},
	"goal":     {kind: kindString, ops: map[filterOp]bool{opEq: true, opIn: true}},
	"account":  {kind: kindString, ops: map[filterOp]bool{opEq: true, opIn: true}},
	"create_time": {kind: kindTime, ops: map[filterOp]bool{
		opEq: true, opNe: true, opLt: true, opLe: true, opGt: true, opGe: true,
	}},
	"update_time": {kind: kindTime, ops: map[filterOp]bool{
		opEq: true, opNe: true, opLt: true, opLe: true, opGt: true, opGe: true,
	}},
}

// triageStates is the 02c TriageState short-name set, every value the
// contract names.
var triageStates = map[string]bool{
	"UNSPECIFIED": true, "OPEN": true, "SNOOZED": true, "IN_FLIGHT": true,
	"ACTED": true, "RETIRED": true, "EXPIRED": true, "DISMISSED": true,
}

// triageKinds is the 02c TriageKind short-name set, every value the
// contract names.
var triageKinds = map[string]bool{
	"UNSPECIFIED": true, "PROMPT": true, "CONDITION": true, "STUCK": true,
	"BLOCKED_OWNER": true, "DONE_DISAGREEMENT": true, "READY_FOR_HUMAN_MERGE": true,
	"REVIEW_LOOP": true, "PACING": true, "START_UNKNOWN": true, "ORPHAN_GOAL_PANE": true,
	"DOUBLE_CLAIM": true, "INTEGRATION_ACCESS_LOST": true, "USAGE_PARSE_FAILED": true,
	"PARSER_UNSUPPORTED": true, "DUPLICATE_IDENTITY": true, "POLICY_CHANGED": true,
	"HOSTNAME_MISMATCH": true, "DIRTY_WORKTREE": true, "LIMIT_WAIT": true,
	"LOG_DISK_LOW": true, "POLICY_INVALID": true, "PARSER_DEGRADED": true,
	"START_FAILED": true, "WORK_ITEM_CHANGED": true, "ROUTINE_BLOCKED": true,
	"SETTINGS_UNENFORCED": true, "SYSTEM": true, "SUGGESTION": true,
	"GOAL_PAUSED": true, "OTHER": true,
}

// triageSeverities is the 02c Severity short-name set, every value the
// contract names.
var triageSeverities = map[string]bool{
	"UNSPECIFIED": true, "INFO": true, "WARNING": true, "ACTION": true, "CRITICAL": true,
}

// triageSeverityRank orders severities for the `>=` filter and the
// severity sort: the most severe first when descending.
func triageSeverityRank(severity string) int {
	switch severity {
	case "CRITICAL":
		return 4
	case "ACTION":
		return 3
	case "WARNING":
		return 2
	case "INFO":
		return 1
	default:
		return 0
	}
}

// matchSeverity compares a stored severity short name against enum
// literals by rank, so `severity >= WARNING` holds for WARNING, ACTION
// and CRITICAL.
func matchSeverity(op filterOp, got string, want []string) bool {
	gank := triageSeverityRank(got)
	switch op {
	case opEq:
		return got == want[0]
	case opGe:
		return gank >= triageSeverityRank(want[0])
	case opIn:
		for _, w := range want {
			if got == w {
				return true
			}
		}
		return false
	default:
		// The parse allows only =, >= and in on severity.
		return false
	}
}

// triageOrderFields is the 02c TriageItem order_by catalogue.
var triageOrderFields = map[string]bool{"severity": true, "ct": true}

// defaultTriageOrder is the List order with no order_by: most severe
// first, then oldest first.
var defaultTriageOrder = []orderTerm{{field: "severity", desc: true}, {field: "ct"}}

// triageView is one stored triage item, so the filter and the order
// tuple read the same values the TriageItem carries.
type triageView struct {
	stored statedb.TriageStored
}

func nodeMatchesTriage(n *filterNode, v triageView) bool {
	if n == nil {
		return true
	}
	switch n.op {
	case nodeAtom:
		return atomMatchesTriage(n.atom, v)
	case nodeNot:
		return !nodeMatchesTriage(n.kids[0], v)
	case nodeAnd:
		for _, k := range n.kids {
			if !nodeMatchesTriage(k, v) {
				return false
			}
		}
		return true
	case nodeOr:
		for _, k := range n.kids {
			if nodeMatchesTriage(k, v) {
				return true
			}
		}
		return false
	}
	return false
}

// atomMatchesTriage evaluates one comparison. The parse already checked
// the field, the operator and every literal kind, so an unknown field
// here is unreachable and matches nothing.
func atomMatchesTriage(a filterAtom, v triageView) bool {
	switch a.field {
	case "id":
		return matchStrings(a.op, v.stored.ID, a.strs)
	case "state":
		return matchStrings(a.op, v.stored.State, a.strs)
	case "kind":
		return matchStrings(a.op, v.stored.Kind, a.strs)
	case "severity":
		return matchSeverity(a.op, v.stored.Severity, a.strs)
	case "machine":
		return matchStrings(a.op, v.stored.MachineID, a.strs)
	case "session":
		return matchStrings(a.op, v.stored.SessionID, a.strs)
	case "goal":
		return matchStrings(a.op, v.stored.GoalID, a.strs)
	case "account":
		return matchStrings(a.op, v.stored.AccountID, a.strs)
	case "create_time":
		return matchTimes(a.op, v.stored.CreateMs, a.times)
	case "update_time":
		return matchTimes(a.op, v.stored.UpdateMs, a.times)
	}
	return false
}

// triageSortKey renders one item's order tuple as fixed strings: the
// severity rank and the create time zero-padded so string order is
// severity and time order. The canonical id breaks ties, like every
// other order.
func triageSortKey(v triageView, order []orderTerm) []string {
	key := make([]string, 0, len(order))
	for _, t := range order {
		switch t.field {
		case "severity":
			key = append(key, fmt.Sprintf("%010d", triageSeverityRank(v.stored.Severity)))
		default:
			key = append(key, fmt.Sprintf("%020d", v.stored.CreateMs))
		}
	}
	return key
}

// triageKeyAfter reports whether the item sorts strictly after (key,
// id) under order, with the canonical id as the final ascending
// tiebreak.
func triageKeyAfter(v triageView, order []orderTerm, key []string, id string) bool {
	mine := triageSortKey(v, order)
	for i, t := range order {
		if mine[i] == key[i] {
			continue
		}
		if t.desc {
			return mine[i] < key[i]
		}
		return mine[i] > key[i]
	}
	return v.stored.ID > id
}

func triageLess(order []orderTerm, a, b triageView) bool {
	ka, kb := triageSortKey(a, order), triageSortKey(b, order)
	for i, t := range order {
		if ka[i] == kb[i] {
			continue
		}
		if t.desc {
			return ka[i] > kb[i]
		}
		return ka[i] < kb[i]
	}
	return a.stored.ID < b.stored.ID
}

func sortTriageViews(views []triageView, order []orderTerm) {
	sort.Slice(views, func(i, j int) bool { return triageLess(order, views[i], views[j]) })
}

// triagePageLife is how long a ListTriageItems page token stays valid
// (02b paging: opaque, valid for 10 min, from the same query).
const triagePageLife = 10 * time.Minute

// triageQueryHash binds a page token to the query that minted it: the
// raw filter and order_by plus the page size. A token from another query
// is rejected.
func triageQueryHash(filter, order string, size int) string {
	sum := sha256.Sum256([]byte(filter + "\x00" + order + "\x00" + fmt.Sprint(size)))
	return hex.EncodeToString(sum[:])
}

// triageCursor is a decoded ListTriageItems page token.
type triageCursor struct {
	key []string
	id  string
}

// triagePageToken is the minted form: version, query hash, sort key,
// id, expiry.
type triagePageToken struct {
	V   int      `json:"v"`
	Q   string   `json:"q"`
	Key []string `json:"key"`
	ID  string   `json:"id"`
	Exp int64    `json:"exp"`
}

// mintTriageToken seals a next-page token: clients treat it as opaque.
func mintTriageToken(query string, key []string, id string, now time.Time) string {
	raw, err := json.Marshal(triagePageToken{
		V: 1, Q: query, Key: key, ID: id, Exp: now.Add(triagePageLife).Unix(),
	})
	if err != nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(raw)
}

// parseTriageToken opens a page token minted by [mintTriageToken] for
// the same query and order. Anything else is an error the handler
// reports as PAGE_TOKEN_INVALID. The key length is checked so a forged
// short key can never index past the order tuple.
func parseTriageToken(token, want string, keyLen int, now time.Time) (triageCursor, error) {
	if len(token) > maxFilterBytes {
		return triageCursor{}, errors.New("api: triage page token over 4 KiB")
	}
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return triageCursor{}, err
	}
	var pt triagePageToken
	if err := json.Unmarshal(raw, &pt); err != nil {
		return triageCursor{}, err
	}
	if pt.V != 1 || pt.Q != want || len(pt.Key) != keyLen || now.Unix() > pt.Exp {
		return triageCursor{}, errors.New("api: bad triage page token")
	}
	return triageCursor{key: pt.Key, id: pt.ID}, nil
}
