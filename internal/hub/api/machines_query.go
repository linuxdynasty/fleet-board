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

// This file is the ListMachines query: the 02c field table, filter
// evaluation, ordering, and the query-bound page tokens. The handlers
// and the Machine assembly live in machines.go.

// machineFilterFields is the 02c Machine field table. Every catalogue name
// but harness and is_hub is served: harness filters need the per-machine
// harness inventory, which no row stores yet, and is_hub needs the
// hub-machine link, which the hub keeps nowhere yet.
var machineFilterFields = map[string]filterField{
	"id":   {kind: kindString, ops: map[filterOp]bool{opEq: true, opIn: true}},
	"name": {kind: kindString, ops: map[filterOp]bool{opEq: true, opNe: true}},
	"state": {
		kind: kindEnum, ops: map[filterOp]bool{opEq: true, opNe: true, opIn: true},
		enum: map[string]bool{
			"ENROLLED": true, "ONLINE": true, "STALE": true, "OFFLINE": true, "REVOKED": true,
		},
	},
	"label":    {kind: kindString, repeated: true, ops: map[filterOp]bool{opHas: true}},
	"harness":  {unserved: "the hub stores no per-machine harness inventory yet"},
	"draining": {kind: kindBool, ops: map[filterOp]bool{opEq: true}},
	"is_hub": {
		kind: kindBool, ops: map[filterOp]bool{opEq: true},
		unserved: "the hub keeps no hub-machine link yet",
	},
	"create_time": {kind: kindTime, ops: map[filterOp]bool{
		opEq: true, opNe: true, opLt: true, opLe: true, opGt: true, opGe: true,
	}},
	"update_time": {kind: kindTime, ops: map[filterOp]bool{
		opEq: true, opNe: true, opLt: true, opLe: true, opGt: true, opGe: true,
	}},
}

// machineOrderFields is the 02c Machine order_by catalogue.
var machineOrderFields = map[string]bool{"name": true, "ct": true, "last_seen_time": true}

// defaultMachineOrder is the List order with no order_by: display name
// (documented on ListMachinesRequest).
var defaultMachineOrder = []orderTerm{{field: "name"}}

// authorizeMachines admits the caller for ListMachines and GetMachine (02d:
// READ). Until a later row-12 slice brings caller scopes, that is only the
// local user, who maps to OWNER and covers READ.
func nodeMatchesMachine(n *filterNode, r statedb.MachineRow) bool {
	if n == nil {
		return true
	}
	switch n.op {
	case nodeAtom:
		return atomMatchesMachine(n.atom, r)
	case nodeNot:
		return !nodeMatchesMachine(n.kids[0], r)
	case nodeAnd:
		for _, k := range n.kids {
			if !nodeMatchesMachine(k, r) {
				return false
			}
		}
		return true
	case nodeOr:
		for _, k := range n.kids {
			if nodeMatchesMachine(k, r) {
				return true
			}
		}
		return false
	}
	return false
}

func matchStrings(op filterOp, got string, want []string) bool {
	switch op {
	case opEq:
		return got == want[0]
	case opNe:
		return got != want[0]
	case opIn:
		for _, w := range want {
			if got == w {
				return true
			}
		}
		return false
	default:
		// The parse allows only =, != and in on string fields.
		return false
	}
}

func matchTimes(op filterOp, got int64, want []int64) bool {
	switch op {
	case opEq:
		return got == want[0]
	case opNe:
		return got != want[0]
	case opLt:
		return got < want[0]
	case opLe:
		return got <= want[0]
	case opGt:
		return got > want[0]
	case opGe:
		return got >= want[0]
	default:
		// The parse allows only the comparison operators on times.
		return false
	}
}

// atomMatchesMachine evaluates one comparison. The parse already checked the
// field, the operator and every literal kind, so an unknown field here is
// unreachable and matches nothing.
func atomMatchesMachine(a filterAtom, r statedb.MachineRow) bool {
	switch a.field {
	case "id":
		return matchStrings(a.op, r.MachineID, a.strs)
	case "name":
		return matchStrings(a.op, r.Name, a.strs)
	case "state":
		return matchStrings(a.op, r.State, a.strs)
	case "label":
		for _, l := range r.Labels {
			if l == a.strs[0] {
				return true
			}
		}
		return false
	case "draining":
		return a.bools[0] == (r.Drained || r.AgentDraining)
	case "create_time", "update_time":
		// update_time is create_time: no spec mutation exists yet
		// (UpdateMachine is row 13a), so the version never moves.
		return matchTimes(a.op, r.CreatedMillis, a.times)
	}
	return false
}

