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
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

// This file is the ListAccounts query: the 02c field table, filter
// evaluation, ordering, and the query-bound page tokens. The handlers
// and the Account assembly live in accounts.go.

// accountFilterFields is the 02c Account field table. Harness, provider
// and origin read the stored row the owner wrote; mode and budget_kind
// read the stored spec. Pacing_state needs the scheduler's pacing,
// which no row stores yet, so it is refused until that row lands.
var accountFilterFields = map[string]filterField{
	"id":           {kind: kindString, ops: map[filterOp]bool{opEq: true, opIn: true}},
	"harness":      {kind: kindString, ops: map[filterOp]bool{opEq: true, opIn: true}},
	"mode":         {kind: kindEnum, ops: map[filterOp]bool{opEq: true, opIn: true}, enum: accountModes},
	"budget_kind":  {kind: kindEnum, ops: map[filterOp]bool{opEq: true}, enum: accountBudgetKinds},
	"pacing_state": {unserved: "the hub stores no account pacing yet"},
	"provider":     {kind: kindString, ops: map[filterOp]bool{opEq: true, opIn: true}},
	"origin":       {kind: kindEnum, ops: map[filterOp]bool{opEq: true}, enum: accountOrigins},
	"create_time": {kind: kindTime, ops: map[filterOp]bool{
		opEq: true, opNe: true, opLt: true, opLe: true, opGt: true, opGe: true,
	}},
	"update_time": {kind: kindTime, ops: map[filterOp]bool{
		opEq: true, opNe: true, opLt: true, opLe: true, opGt: true, opGe: true,
	}},
}

// accountModes is the 02c AccountMode short-name set, every value the
// contract names.
var accountModes = map[string]bool{
	"UNSPECIFIED": true, "FLEET": true, "INTERACTIVE_ONLY": true,
}

// accountBudgetKinds is the 02c BudgetKind short-name set, every value
// the contract names.
var accountBudgetKinds = map[string]bool{
	"UNSPECIFIED": true, "SUBSCRIPTION_PERCENT": true, "DOLLAR_CAP": true,
}

// accountOrigins is the 02c AccountOrigin short-name set, every value
// the contract names.
var accountOrigins = map[string]bool{
	"UNSPECIFIED": true, "DISCOVERED": true, "OWNER_CREATED": true,
}

// accountOrderFields is the 02c Account order_by catalogue.
var accountOrderFields = map[string]bool{"name": true, "rank": true}

// defaultAccountOrder is the List order with no order_by: display name.
var defaultAccountOrder = []orderTerm{{field: "name"}}

// accountView is one stored account with its decoded spec, so the filter
// and the order tuple read the same values the Account carries.
type accountView struct {
	stored statedb.AccountStored
	spec   *hubv1.AccountSpec
}

// accountModeShort renders the stored mode as its 02c short name.
func accountModeShort(m hubv1.AccountMode) string {
	return strings.TrimPrefix(m.String(), "ACCOUNT_MODE_")
}

// accountBudgetKindShort renders the stored budget kind as its 02c short
// name.
func accountBudgetKindShort(k typev1.BudgetKind) string {
	return strings.TrimPrefix(k.String(), "BUDGET_KIND_")
}

func nodeMatchesAccount(n *filterNode, v accountView) bool {
	if n == nil {
		return true
	}
	switch n.op {
	case nodeAtom:
		return atomMatchesAccount(n.atom, v)
	case nodeNot:
		return !nodeMatchesAccount(n.kids[0], v)
	case nodeAnd:
		for _, k := range n.kids {
			if !nodeMatchesAccount(k, v) {
				return false
			}
		}
		return true
	case nodeOr:
		for _, k := range n.kids {
			if nodeMatchesAccount(k, v) {
				return true
			}
		}
		return false
	}
	return false
}

