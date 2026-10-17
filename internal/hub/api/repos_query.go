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

// This file is the ListRepos query: the 02c field table, filter
// evaluation, ordering, and the query-bound page tokens. The handlers
// and the Repo assembly live in repos.go.

// repoFilterFields is the 02c Repo field table. Every catalogue name is
// served from the watched row and its stored spec: the id is host/path,
// enabled and hooks_mode read the spec, integration reads the spec's
// code host, and the times read the row's write stamps.
var repoFilterFields = map[string]filterField{
	"id":          {kind: kindString, ops: map[filterOp]bool{opEq: true, opIn: true}},
	"enabled":     {kind: kindBool, ops: map[filterOp]bool{opEq: true}},
	"host":        {kind: kindString, ops: map[filterOp]bool{opEq: true, opIn: true}},
	"hooks_mode":  {kind: kindEnum, ops: map[filterOp]bool{opEq: true, opIn: true}, enum: repoHooksModes},
	"integration": {kind: kindString, ops: map[filterOp]bool{opEq: true, opIn: true}},
	"create_time": {kind: kindTime, ops: map[filterOp]bool{
		opEq: true, opNe: true, opLt: true, opLe: true, opGt: true, opGe: true,
	}},
	"update_time": {kind: kindTime, ops: map[filterOp]bool{
		opEq: true, opNe: true, opLt: true, opLe: true, opGt: true, opGe: true,
	}},
}

// repoHooksModes is the 02c RepoHooksMode short-name set, every value the
// contract names.
var repoHooksModes = map[string]bool{
	"UNSPECIFIED": true, "AUTO": true, "ESHU": true, "GENERIC": true,
}

// repoOrderFields is the 02c Repo order_by catalogue.
var repoOrderFields = map[string]bool{"name": true, "ct": true}

// defaultRepoOrder is the List order with no order_by: id (host/path).
var defaultRepoOrder = []orderTerm{{field: "name"}}

// repoView is one watched row with its decoded spec, so the filter and
// the order tuple read the same values the Repo carries.
type repoView struct {
	stored statedb.RepoStored
	spec   *hubv1.RepoSpec
}

// repoIDOf renders the view's canonical id: host/path.
func repoIDOf(v repoView) string {
	return repoID(v.stored.Host, v.stored.Path)
}

// repoHooksModeShort renders the stored hooks mode as its 02c short name.
func repoHooksModeShort(m hubv1.RepoHooksMode) string {
	return strings.TrimPrefix(m.String(), "REPO_HOOKS_MODE_")
}

func nodeMatchesRepo(n *filterNode, v repoView) bool {
	if n == nil {
		return true
	}
	switch n.op {
	case nodeAtom:
		return atomMatchesRepo(n.atom, v)
	case nodeNot:
		return !nodeMatchesRepo(n.kids[0], v)
	case nodeAnd:
		for _, k := range n.kids {
			if !nodeMatchesRepo(k, v) {
				return false
			}
		}
		return true
	case nodeOr:
		for _, k := range n.kids {
			if nodeMatchesRepo(k, v) {
				return true
			}
		}
		return false
	}
	return false
}

// atomMatchesRepo evaluates one comparison. The parse already checked the
// field, the operator and every literal kind, so an unknown field here is
// unreachable and matches nothing.
func atomMatchesRepo(a filterAtom, v repoView) bool {
	switch a.field {
	case "id":
		return matchStrings(a.op, repoIDOf(v), a.strs)
	case "enabled":
		return a.bools[0] == v.spec.GetEnabled()
	case "host":
		return matchStrings(a.op, v.spec.GetRepo().GetHost(), a.strs)
	case "hooks_mode":
		return matchStrings(a.op, repoHooksModeShort(v.spec.GetHooksMode()), a.strs)
	case "integration":
		return matchStrings(a.op, v.spec.GetRepo().GetCodeHost().GetValue(), a.strs)
	case "create_time":
		return matchTimes(a.op, v.stored.CreateMs, a.times)
	case "update_time":
		return matchTimes(a.op, v.stored.UpdateMs, a.times)
	}
	return false
}

