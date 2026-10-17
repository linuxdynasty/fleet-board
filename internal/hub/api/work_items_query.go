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
	integrationv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/integration/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

// This file is the ListWorkItems query: the 02c field table, filter
// evaluation, ordering, and the query-bound page tokens. The handlers
// and the WorkItem assembly live in work_items.go.

// workItemFilterFields is the 02c WorkItem field table. integration,
// kind, stage, state, trusted, repo, label and manual_reason are served
// from the mirror row and its stored item and trust; goal needs the
// dispatch rows' goal link, which no row stores yet, so it is refused
// until those rows land.
var workItemFilterFields = map[string]filterField{
	"integration": {kind: kindString, ops: map[filterOp]bool{opEq: true, opIn: true}},
	"kind":        {kind: kindEnum, ops: map[filterOp]bool{opEq: true, opIn: true}, enum: workItemKinds},
	"repo":        {kind: kindString, repeated: true, ops: map[filterOp]bool{opHas: true}},
	"stage":       {kind: kindEnum, ops: map[filterOp]bool{opEq: true, opNe: true, opIn: true}, enum: workStages},
	"state":       {kind: kindEnum, ops: map[filterOp]bool{opEq: true, opIn: true}, enum: workItemStates},
	"trusted":     {kind: kindBool, ops: map[filterOp]bool{opEq: true}},
	"label":       {kind: kindString, repeated: true, ops: map[filterOp]bool{opHas: true}},
	"goal":        {unserved: "the hub stores no work-item goal link yet"},
	"manual_reason": {
		kind: kindEnum, ops: map[filterOp]bool{opEq: true, opIn: true}, enum: workManualReasons,
	},
}

// workItemKinds is the 02c WorkItemKind short-name set, every value the
// contract names.
var workItemKinds = map[string]bool{
	"UNSPECIFIED": true, "ISSUE": true, "TICKET": true,
	"CHANGE_REQUEST": true, "ROUTINE": true, "OTHER": true,
}

// workItemStates is the 02c WorkItemState short-name set.
var workItemStates = map[string]bool{
	"UNSPECIFIED": true, "OPEN": true, "IN_PROGRESS": true, "CLOSED": true,
	"MERGED": true, "DRAFT": true, "CANCELLED": true,
}

// workStages is the 02c WorkStage short-name set.
var workStages = map[string]bool{
	"UNSPECIFIED": true, "SEEN": true, "READY": true, "MANUAL_ONLY": true,
	"CLAIMED_ELSEWHERE": true, "DISPATCHED": true, "RUNNING": true,
	"DONE_CLAIMED": true, "DISAGREE": true, "CLOSED": true,
}

// workManualReasons is the 02c WorkManualReason short-name set.
var workManualReasons = map[string]bool{
	"UNSPECIFIED": true, "UNTRUSTED": true, "GOAL_CLEARED": true,
	"START_EXHAUSTED": true, "EDITED_AFTER_TRUST": true,
}

// workItemOrderFields is the 02c WorkItem order_by catalogue.
var workItemOrderFields = map[string]bool{
	"ct": true, "ut": true, "provider_update_time": true, "dispatch_order": true,
}

// defaultWorkItemOrder is the List order with no order_by: oldest first
// sight first, the same first key as dispatch_order.
var defaultWorkItemOrder = []orderTerm{{field: "ct"}}

// workItemView is one stored mirror with its decoded provider item and
// trust, so the filter and the order tuple read the same values the
// WorkItem carries.
type workItemView struct {
	row   statedb.WorkItemRow
	item  *integrationv1.WorkItem
	trust *hubv1.TrustVerdict
}

// workKindShort renders a stored kind as its 02c short name. Mirrors
// carry the full proto name; a short name already stored reads unchanged.
func workKindShort(stored string) string {
	return strings.TrimPrefix(stored, "WORK_ITEM_KIND_")
}

// workStateShort renders the stored provider state as its short name.
func workStateShort(state integrationv1.WorkItemState) string {
	return strings.TrimPrefix(state.String(), "WORK_ITEM_STATE_")
}

// workStageShort renders the stored stage as its 02c short name. A stage
// number the contract has no name for matches nothing.
func workStageShort(stage int32) string {
	return strings.TrimPrefix(hubv1.WorkStage(stage).String(), "WORK_STAGE_")
}

// workReasonShort renders the stored manual reason as its short name.
func workReasonShort(reason int32) string {
	return strings.TrimPrefix(hubv1.WorkManualReason(reason).String(), "WORK_MANUAL_REASON_")
}

// workItemRepoRefs renders the item's repos as host/path strings.
func workItemRepoRefs(item *integrationv1.WorkItem) []string {
	var out []string
	for _, r := range item.GetRepos() {
		out = append(out, r.GetHost()+"/"+r.GetPath())
	}
	return out
}

// workItemLabels renders the item's label names.
func workItemLabels(item *integrationv1.WorkItem) []string {
	var out []string
	for _, l := range item.GetLabels() {
		out = append(out, l.GetName())
	}
	return out
}

// workProviderUpdateMs is the provider item's own update in UnixMillis,
// 0 when the mirror carries none.
func workProviderUpdateMs(item *integrationv1.WorkItem) int64 {
	if item.GetUpdateTime() == nil {
		return 0
	}
	return item.GetUpdateTime().AsTime().UnixMilli()
}

// matchContains reports whether one of got equals the `:` value.
func matchContains(got []string, want string) bool {
	for _, g := range got {
		if g == want {
			return true
		}
	}
	return false
}

