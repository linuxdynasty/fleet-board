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

// This file is the ListIntegrations query: the 02c field table, filter
// evaluation, ordering, and the query-bound page tokens. The handler and
// the Integration assembly live in integrations.go.

// integrationFilterFields is the 02c Integration field table. The id and
// the times read the stored row; health and provider read the syncer's
// status passthrough. Fleets hold a handful of integrations, so there is
// no per-field SQL: the handler filters this snapshot in memory.
var integrationFilterFields = map[string]filterField{
	"id":       {kind: kindString, ops: map[filterOp]bool{opEq: true, opIn: true}},
	"health":   {kind: kindEnum, ops: map[filterOp]bool{opEq: true, opIn: true}, enum: integrationHealths},
	"provider": {kind: kindString, ops: map[filterOp]bool{opEq: true, opIn: true}},
	"create_time": {kind: kindTime, ops: map[filterOp]bool{
		opEq: true, opNe: true, opLt: true, opLe: true, opGt: true, opGe: true,
	}},
	"update_time": {kind: kindTime, ops: map[filterOp]bool{
		opEq: true, opNe: true, opLt: true, opLe: true, opGt: true, opGe: true,
	}},
}

// integrationHealths is the 02c IntegrationHealth short-name set, every
// value the contract names.
var integrationHealths = map[string]bool{
	"UNSPECIFIED": true, "OK": true, "DEGRADED": true,
	"DISABLED": true, "BACKING_OFF": true,
}

// integrationOrderFields is the 02c Integration order_by catalogue.
var integrationOrderFields = map[string]bool{"name": true, "ct": true}

// defaultIntegrationOrder is the List order with no order_by: id.
var defaultIntegrationOrder = []orderTerm{{field: "name"}}

// integrationView is one stored integration with its decoded spec and
// status, so the filter and the order tuple read the same values the
// Integration carries.
type integrationView struct {
	stored statedb.IntegrationStored
	spec   *hubv1.IntegrationSpec
	status *hubv1.IntegrationStatus
}

// integrationHealthShort renders the stored health as its 02c short name.
func integrationHealthShort(h hubv1.IntegrationHealth) string {
	return strings.TrimPrefix(h.String(), "INTEGRATION_HEALTH_")
}

func nodeMatchesIntegration(n *filterNode, v integrationView) bool {
	if n == nil {
		return true
	}
	switch n.op {
	case nodeAtom:
		return atomMatchesIntegration(n.atom, v)
	case nodeNot:
		return !nodeMatchesIntegration(n.kids[0], v)
	case nodeAnd:
		for _, k := range n.kids {
			if !nodeMatchesIntegration(k, v) {
				return false
			}
		}
		return true
	case nodeOr:
		for _, k := range n.kids {
			if nodeMatchesIntegration(k, v) {
				return true
			}
		}
		return false
	}
	return false
}

// atomMatchesIntegration evaluates one comparison. The parse already
// checked the field, the operator and every literal kind, so an unknown
// field here is unreachable and matches nothing.
func atomMatchesIntegration(a filterAtom, v integrationView) bool {
	switch a.field {
	case "id":
		return matchStrings(a.op, v.stored.ID, a.strs)
	case "health":
		return matchStrings(a.op, integrationHealthShort(v.status.GetHealth()), a.strs)
	case "provider":
		return matchStrings(a.op, v.status.GetProviderName(), a.strs)
	case "create_time":
		return matchTimes(a.op, v.stored.CreateMs, a.times)
	case "update_time":
		return matchTimes(a.op, v.stored.UpdateMs, a.times)
	}
	return false
}

// integrationSortKey renders one integration's order tuple as fixed
// strings: the id as-is, the create time zero-padded so string order is
// time order. The id breaks ties, like every other order.
func integrationSortKey(v integrationView, order []orderTerm) []string {
	key := make([]string, 0, len(order))
	for _, t := range order {
		switch t.field {
		case "ct":
			key = append(key, fmt.Sprintf("%020d", v.stored.CreateMs))
		default:
			key = append(key, v.stored.ID)
		}
	}
	return key
}

// integrationKeyAfter reports whether the integration sorts strictly
// after (key, id) under order, with the id as the final ascending
// tiebreak.
func integrationKeyAfter(v integrationView, order []orderTerm, key []string, id string) bool {
	mine := integrationSortKey(v, order)
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

func integrationLess(order []orderTerm, a, b integrationView) bool {
	ka, kb := integrationSortKey(a, order), integrationSortKey(b, order)
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

func sortIntegrationViews(views []integrationView, order []orderTerm) {
	sort.Slice(views, func(i, j int) bool { return integrationLess(order, views[i], views[j]) })
}

// integrationPageLife is how long a ListIntegrations page token stays
// valid (02b paging: opaque, valid for 10 min, from the same query).
const integrationPageLife = 10 * time.Minute

// integrationQueryHash binds a page token to the query that minted it:
// the raw filter and order_by plus the page size. A token from another
// query is rejected.
func integrationQueryHash(filter, order string, size int) string {
	sum := sha256.Sum256([]byte(filter + "\x00" + order + "\x00" + fmt.Sprint(size)))
	return hex.EncodeToString(sum[:])
}

// integrationCursor is a decoded ListIntegrations page token.
type integrationCursor struct {
	key []string
	id  string
}

// integrationPageToken is the minted form: version, query hash, sort key,
// id, expiry.
type integrationPageToken struct {
	V   int      `json:"v"`
	Q   string   `json:"q"`
	Key []string `json:"key"`
	ID  string   `json:"id"`
	Exp int64    `json:"exp"`
}

// mintIntegrationToken seals a next-page token: clients treat it as opaque.
func mintIntegrationToken(query string, key []string, id string, now time.Time) string {
	raw, err := json.Marshal(integrationPageToken{
		V: 1, Q: query, Key: key, ID: id, Exp: now.Add(integrationPageLife).Unix(),
	})
	if err != nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(raw)
}

// parseIntegrationToken opens a page token minted by
// [mintIntegrationToken] for the same query and order. Anything else is
// an error the handler reports as PAGE_TOKEN_INVALID. The key length is
// checked so a forged short key can never index past the order tuple.
func parseIntegrationToken(token, want string, keyLen int, now time.Time) (integrationCursor, error) {
	if len(token) > maxFilterBytes {
		return integrationCursor{}, errors.New("api: integration page token over 4 KiB")
	}
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return integrationCursor{}, err
	}
	var pt integrationPageToken
	if err := json.Unmarshal(raw, &pt); err != nil {
		return integrationCursor{}, err
	}
	if pt.V != 1 || pt.Q != want || len(pt.Key) != keyLen || now.Unix() > pt.Exp {
		return integrationCursor{}, errors.New("api: bad integration page token")
	}
	return integrationCursor{key: pt.Key, id: pt.ID}, nil
}
