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

// This file is the ListAuditEntries query: the 02c field table, filter
// evaluation, ordering, and the query-bound page tokens. The handler and
// the AuditEntry assembly live in audit.go.

// auditFilterFields is the 02c AuditEntry field table. Entries are
// immutable: there is no update_time filter, and order_by takes only ct.
var auditFilterFields = map[string]filterField{
	"actor":   {kind: kindString, ops: map[filterOp]bool{opEq: true, opIn: true}},
	"action":  {kind: kindString, ops: map[filterOp]bool{opEq: true, opNe: true, opIn: true}},
	"target":  {kind: kindString, ops: map[filterOp]bool{opEq: true}},
	"machine": {kind: kindString, ops: map[filterOp]bool{opEq: true, opIn: true}},
	"goal":    {kind: kindString, ops: map[filterOp]bool{opEq: true, opIn: true}},
	"command": {kind: kindString, ops: map[filterOp]bool{opEq: true, opIn: true}},
}

// auditOrderFields is the 02c AuditEntry order_by catalogue.
var auditOrderFields = map[string]bool{"ct": true}

// defaultAuditOrder is the List order with no order_by: newest first.
var defaultAuditOrder = []orderTerm{{field: "ct", desc: true}}

// auditView is one stored row, so the filter and the order tuple read
// the same values the AuditEntry carries.
type auditView struct {
	stored statedb.AuditStored
}

func nodeMatchesAudit(n *filterNode, v auditView) bool {
	if n == nil {
		return true
	}
	switch n.op {
	case nodeAtom:
		return atomMatchesAudit(n.atom, v)
	case nodeNot:
		return !nodeMatchesAudit(n.kids[0], v)
	case nodeAnd:
		for _, k := range n.kids {
			if !nodeMatchesAudit(k, v) {
				return false
			}
		}
		return true
	case nodeOr:
		for _, k := range n.kids {
			if nodeMatchesAudit(k, v) {
				return true
			}
		}
		return false
	}
	return false
}

// atomMatchesAudit evaluates one comparison. The parse already checked
// the field, the operator and every literal kind, so an unknown field
// here is unreachable and matches nothing.
func atomMatchesAudit(a filterAtom, v auditView) bool {
	switch a.field {
	case "actor":
		return matchStrings(a.op, v.stored.ActorID, a.strs)
	case "action":
		return matchStrings(a.op, v.stored.Action, a.strs)
	case "target":
		return matchStrings(a.op, v.stored.Target, a.strs)
	case "machine":
		return matchStrings(a.op, v.stored.MachineID, a.strs)
	case "goal":
		return matchStrings(a.op, v.stored.GoalID, a.strs)
	case "command":
		return matchStrings(a.op, v.stored.CommandID, a.strs)
	}
	return false
}

// auditTieID is the final ascending tiebreak: the row id, so paging is
// stable when two rows share a create millisecond.
func auditTieID(v auditView) string {
	return fmt.Sprintf("%020d", v.stored.ID)
}

// auditSortKey renders one row's order tuple as fixed strings: the
// create time zero-padded so string order is time order.
func auditSortKey(v auditView, order []orderTerm) []string {
	key := make([]string, 0, len(order))
	for _, t := range order {
		if t.field == "ct" {
			key = append(key, fmt.Sprintf("%020d", v.stored.CreateMs))
			continue
		}
		key = append(key, "")
	}
	return key
}

// auditKeyAfter reports whether the row sorts strictly after (key, id)
// under order, with the row id as the final ascending tiebreak.
func auditKeyAfter(v auditView, order []orderTerm, key []string, id string) bool {
	mine := auditSortKey(v, order)
	for i, t := range order {
		if mine[i] == key[i] {
			continue
		}
		if t.desc {
			return mine[i] < key[i]
		}
		return mine[i] > key[i]
	}
	return auditTieID(v) > id
}

func auditLess(order []orderTerm, a, b auditView) bool {
	ka, kb := auditSortKey(a, order), auditSortKey(b, order)
	for i, t := range order {
		if ka[i] == kb[i] {
			continue
		}
		if t.desc {
			return ka[i] > kb[i]
		}
		return ka[i] < kb[i]
	}
	return auditTieID(a) < auditTieID(b)
}

func sortAuditViews(views []auditView, order []orderTerm) {
	sort.Slice(views, func(i, j int) bool { return auditLess(order, views[i], views[j]) })
}

// auditPageLife is how long a ListAuditEntries page token stays valid
// (02b paging: opaque, valid for 10 min, from the same query).
const auditPageLife = 10 * time.Minute

// auditQueryHash binds a page token to the query that minted it: the raw
// filter and order_by plus the page size. A token from another query is
// rejected.
func auditQueryHash(filter, order string, size int) string {
	sum := sha256.Sum256([]byte(filter + "\x00" + order + "\x00" + fmt.Sprint(size)))
	return hex.EncodeToString(sum[:])
}

// auditCursor is a decoded ListAuditEntries page token.
type auditCursor struct {
	key []string
	id  string
}

// auditPageToken is the minted form: version, query hash, sort key, id,
// expiry.
type auditPageToken struct {
	V   int      `json:"v"`
	Q   string   `json:"q"`
	Key []string `json:"key"`
	ID  string   `json:"id"`
	Exp int64    `json:"exp"`
}

// mintAuditToken seals a next-page token: clients treat it as opaque.
func mintAuditToken(query string, key []string, id string, now time.Time) string {
	raw, err := json.Marshal(auditPageToken{
		V: 1, Q: query, Key: key, ID: id, Exp: now.Add(auditPageLife).Unix(),
	})
	if err != nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(raw)
}

// parseAuditToken opens a page token minted by [mintAuditToken] for the
// same query and order. Anything else is an error the handler reports
// as PAGE_TOKEN_INVALID. The key length is checked so a forged short key
// can never index past the order tuple.
func parseAuditToken(token, want string, keyLen int, now time.Time) (auditCursor, error) {
	if len(token) > maxFilterBytes {
		return auditCursor{}, errors.New("api: audit page token over 4 KiB")
	}
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return auditCursor{}, err
	}
	var pt auditPageToken
	if err := json.Unmarshal(raw, &pt); err != nil {
		return auditCursor{}, err
	}
	if pt.V != 1 || pt.Q != want || len(pt.Key) != keyLen || now.Unix() > pt.Exp {
		return auditCursor{}, errors.New("api: bad audit page token")
	}
	return auditCursor{key: pt.Key, id: pt.ID}, nil
}