func nodeMatchesWorkItem(n *filterNode, v workItemView) bool {
	if n == nil {
		return true
	}
	switch n.op {
	case nodeAtom:
		return atomMatchesWorkItem(n.atom, v)
	case nodeNot:
		return !nodeMatchesWorkItem(n.kids[0], v)
	case nodeAnd:
		for _, k := range n.kids {
			if !nodeMatchesWorkItem(k, v) {
				return false
			}
		}
		return true
	case nodeOr:
		for _, k := range n.kids {
			if nodeMatchesWorkItem(k, v) {
				return true
			}
		}
		return false
	}
	return false
}

// atomMatchesWorkItem evaluates one comparison. The parse already checked
// the field, the operator and every literal kind, so an unknown field
// here is unreachable and matches nothing.
func atomMatchesWorkItem(a filterAtom, v workItemView) bool {
	switch a.field {
	case "integration":
		return matchStrings(a.op, v.row.Integration, a.strs)
	case "kind":
		return matchStrings(a.op, workKindShort(v.row.Kind), a.strs)
	case "repo":
		return matchContains(workItemRepoRefs(v.item), a.strs[0])
	case "stage":
		return matchStrings(a.op, workStageShort(v.row.Stage), a.strs)
	case "state":
		return matchStrings(a.op, workStateShort(v.item.GetState()), a.strs)
	case "trusted":
		return a.bools[0] == v.trust.GetTrusted()
	case "label":
		return matchContains(workItemLabels(v.item), a.strs[0])
	case "manual_reason":
		return matchStrings(a.op, workReasonShort(v.row.ManualReason), a.strs)
	}
	return false
}

// workItemSortKey renders one item's order tuple as fixed strings: times
// as zero-padded millis so string order is time order. dispatch_order is
// the 03c rule (oldest hub create_time first); the canonical key breaks
// ties, like every other order.
func workItemSortKey(v workItemView, order []orderTerm) []string {
	key := make([]string, 0, len(order))
	for _, t := range order {
		switch t.field {
		case "provider_update_time":
			key = append(key, fmt.Sprintf("%020d", workProviderUpdateMs(v.item)))
		default:
			key = append(key, fmt.Sprintf("%020d", v.row.CreateMs))
		}
	}
	return key
}

// workItemKeyAfter reports whether the item sorts strictly after (key,
// id) under order, with the canonical mirror key as the final ascending
// tiebreak.
func workItemKeyAfter(v workItemView, order []orderTerm, key []string, id string) bool {
	mine := workItemSortKey(v, order)
	for i, t := range order {
		if mine[i] == key[i] {
			continue
		}
		if t.desc {
			return mine[i] < key[i]
		}
		return mine[i] > key[i]
	}
	return v.row.Key > id
}

func workItemLess(order []orderTerm, a, b workItemView) bool {
	ka, kb := workItemSortKey(a, order), workItemSortKey(b, order)
	for i, t := range order {
		if ka[i] == kb[i] {
			continue
		}
		if t.desc {
			return ka[i] > kb[i]
		}
		return ka[i] < kb[i]
	}
	return a.row.Key < b.row.Key
}

func sortWorkItemViews(views []workItemView, order []orderTerm) {
	sort.Slice(views, func(i, j int) bool { return workItemLess(order, views[i], views[j]) })
}

// workItemPageLife is how long a ListWorkItems page token stays valid
// (02b paging: opaque, valid for 10 min, from the same query).
const workItemPageLife = 10 * time.Minute

// workItemQueryHash binds a page token to the query that minted it: the
// raw filter and order_by plus the page size. A token from another query
// is rejected.
func workItemQueryHash(filter, order string, size int) string {
	sum := sha256.Sum256([]byte(filter + "\x00" + order + "\x00" + fmt.Sprint(size)))
	return hex.EncodeToString(sum[:])
}

// workItemCursor is a decoded ListWorkItems page token.
type workItemCursor struct {
	key []string
	id  string
}

// workItemPageToken is the minted form: version, query hash, sort key,
// id, expiry.
type workItemPageToken struct {
	V   int      `json:"v"`
	Q   string   `json:"q"`
	Key []string `json:"key"`
	ID  string   `json:"id"`
	Exp int64    `json:"exp"`
}

// mintWorkItemToken seals a next-page token: clients treat it as opaque.
func mintWorkItemToken(query string, key []string, id string, now time.Time) string {
	raw, err := json.Marshal(workItemPageToken{
		V: 1, Q: query, Key: key, ID: id, Exp: now.Add(workItemPageLife).Unix(),
	})
	if err != nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(raw)
}

// parseWorkItemToken opens a page token minted by [mintWorkItemToken] for
// the same query and order. Anything else is an error the handler reports
// as PAGE_TOKEN_INVALID. The key length is checked so a forged short key
// can never index past the order tuple.
func parseWorkItemToken(token, want string, keyLen int, now time.Time) (workItemCursor, error) {
	if len(token) > maxFilterBytes {
		return workItemCursor{}, errors.New("api: work item page token over 4 KiB")
	}
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return workItemCursor{}, err
	}
	var pt workItemPageToken
	if err := json.Unmarshal(raw, &pt); err != nil {
		return workItemCursor{}, err
	}
	if pt.V != 1 || pt.Q != want || len(pt.Key) != keyLen || now.Unix() > pt.Exp {
		return workItemCursor{}, errors.New("api: bad work item page token")
	}
	return workItemCursor{key: pt.Key, id: pt.ID}, nil
}
