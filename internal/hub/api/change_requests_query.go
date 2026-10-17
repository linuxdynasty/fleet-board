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
)

// This file is the ListChangeRequests query: the 02c field table, filter
// evaluation, ordering, and the query-bound page tokens. The handlers
// and the ChangeRequest assembly live in change_requests.go.

// changeRequestFilterFields is the 02c ChangeRequest field table.
// integration, repo and state are served from the mirror row and its stored
// change request, with the common id, create_time and update_time every
// resource filters on; goal and owned need the dispatch rows' goal link,
// which no row stores yet, so they are refused until those rows land.
var changeRequestFilterFields = map[string]filterField{
	"id":          {kind: kindString, ops: map[filterOp]bool{opEq: true, opIn: true}},
	"integration": {kind: kindString, ops: map[filterOp]bool{opEq: true, opIn: true}},
	"repo":        {kind: kindString, ops: map[filterOp]bool{opEq: true, opIn: true}},
	"state":       {kind: kindEnum, ops: map[filterOp]bool{opEq: true, opIn: true}, enum: changeRequestStates},
	"goal":        {unserved: "the hub stores no change-request goal link yet"},
	"owned":       {unserved: "the hub stores no change-request goal link yet"},
	"create_time": {kind: kindTime, ops: map[filterOp]bool{
		opEq: true, opNe: true, opLt: true, opLe: true, opGt: true, opGe: true,
	}},
	"update_time": {kind: kindTime, ops: map[filterOp]bool{
		opEq: true, opNe: true, opLt: true, opLe: true, opGt: true, opGe: true,
	}},
}

// changeRequestStates is the 02c WorkItemState short-name set, shared with
// the WorkItem state filter.
var changeRequestStates = map[string]bool{
	"UNSPECIFIED": true, "OPEN": true, "IN_PROGRESS": true, "CLOSED": true,
	"MERGED": true, "DRAFT": true, "CANCELLED": true,
}

// changeRequestOrderFields is the 02c ChangeRequest order_by catalogue.
var changeRequestOrderFields = map[string]bool{
	"ct": true, "ut": true, "provider_update_time": true,
}

// defaultChangeRequestOrder is the List order with no order_by: oldest
// first sight first.
var defaultChangeRequestOrder = []orderTerm{{field: "ct"}}

// changeStateShort renders the stored provider state as its 02c short name.
func changeStateShort(state string) string {
	return strings.TrimPrefix(state, "WORK_ITEM_STATE_")
}

// changeRequestRepoRef renders the change request's repo as host/path.
func changeRequestRepoOf(host, path string) string {
	return host + "/" + path
}

func nodeMatchesChangeRequest(n *filterNode, v changeRequestView) bool {
	if n == nil {
		return true
	}
	switch n.op {
	case nodeAtom:
		return atomMatchesChangeRequest(n.atom, v)
	case nodeNot:
		return !nodeMatchesChangeRequest(n.kids[0], v)
	case nodeAnd:
		for _, k := range n.kids {
			if !nodeMatchesChangeRequest(k, v) {
				return false
			}
		}
		return true
	case nodeOr:
		for _, k := range n.kids {
			if nodeMatchesChangeRequest(k, v) {
				return true
			}
		}
		return false
	}
	return false
}

// atomMatchesChangeRequest evaluates one comparison. The parse already
// checked the field, the operator and every literal kind, so an unknown
// field here is unreachable and matches nothing.
func atomMatchesChangeRequest(a filterAtom, v changeRequestView) bool {
	switch a.field {
	case "id":
		return matchStrings(a.op, v.row.Key, a.strs)
	case "integration":
		return matchStrings(a.op, v.row.Integration, a.strs)
	case "repo":
		return matchStrings(a.op, changeRequestRepoOf(v.cr.GetRepo().GetHost(), v.cr.GetRepo().GetPath()), a.strs)
	case "state":
		return matchStrings(a.op, changeStateShort(v.cr.GetState().String()), a.strs)
	case "create_time":
		return matchTimes(a.op, v.row.CreateMs, a.times)
	case "update_time":
		// update_time reads as create_time until a later row stores the
		// hub-change stamp.
		return matchTimes(a.op, v.row.CreateMs, a.times)
	}
	return false
}

