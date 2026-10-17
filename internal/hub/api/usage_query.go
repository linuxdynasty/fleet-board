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

// This file is the ListUsageReadings query: the 02c field table, filter
// evaluation, ordering, and the query-bound page tokens. The handler and
// the UsageReading assembly live in usage.go.

// usageFilterFields is the 02c UsageReading field table: the hub-adjusted
// read_time and the source. Readings have no id, create_time or
// update_time, and the account scope arrives as the typed account field,
// not the filter.
var usageFilterFields = map[string]filterField{
	"read_time": {kind: kindTime, ops: map[filterOp]bool{
		opEq: true, opNe: true, opLt: true, opLe: true, opGt: true, opGe: true,
	}},
	"source": {kind: kindEnum, ops: map[filterOp]bool{opEq: true, opIn: true}, enum: usageSources},
}

// usageSources is the 02c UsageSource short-name set, every value the
// contract names.
var usageSources = map[string]bool{
	"PROBE": true, "PASSIVE": true, "IDENTITY_PROBE": true,
}

// usageOrderFields is the 02c UsageReading order_by catalogue.
var usageOrderFields = map[string]bool{"read_time": true}

// defaultUsageOrder is the List order with no order_by: newest first.
var defaultUsageOrder = []orderTerm{{field: "read_time", desc: true}}

// usageView is one stored reading, so the filter and the order tuple
// read the same values the UsageReading carries.
type usageView struct {
	stored statedb.UsageReadingStored
}

// usageTieID is the final ascending tiebreak: the account with the two
// times that key the row, so paging is stable when two readings share a
// read millisecond.
func usageTieID(v usageView) string {
	return v.stored.AccountID + "\x00" + fmt.Sprintf("%020d/%020d", v.stored.ReadMs, v.stored.ReceivedMs)
}

func nodeMatchesUsage(n *filterNode, v usageView) bool {
	if n == nil {
		return true
	}
	switch n.op {
	case nodeAtom:
		return atomMatchesUsage(n.atom, v)
	case nodeNot:
		return !nodeMatchesUsage(n.kids[0], v)
	case nodeAnd:
		for _, k := range n.kids {
			if !nodeMatchesUsage(k, v) {
				return false
			}
		}
		return true
	case nodeOr:
		for _, k := range n.kids {
			if nodeMatchesUsage(k, v) {
				return true
			}
		}
		return false
	}
	return false
}

// atomMatchesUsage evaluates one comparison. The parse already checked
// the field, the operator and every literal kind, so an unknown field
// here is unreachable and matches nothing.
func atomMatchesUsage(a filterAtom, v usageView) bool {
	switch a.field {
	case "read_time":
		return matchTimes(a.op, v.stored.ReadMs, a.times)
	case "source":
		return matchStrings(a.op, v.stored.Source, a.strs)
	}
	return false
}

// usageSortKey renders one reading's order tuple as fixed strings: the
// hub-adjusted read time zero-padded so string order is time order,
// with the hub receive time second so equal read times page stably.
func usageSortKey(v usageView, order []orderTerm) []string {
	key := make([]string, 0, len(order))
	for _, t := range order {
		if t.field == "read_time" {
			key = append(key, fmt.Sprintf("%020d/%020d", v.stored.ReadMs, v.stored.ReceivedMs))
			continue
		}
		key = append(key, "")
	}
	return key
}

// usageKeyAfter reports whether the reading sorts strictly after (key,
// id) under order, with the account and times as the final ascending
// tiebreak.
func usageKeyAfter(v usageView, order []orderTerm, key []string, id string) bool {
	mine := usageSortKey(v, order)
	for i, t := range order {
		if mine[i] == key[i] {
			continue
		}
		if t.desc {
			return mine[i] < key[i]
		}
		return mine[i] > key[i]
	}
	return usageTieID(v) > id
}

func usageLess(order []orderTerm, a, b usageView) bool {
	ka, kb := usageSortKey(a, order), usageSortKey(b, order)
	for i, t := range order {
		if ka[i] == kb[i] {
			continue
		}
		if t.desc {
			return ka[i] > kb[i]
		}
		return ka[i] < kb[i]
	}
	return usageTieID(a) < usageTieID(b)
}

func sortUsageViews(views []usageView, order []orderTerm) {
	sort.Slice(views, func(i, j int) bool { return usageLess(order, views[i], views[j]) })
}

// usagePageLife is how long a ListUsageReadings page token stays valid
// (02b paging: opaque, valid for 10 min, from the same query).
const usagePageLife = 10 * time.Minute

// usageQueryHash binds a page token to the query that minted it: the raw
// filter and order_by, the typed account scope, plus the page size. A
// token from another query is rejected.
func usageQueryHash(filter, order, account string, size int) string {
	sum := sha256.Sum256([]byte(filter + "\x00" + order + "\x00" + account + "\x00" + fmt.Sprint(size)))
	return hex.EncodeToString(sum[:])
}

// usageCursor is a decoded ListUsageReadings page token.
type usageCursor struct {
	key []string
	id  string
}

// usagePageToken is the minted form: version, query hash, sort key, id,
// expiry.
type usagePageToken struct {
	V   int      `json:"v"`
	Q   string   `json:"q"`
	Key []string `json:"key"`
	ID  string   `json:"id"`
	Exp int64    `json:"exp"`
}

// mintUsageToken seals a next-page token: clients treat it as opaque.
func mintUsageToken(query string, key []string, id string, now time.Time) string {
	raw, err := json.Marshal(usagePageToken{
		V: 1, Q: query, Key: key, ID: id, Exp: now.Add(usagePageLife).Unix(),
	})
	if err != nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(raw)
}

// parseUsageToken opens a page token minted by [mintUsageToken] for the
// same query and order. Anything else is an error the handler reports
// as PAGE_TOKEN_INVALID. The key length is checked so a forged short key
// can never index past the order tuple.
func parseUsageToken(token, want string, keyLen int, now time.Time) (usageCursor, error) {
	if len(token) > maxFilterBytes {
		return usageCursor{}, errors.New("api: usage page token over 4 KiB")
	}
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return usageCursor{}, err
	}
	var pt usagePageToken
	if err := json.Unmarshal(raw, &pt); err != nil {
		return usageCursor{}, err
	}
	if pt.V != 1 || pt.Q != want || len(pt.Key) != keyLen || now.Unix() > pt.Exp {
		return usageCursor{}, errors.New("api: bad usage page token")
	}
	return usageCursor{key: pt.Key, id: pt.ID}, nil
}
