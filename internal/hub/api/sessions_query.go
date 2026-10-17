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

// This file is the ListSessions query: the 02c field table, filter
// evaluation, ordering, and the query-bound page tokens. The handlers
// and the Session assembly live in sessions.go.

// sessionFilterFields is the 02c Session field table. machine, name,
// started_by_fleet and state are served from the hub_sessions row;
// harness, goal and available_for_work need the pane mirror, which no row
// stores yet, so they are refused until it lands.
var sessionFilterFields = map[string]filterField{
	"machine":            {kind: kindString, ops: map[filterOp]bool{opEq: true, opNe: true, opIn: true}},
	"name":               {kind: kindString, ops: map[filterOp]bool{opEq: true, opNe: true}},
	"started_by_fleet":   {kind: kindBool, ops: map[filterOp]bool{opEq: true}},
	"harness":            {unserved: "the hub stores no pane mirror yet"},
	"goal":               {unserved: "the hub stores no pane mirror yet"},
	"available_for_work": {unserved: "the hub stores no pane mirror yet"},
	"state": {
		kind: kindEnum, ops: map[filterOp]bool{opEq: true, opIn: true},
		enum: map[string]bool{"LIVE": true, "ENDED": true},
	},
}

// sessionOrderFields is the 02c Session order_by catalogue.
var sessionOrderFields = map[string]bool{"name": true, "ct": true}

// defaultSessionOrder is the List order with no order_by: session name.
var defaultSessionOrder = []orderTerm{{field: "name"}}

func nodeMatchesSession(n *filterNode, r statedb.SessionRow) bool {
	if n == nil {
		return true
	}
	switch n.op {
	case nodeAtom:
		return atomMatchesSession(n.atom, r)
	case nodeNot:
		return !nodeMatchesSession(n.kids[0], r)
	case nodeAnd:
		for _, k := range n.kids {
			if !nodeMatchesSession(k, r) {
				return false
			}
		}
		return true
	case nodeOr:
		for _, k := range n.kids {
			if nodeMatchesSession(k, r) {
				return true
			}
		}
		return false
	}
	return false
}

// atomMatchesSession evaluates one comparison. The parse already checked
// the field, the operator and every literal kind, so an unknown field here
// is unreachable and matches nothing. Every stored row is a hub-started
// LIVE session: started_by_fleet reads true and state reads LIVE until the
// pane mirror lands and learns ends.
func atomMatchesSession(a filterAtom, r statedb.SessionRow) bool {
	switch a.field {
	case "machine":
		return matchStrings(a.op, r.MachineID, a.strs)
	case "name":
		return matchStrings(a.op, r.Name, a.strs)
	case "started_by_fleet":
		return a.bools[0]
	case "state":
		return matchStrings(a.op, "LIVE", a.strs)
	}
	return false
}

// sessionSortKey renders one row's order tuple as fixed strings: names
// as-is, times as zero-padded millis so string order is time order.
func sessionSortKey(r statedb.SessionRow, order []orderTerm) []string {
	key := make([]string, 0, len(order))
	for _, t := range order {
		if t.field == "ct" {
			key = append(key, fmt.Sprintf("%020d", r.CreatedMillis))
			continue
		}
		key = append(key, r.Name)
	}
	return key
}

// sessionKeyAfter reports whether row sorts strictly after (key, id) under
// order, with session id as the final ascending tiebreak.
func sessionKeyAfter(r statedb.SessionRow, order []orderTerm, key []string, id string) bool {
	mine := sessionSortKey(r, order)
	for i, t := range order {
		if mine[i] == key[i] {
			continue
		}
		if t.desc {
			return mine[i] < key[i]
		}
		return mine[i] > key[i]
	}
	return r.SessionID > id
}

func sessionLess(order []orderTerm, a, b statedb.SessionRow) bool {
	ka, kb := sessionSortKey(a, order), sessionSortKey(b, order)
	for i, t := range order {
		if ka[i] == kb[i] {
			continue
		}
		if t.desc {
			return ka[i] > kb[i]
		}
		return ka[i] < kb[i]
	}
	return a.SessionID < b.SessionID
}

func sortSessionRows(rows []statedb.SessionRow, order []orderTerm) {
	sort.Slice(rows, func(i, j int) bool { return sessionLess(order, rows[i], rows[j]) })
}

// sessionPageLife is how long a ListSessions page token stays valid (02b
// paging: opaque, valid for 10 min, from the same query).
const sessionPageLife = 10 * time.Minute

// sessionQueryHash binds a page token to the query that minted it: the raw
// filter and order_by plus the page size. A token from another query is
// rejected.
func sessionQueryHash(filter, order string, size int) string {
	sum := sha256.Sum256([]byte(filter + "\x00" + order + "\x00" + fmt.Sprint(size)))
	return hex.EncodeToString(sum[:])
}

// sessionCursor is a decoded ListSessions page token.
type sessionCursor struct {
	key []string
	id  string
}

// sessionPageToken is the minted form: version, query hash, sort key, id,
// expiry.
type sessionPageToken struct {
	V   int      `json:"v"`
	Q   string   `json:"q"`
	Key []string `json:"key"`
	ID  string   `json:"id"`
	Exp int64    `json:"exp"`
}

// mintSessionToken seals a next-page token: clients treat it as opaque.
func mintSessionToken(query string, key []string, id string, now time.Time) string {
	raw, err := json.Marshal(sessionPageToken{
		V: 1, Q: query, Key: key, ID: id, Exp: now.Add(sessionPageLife).Unix(),
	})
	if err != nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(raw)
}

// parseSessionToken opens a page token minted by [mintSessionToken] for the
// same query and order. Anything else is an error the handler reports as
// PAGE_TOKEN_INVALID. The key length is checked so a forged short key can
// never index past the order tuple.
func parseSessionToken(token, want string, keyLen int, now time.Time) (sessionCursor, error) {
	if len(token) > maxFilterBytes {
		return sessionCursor{}, errors.New("api: session page token over 4 KiB")
	}
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return sessionCursor{}, err
	}
	var pt sessionPageToken
	if err := json.Unmarshal(raw, &pt); err != nil {
		return sessionCursor{}, err
	}
	if pt.V != 1 || pt.Q != want || len(pt.Key) != keyLen || now.Unix() > pt.Exp {
		return sessionCursor{}, errors.New("api: bad session page token")
	}
	return sessionCursor{key: pt.Key, id: pt.ID}, nil
}