// machineSortKey renders one row's order tuple as fixed strings: names
// as-is, times as zero-padded millis so string order is time order.
func machineSortKey(r statedb.MachineRow, order []orderTerm) []string {
	key := make([]string, 0, len(order))
	for _, t := range order {
		switch t.field {
		case "ct":
			key = append(key, fmt.Sprintf("%020d", r.CreatedMillis))
		case "last_seen_time":
			key = append(key, fmt.Sprintf("%020d", r.LastSeenMillis))
		default:
			key = append(key, r.Name)
		}
	}
	return key
}

// machineKeyAfter reports whether row sorts strictly after (key, id) under
// order, with machine id as the final ascending tiebreak.
func machineKeyAfter(r statedb.MachineRow, order []orderTerm, key []string, id string) bool {
	mine := machineSortKey(r, order)
	for i, t := range order {
		if mine[i] == key[i] {
			continue
		}
		if t.desc {
			return mine[i] < key[i]
		}
		return mine[i] > key[i]
	}
	return r.MachineID > id
}

func machineLess(order []orderTerm, a, b statedb.MachineRow) bool {
	ka, kb := machineSortKey(a, order), machineSortKey(b, order)
	for i, t := range order {
		if ka[i] == kb[i] {
			continue
		}
		if t.desc {
			return ka[i] > kb[i]
		}
		return ka[i] < kb[i]
	}
	return a.MachineID < b.MachineID
}

func sortMachineRows(rows []statedb.MachineRow, order []orderTerm) {
	sort.Slice(rows, func(i, j int) bool { return machineLess(order, rows[i], rows[j]) })
}

// machinePageLife is how long a ListMachines page token stays valid (02b
// paging: opaque, valid for 10 min, from the same query).
const machinePageLife = 10 * time.Minute

// machineQueryHash binds a page token to the query that minted it: the raw
// filter and order_by plus the page size. A token from another query is
// rejected.
func machineQueryHash(filter, order string, size int) string {
	sum := sha256.Sum256([]byte(filter + "\x00" + order + "\x00" + fmt.Sprint(size)))
	return hex.EncodeToString(sum[:])
}

// machineCursor is a decoded ListMachines page token.
type machineCursor struct {
	key []string
	id  string
}

// machinePageToken is the minted form: version, query hash, sort key, id,
// expiry.
type machinePageToken struct {
	V   int      `json:"v"`
	Q   string   `json:"q"`
	Key []string `json:"key"`
	ID  string   `json:"id"`
	Exp int64    `json:"exp"`
}

// mintMachineToken seals a next-page token: clients treat it as opaque.
func mintMachineToken(query string, key []string, id string, now time.Time) string {
	raw, err := json.Marshal(machinePageToken{
		V: 1, Q: query, Key: key, ID: id, Exp: now.Add(machinePageLife).Unix(),
	})
	if err != nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(raw)
}

// parseMachineToken opens a page token minted by [mintMachineToken] for the
// same query and order. Anything else is an error the handler reports as
// PAGE_TOKEN_INVALID. The key length is checked so a forged short key can
// never index past the order tuple.
func parseMachineToken(token, want string, keyLen int, now time.Time) (machineCursor, error) {
	if len(token) > maxFilterBytes {
		return machineCursor{}, errors.New("api: machine page token over 4 KiB")
	}
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return machineCursor{}, err
	}
	var pt machinePageToken
	if err := json.Unmarshal(raw, &pt); err != nil {
		return machineCursor{}, err
	}
	if pt.V != 1 || pt.Q != want || len(pt.Key) != keyLen || now.Unix() > pt.Exp {
		return machineCursor{}, errors.New("api: bad machine page token")
	}
	return machineCursor{key: pt.Key, id: pt.ID}, nil
}

// machineState maps the stored connection state to the contract enum. The
// table CHECK holds the set closed, so anything else is the hub's own data
// error.