// atomMatchesAccount evaluates one comparison. The parse already checked
// the field, the operator and every literal kind, so an unknown field
// here is unreachable and matches nothing.
func atomMatchesAccount(a filterAtom, v accountView) bool {
	switch a.field {
	case "id":
		return matchStrings(a.op, v.stored.ID, a.strs)
	case "harness":
		return matchStrings(a.op, v.stored.Harness, a.strs)
	case "mode":
		return matchStrings(a.op, accountModeShort(v.spec.GetMode()), a.strs)
	case "budget_kind":
		return matchStrings(a.op, accountBudgetKindShort(v.spec.GetBudgetKind()), a.strs)
	case "provider":
		return matchStrings(a.op, v.stored.Provider, a.strs)
	case "origin":
		return matchStrings(a.op, v.stored.Origin, a.strs)
	case "create_time":
		return matchTimes(a.op, v.stored.CreateMs, a.times)
	case "update_time":
		return matchTimes(a.op, v.stored.UpdateMs, a.times)
	}
	return false
}

// accountSortKey renders one account's order tuple as fixed strings: the
// display name as-is, the pacing rank zero-padded so numeric order is
// string order. The rank is 0 for every account until the scheduler row
// writes pacing. The canonical id breaks ties, like every other order.
func accountSortKey(v accountView, order []orderTerm) []string {
	key := make([]string, 0, len(order))
	for _, t := range order {
		switch t.field {
		case "rank":
			// Pacing is unset until the scheduler row writes it, so
			// every account ranks 0 and the id tiebreak decides.
			key = append(key, fmt.Sprintf("%010d", 0))
		default:
			key = append(key, v.spec.GetDisplayName())
		}
	}
	return key
}

// accountKeyAfter reports whether the account sorts strictly after (key,
// id) under order, with the canonical id as the final ascending
// tiebreak.
func accountKeyAfter(v accountView, order []orderTerm, key []string, id string) bool {
	mine := accountSortKey(v, order)
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

func accountLess(order []orderTerm, a, b accountView) bool {
	ka, kb := accountSortKey(a, order), accountSortKey(b, order)
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

func sortAccountViews(views []accountView, order []orderTerm) {
	sort.Slice(views, func(i, j int) bool { return accountLess(order, views[i], views[j]) })
}

// accountPageLife is how long a ListAccounts page token stays valid (02b
// paging: opaque, valid for 10 min, from the same query).
const accountPageLife = 10 * time.Minute

// accountQueryHash binds a page token to the query that minted it: the
// raw filter and order_by plus the page size. A token from another query
// is rejected.
func accountQueryHash(filter, order string, size int) string {
	sum := sha256.Sum256([]byte(filter + "\x00" + order + "\x00" + fmt.Sprint(size)))
	return hex.EncodeToString(sum[:])
}

// accountCursor is a decoded ListAccounts page token.
type accountCursor struct {
	key []string
	id  string
}

// accountPageToken is the minted form: version, query hash, sort key,
// id, expiry.
type accountPageToken struct {
	V   int      `json:"v"`
	Q   string   `json:"q"`
	Key []string `json:"key"`
	ID  string   `json:"id"`
	Exp int64    `json:"exp"`
}

// mintAccountToken seals a next-page token: clients treat it as opaque.
func mintAccountToken(query string, key []string, id string, now time.Time) string {
	raw, err := json.Marshal(accountPageToken{
		V: 1, Q: query, Key: key, ID: id, Exp: now.Add(accountPageLife).Unix(),
	})
	if err != nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(raw)
}

// parseAccountToken opens a page token minted by [mintAccountToken] for
// the same query and order. Anything else is an error the handler
// reports as PAGE_TOKEN_INVALID. The key length is checked so a forged
// short key can never index past the order tuple.
func parseAccountToken(token, want string, keyLen int, now time.Time) (accountCursor, error) {
	if len(token) > maxFilterBytes {
		return accountCursor{}, errors.New("api: account page token over 4 KiB")
	}
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return accountCursor{}, err
	}
	var pt accountPageToken
	if err := json.Unmarshal(raw, &pt); err != nil {
		return accountCursor{}, err
	}
	if pt.V != 1 || pt.Q != want || len(pt.Key) != keyLen || now.Unix() > pt.Exp {
		return accountCursor{}, errors.New("api: bad account page token")
	}
	return accountCursor{key: pt.Key, id: pt.ID}, nil
}