// changeRequestSortKey renders one change request's order tuple as fixed
// strings: times as zero-padded millis so string order is time order. The
// canonical key breaks ties, like every other order.
func changeRequestSortKey(v changeRequestView, order []orderTerm) []string {
	key := make([]string, 0, len(order))
	for _, t := range order {
		switch t.field {
		case "provider_update_time":
			ms := int64(0)
			if v.cr.GetUpdateTime() != nil {
				ms = v.cr.GetUpdateTime().AsTime().UnixMilli()
			}
			key = append(key, fmt.Sprintf("%020d", ms))
		default:
			key = append(key, fmt.Sprintf("%020d", v.row.CreateMs))
		}
	}
	return key
}

// changeRequestKeyAfter reports whether the change request sorts strictly
// after (key, id) under order, with the canonical mirror key as the final
// ascending tiebreak.
func changeRequestKeyAfter(v changeRequestView, order []orderTerm, key []string, id string) bool {
	mine := changeRequestSortKey(v, order)
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

func changeRequestLess(order []orderTerm, a, b changeRequestView) bool {
	ka, kb := changeRequestSortKey(a, order), changeRequestSortKey(b, order)
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

func sortChangeRequestViews(views []changeRequestView, order []orderTerm) {
	sort.Slice(views, func(i, j int) bool { return changeRequestLess(order, views[i], views[j]) })
}

// changeRequestPageLife is how long a ListChangeRequests page token stays
// valid (02b paging: opaque, valid for 10 min, from the same query).
const changeRequestPageLife = 10 * time.Minute

// changeRequestQueryHash binds a page token to the query that minted it:
// the raw filter and order_by plus the page size. A token from another
// query is rejected.
func changeRequestQueryHash(filter, order string, size int) string {
	sum := sha256.Sum256([]byte(filter + "\x00" + order + "\x00" + fmt.Sprint(size)))
	return hex.EncodeToString(sum[:])
}

// changeRequestCursor is a decoded ListChangeRequests page token.
type changeRequestCursor struct {
	key []string
	id  string
}

// changeRequestPageToken is the minted form: version, query hash, sort
// key, id, expiry.
type changeRequestPageToken struct {
	V   int      `json:"v"`
	Q   string   `json:"q"`
	Key []string `json:"key"`
	ID  string   `json:"id"`
	Exp int64    `json:"exp"`
}

// mintChangeRequestToken seals a next-page token: clients treat it as opaque.
func mintChangeRequestToken(query string, key []string, id string, now time.Time) string {
	raw, err := json.Marshal(changeRequestPageToken{
		V: 1, Q: query, Key: key, ID: id, Exp: now.Add(changeRequestPageLife).Unix(),
	})
	if err != nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(raw)
}

// parseChangeRequestToken opens a page token minted by
// [mintChangeRequestToken] for the same query and order. Anything else is
// an error the handler reports as PAGE_TOKEN_INVALID. The key length is
// checked so a forged short key can never index past the order tuple.
func parseChangeRequestToken(token, want string, keyLen int, now time.Time) (changeRequestCursor, error) {
	if len(token) > maxFilterBytes {
		return changeRequestCursor{}, errors.New("api: change request page token over 4 KiB")
	}
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return changeRequestCursor{}, err
	}
	var pt changeRequestPageToken
	if err := json.Unmarshal(raw, &pt); err != nil {
		return changeRequestCursor{}, err
	}
	if pt.V != 1 || pt.Q != want || len(pt.Key) != keyLen || now.Unix() > pt.Exp {
		return changeRequestCursor{}, errors.New("api: bad change request page token")
	}
	return changeRequestCursor{key: pt.Key, id: pt.ID}, nil
}
