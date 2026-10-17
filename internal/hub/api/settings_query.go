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

// This file is the ListSettings query: the 02c field table, filter
// evaluation, ordering, and the query-bound page tokens. The handlers
// and the Settings assembly live in settings.go.

// settingsFilterFields is the 02c Settings field table. The layer is
// derived from the id: "fleet" is the fleet defaults, "group" a
// group/<label> layer and "machine" a machine/<id> layer. Fleets hold
// a handful of layers, so there is no per-field SQL: the handler
// filters this snapshot in memory.
var settingsFilterFields = map[string]filterField{
	"layer": {kind: kindString, ops: map[filterOp]bool{opEq: true, opIn: true}},
}

// settingsOrderFields is the 02c Settings order_by catalogue.
var settingsOrderFields = map[string]bool{"name": true, "ct": true}

// defaultSettingsOrder is the List order with no order_by: id.
var defaultSettingsOrder = []orderTerm{{field: "name"}}

// settingsView is one stored layer with its decoded spec, so the
// filter and the order tuple read the same values the Settings
// carries.
type settingsView struct {
	stored statedb.SettingsLayerStored
	spec   *hubv1.SettingsSpec
}

// settingsLayerOf renders a stored scope key as its 02c layer name.
func settingsLayerOf(scope string) string {
	switch {
	case scope == statedb.FleetScope:
		return "fleet"
	case strings.HasPrefix(scope, "group/"):
		return "group"
	default:
		return "machine"
	}
}

func nodeMatchesSettings(n *filterNode, v settingsView) bool {
	if n == nil {
		return true
	}
	switch n.op {
	case nodeAtom:
		return atomMatchesSettings(n.atom, v)
	case nodeNot:
		return !nodeMatchesSettings(n.kids[0], v)
	case nodeAnd:
		for _, k := range n.kids {
			if !nodeMatchesSettings(k, v) {
				return false
			}
		}
		return true
	case nodeOr:
		for _, k := range n.kids {
			if nodeMatchesSettings(k, v) {
				return true
			}
		}
		return false
	}
	return false
}

// atomMatchesSettings evaluates one comparison. The parse already
// checked the field, the operator and every literal kind, so an unknown
// field here is unreachable and matches nothing.
func atomMatchesSettings(a filterAtom, v settingsView) bool {
	switch a.field {
	case "layer":
		return matchStrings(a.op, settingsLayerOf(v.stored.Scope), a.strs)
	}
	return false
}

// settingsSortKey renders one layer's order tuple as fixed strings:
// the scope key as-is, the create time zero-padded so string order is
// time order. The id breaks ties, like every other order.
func settingsSortKey(v settingsView, order []orderTerm) []string {
	key := make([]string, 0, len(order))
	for _, t := range order {
		switch t.field {
		case "ct":
			key = append(key, fmt.Sprintf("%020d", v.stored.CreateMs))
		default:
			key = append(key, v.stored.Scope)
		}
	}
	return key
}

// settingsKeyAfter reports whether the layer sorts strictly after
// (key, id) under order, with the id as the final ascending tiebreak.
func settingsKeyAfter(v settingsView, order []orderTerm, key []string, id string) bool {
	mine := settingsSortKey(v, order)
	for i, t := range order {
		if mine[i] == key[i] {
			continue
		}
		if t.desc {
			return mine[i] < key[i]
		}
		return mine[i] > key[i]
	}
	return v.stored.Scope > id
}

func settingsLess(order []orderTerm, a, b settingsView) bool {
	ka, kb := settingsSortKey(a, order), settingsSortKey(b, order)
	for i, t := range order {
		if ka[i] == kb[i] {
			continue
		}
		if t.desc {
			return ka[i] > kb[i]
		}
		return ka[i] < kb[i]
	}
	return a.stored.Scope < b.stored.Scope
}

func sortSettingsViews(views []settingsView, order []orderTerm) {
	sort.Slice(views, func(i, j int) bool { return settingsLess(order, views[i], views[j]) })
}

// settingsPageLife is how long a ListSettings page token stays valid
// (02b paging: opaque, valid for 10 min, from the same query).
const settingsPageLife = 10 * time.Minute

// settingsQueryHash binds a page token to the query that minted it:
// the raw filter and order_by plus the page size. A token from another
// query is rejected.
func settingsQueryHash(filter, order string, size int) string {
	sum := sha256.Sum256([]byte(filter + "\x00" + order + "\x00" + fmt.Sprint(size)))
	return hex.EncodeToString(sum[:])
}

// settingsCursor is a decoded ListSettings page token.
type settingsCursor struct {
	key []string
	id  string
}

// settingsPageToken is the minted form: version, query hash, sort key,
// id, expiry.
type settingsPageToken struct {
	V   int      `json:"v"`
	Q   string   `json:"q"`
	Key []string `json:"key"`
	ID  string   `json:"id"`
	Exp int64    `json:"exp"`
}

// mintSettingsToken seals a next-page token: clients treat it as opaque.
func mintSettingsToken(query string, key []string, id string, now time.Time) string {
	raw, err := json.Marshal(settingsPageToken{
		V: 1, Q: query, Key: key, ID: id, Exp: now.Add(settingsPageLife).Unix(),
	})
	if err != nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(raw)
}

// parseSettingsToken opens a page token minted by [mintSettingsToken]
// for the same query and order. Anything else is an error the handler
// reports as PAGE_TOKEN_INVALID. The key length is checked so a forged
// short key can never index past the order tuple.
func parseSettingsToken(token, want string, keyLen int, now time.Time) (settingsCursor, error) {
	if len(token) > maxFilterBytes {
		return settingsCursor{}, errors.New("api: settings page token over 4 KiB")
	}
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return settingsCursor{}, err
	}
	var pt settingsPageToken
	if err := json.Unmarshal(raw, &pt); err != nil {
		return settingsCursor{}, err
	}
	if pt.V != 1 || pt.Q != want || len(pt.Key) != keyLen || now.Unix() > pt.Exp {
		return settingsCursor{}, errors.New("api: bad settings page token")
	}
	return settingsCursor{key: pt.Key, id: pt.ID}, nil
}
