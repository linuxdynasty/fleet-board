package api

import (
	"fmt"
	"strings"
)

// This file is the 02c filter grammar (an AIP-160 subset) and the AIP-132
// order_by list, shared by every List RPC as its row-12 slice lands. A
// resource supplies its field table; the parser checks names, operators and
// literal kinds, and reports the byte offset of the bad atom so the handler
// can answer FILTER_INVALID or ORDER_BY_INVALID with it.
//
// Limits (02c): 4 KiB of filter, 32 terms, nesting depth 4, 200 values in
// one `in` list, at most 3 order terms.
const (
	maxFilterBytes = 4096
	maxFilterTerms = 32
	maxFilterDepth = 4
	maxInValues    = 200
	maxOrderTerms  = 3
)

// filterOp is one 02c comparison operator.
type filterOp int

const (
	opEq filterOp = iota
	opNe
	opLt
	opLe
	opGt
	opGe
	opHas
	opIn
)

// filterKind is the literal kind of a field.
type filterKind int

const (
	kindString filterKind = iota
	kindBool
	kindNumber
	kindTime
	kindEnum
)

// filterField is one row of a resource's 02c field table: the literal kind,
// the operators the catalogue allows, the valid short names for an enum,
// and, for a name the catalogue lists but the hub cannot serve yet, the
// reason the handler refuses.
type filterField struct {
	kind     filterKind
	repeated bool
	ops      map[filterOp]bool
	enum     map[string]bool
	unserved string
}

// filterAtom is one `field op value` comparison with its byte offset.
type filterAtom struct {
	field  string
	op     filterOp
	strs   []string
	bools  []bool
	nums   []float64
	times  []int64
	count  int
	offset int
}

type nodeOp int

const (
	nodeAtom nodeOp = iota
	nodeNot
	nodeAnd
	nodeOr
)

// filterNode is one parsed filter: an atom, a negation, or an AND/OR of
// children. A nil node is no filter and matches everything.
type filterNode struct {
	op   nodeOp
	kids []*filterNode
	atom filterAtom
}

// filterError is a grammar or catalogue refusal with the byte offset of the
// bad atom (02c: FieldViolation on the field plus metadata["offset"]).
type filterError struct {
	msg    string
	offset int
}

func (e *filterError) Error() string { return e.msg }

// fieldError is an order_by refusal.
type fieldError struct {
	msg string
}

func (e *fieldError) Error() string { return e.msg }

// orderTerm is one parsed order_by term.
type orderTerm struct {
	field string
	desc  bool
}

// parseOrderBy parses an AIP-132 order_by list ("name desc, ct") against the
// allowed names. Empty input means the resource default.
func parseOrderBy(input string, allowed map[string]bool) ([]orderTerm, *fieldError) {
	if input == "" {
		return nil, nil
	}
	parts := strings.Split(input, ",")
	if len(parts) > maxOrderTerms {
		return nil, &fieldError{"order_by holds over 3 terms"}
	}
	var out []orderTerm
	for _, part := range parts {
		words := strings.Fields(part)
		if len(words) == 0 || len(words) > 2 {
			return nil, &fieldError{fmt.Sprintf("a bad order_by term %q", part)}
		}
		if !allowed[words[0]] {
			return nil, &fieldError{fmt.Sprintf("an unknown order_by field %q", words[0])}
		}
		term := orderTerm{field: words[0]}
		if len(words) == 2 {
			switch strings.ToLower(words[1]) {
			case "desc":
				term.desc = true
			case "asc":
			default:
				return nil, &fieldError{fmt.Sprintf("a bad order_by direction in %q", part)}
			}
		}
		out = append(out, term)
	}
	return out, nil
}