// repoSortKey renders one repo's order tuple as fixed strings: the id
// as-is, times as zero-padded millis so string order is time order. The
// canonical id breaks ties, like every other order.
func repoSortKey(v repoView, order []orderTerm) []string {
	key := make([]string, 0, len(order))
	for _, t := range order {
		switch t.field {
		case "ct":
			key = append(key, fmt.Sprintf("%020d", v.stored.CreateMs))
		default:
			key = append(key, repoIDOf(v))
		}
	}
	return key
}

// repoKeyAfter reports whether the repo sorts strictly after (key, id)
// under order, with the canonical id as the final ascending tiebreak.
func repoKeyAfter(v repoView, order []orderTerm, key []string, id string) bool {
	mine := repoSortKey(v, order)
	for i, t := range order {
		if mine[i] == key[i] {
			continue
		}
		if t.desc {
			return mine[i] < key[i]
		}
		return mine[i] > key[i]
	}
	return repoIDOf(v) > id
}

func repoLess(order []orderTerm, a, b repoView) bool {
	ka, kb := repoSortKey(a, order), repoSortKey(b, order)
	for i, t := range order {
		if ka[i] == kb[i] {
			continue
		}
		if t.desc {
			return ka[i] > kb[i]
		}
		return ka[i] < kb[i]
	}
	return repoIDOf(a) < repoIDOf(b)
}

func sortRepoViews(views []repoView, order []orderTerm) {
	sort.Slice(views, func(i, j int) bool { return repoLess(order, views[i], views[j]) })
}

// repoPageLife is how long a ListRepos page token stays valid (02b
// paging: opaque, valid for 10 min, from the same query).
const repoPageLife = 10 * time.Minute

// repoQueryHash binds a page token to the query that minted it: the raw
// filter and order_by plus the page size. A token from another query is
// rejected.
func repoQueryHash(filter, order string, size int) string {
	sum := sha256.Sum256([]byte(filter + "\x00" + order + "\x00" + fmt.Sprint(size)))
	return hex.EncodeToString(sum[:])
}

// repoCursor is a decoded ListRepos page token.
type repoCursor struct {
	key []string
	id  string
}

// repoPageToken is the minted form: version, query hash, sort key, id,
// expiry.
type repoPageToken struct {
	V   int      `json:"v"`
	Q   string   `json:"q"`
	Key []string `json:"key"`
	ID  string   `json:"id"`
	Exp int64    `json:"exp"`
}

// mintRepoToken seals a next-page token: clients treat it as opaque.
func mintRepoToken(query string, key []string, id string, now time.Time) string {
	raw, err := json.Marshal(repoPageToken{
		V: 1, Q: query, Key: key, ID: id, Exp: now.Add(repoPageLife).Unix(),
	})
	if err != nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(raw)
}

// parseRepoToken opens a page token minted by [mintRepoToken] for the same
// query and order. Anything else is an error the handler reports as
// PAGE_TOKEN_INVALID. The key length is checked so a forged short key can
// never index past the order tuple.
func parseRepoToken(token, want string, keyLen int, now time.Time) (repoCursor, error) {
	if len(token) > maxFilterBytes {
		return repoCursor{}, errors.New("api: repo page token over 4 KiB")
	}
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return repoCursor{}, err
	}
	var pt repoPageToken
	if err := json.Unmarshal(raw, &pt); err != nil {
		return repoCursor{}, err
	}
	if pt.V != 1 || pt.Q != want || len(pt.Key) != keyLen || now.Unix() > pt.Exp {
		return repoCursor{}, errors.New("api: bad repo page token")
	}
	return repoCursor{key: pt.Key, id: pt.ID}, nil
}
