package api

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// This file is the tokenizer and the recursive-descent parser behind
// [parseFilter]: tokens with byte offsets, then or/and/not/atom.

type tokKind int

const (
	tIdent tokKind = iota
	tString
	tNumber
	tBool
	tOp
	tLParen
	tRParen
	tComma
)

type token struct {
	kind    tokKind
	text    string
	num     float64
	boolean bool
	pos     int
}

func isIdentStart(c byte) bool {
	return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

func isIdentChar(c byte) bool { return isIdentStart(c) || c >= '0' && c <= '9' }

// tokenize splits input into grammar tokens with byte offsets.
func tokenize(input string) ([]token, *filterError) {
	var out []token
	i := 0
	for i < len(input) {
		c := input[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			i++
		case c == '(':
			out = append(out, token{kind: tLParen, text: "(", pos: i})
			i++
		case c == ')':
			out = append(out, token{kind: tRParen, text: ")", pos: i})
			i++
		case c == ',':
			out = append(out, token{kind: tComma, text: ",", pos: i})
			i++
		case c == '"':
			start := i
			i++
			var sb strings.Builder
			closed := false
			for i < len(input) {
				switch input[i] {
				case '"':
					closed = true
				case '\\':
					if i+1 >= len(input) || input[i+1] != '"' && input[i+1] != '\\' {
						return nil, &filterError{"bad escape in a string", start}
					}
					sb.WriteByte(input[i+1])
					i += 2
					continue
				default:
					sb.WriteByte(input[i])
					i++
					continue
				}
				break
			}
			if !closed {
				return nil, &filterError{"an unterminated string", start}
			}
			i++ // past the closing quote
			out = append(out, token{kind: tString, text: sb.String(), pos: start})
		case c == '=':
			out = append(out, token{kind: tOp, text: "=", pos: i})
			i++
		case c == '!' && i+1 < len(input) && input[i+1] == '=':
			out = append(out, token{kind: tOp, text: "!=", pos: i})
			i += 2
		case c == '<' || c == '>':
			op := string(c)
			i++
			if i < len(input) && input[i] == '=' {
				op += "="
				i++
			}
			out = append(out, token{kind: tOp, text: op, pos: i - len(op)})
		case c == ':':
			out = append(out, token{kind: tOp, text: ":", pos: i})
			i++
		case c >= '0' && c <= '9' || c == '-' && i+1 < len(input) && input[i+1] >= '0' && input[i+1] <= '9':
			start := i
			if input[i] == '-' {
				i++
			}
			for i < len(input) && input[i] >= '0' && input[i] <= '9' {
				i++
			}
			if i < len(input) && input[i] == '.' {
				i++
				for i < len(input) && input[i] >= '0' && input[i] <= '9' {
					i++
				}
			}
			num, err := strconv.ParseFloat(input[start:i], 64)
			if err != nil {
				return nil, &filterError{"a bad number", start}
			}
			out = append(out, token{kind: tNumber, text: input[start:i], num: num, pos: start})
		case isIdentStart(c):
			start := i
			for i < len(input) && isIdentChar(input[i]) {
				i++
			}
			word := input[start:i]
			if word == "true" || word == "false" {
				out = append(out, token{kind: tBool, text: word, boolean: word == "true", pos: start})
			} else {
				out = append(out, token{kind: tIdent, text: word, pos: start})
			}
		default:
			return nil, &filterError{fmt.Sprintf("an unexpected character %q", c), i}
		}
	}
	return out, nil
}

type parser struct {
	toks   []token
	pos    int
	fields map[string]filterField
	terms  int
	err    *filterError
}

func (p *parser) fail(msg string, pos int) *filterNode {
	if p.err == nil {
		at := pos
		if pos < len(p.toks) {
			at = p.toks[pos].pos
		} else if len(p.toks) > 0 {
			at = p.toks[len(p.toks)-1].pos
		}
		p.err = &filterError{msg, at}
	}
	return nil
}

// failAt reports msg at a byte offset (a token's pos or a field's offset),
// never a token index: the two never mix, so metadata["offset"] points at
// the bad atom.
func (p *parser) failAt(msg string, bytePos int) *filterNode {
	if p.err == nil {
		p.err = &filterError{msg, bytePos}
	}
	return nil
}

func (p *parser) peek() *token {
	if p.pos < len(p.toks) {
		return &p.toks[p.pos]
	}
	return nil
}

// parseFilter parses an 02c filter against fields. Empty input is no filter.
func parseFilter(input string, fields map[string]filterField) (*filterNode, *filterError) {
	if input == "" {
		return nil, nil
	}
	if len(input) > maxFilterBytes {
		return nil, &filterError{"the filter is over 4 KiB", 0}
	}
	toks, ferr := tokenize(input)
	if ferr != nil {
		return nil, ferr
	}
	if len(toks) == 0 {
		return nil, &filterError{"the filter holds no terms", 0}
	}
	p := &parser{toks: toks, fields: fields}
	n := p.parseOr(0)
	if p.err != nil {
		return nil, p.err
	}
	if p.pos != len(p.toks) {
		return nil, &filterError{"trailing input after the filter", p.toks[p.pos].pos}
	}
	if p.terms > maxFilterTerms {
		return nil, &filterError{"the filter holds over 32 terms", 0}
	}
	return n, nil
}

func (p *parser) parseOr(depth int) *filterNode {
	if depth > maxFilterDepth {
		return p.fail("the filter nests over 4 deep", p.pos)
	}
	left := p.parseAnd(depth)
	if p.err != nil || left == nil {
		return left
	}
	t := p.peek()
	if t == nil || t.kind != tIdent || t.text != "OR" {
		return left
	}
	out := &filterNode{op: nodeOr, kids: []*filterNode{left}}
	for t != nil && t.kind == tIdent && t.text == "OR" {
		p.pos++
		right := p.parseAnd(depth)
		if p.err != nil {
			return nil
		}
		if right == nil {
			return p.fail("OR needs a term after it", p.pos)
		}
		out.kids = append(out.kids, right)
		t = p.peek()
	}
	return out
}

func (p *parser) parseAnd(depth int) *filterNode {
	left := p.parseNot(depth)
	if p.err != nil || left == nil {
		return left
	}
	t := p.peek()
	if t == nil || t.kind != tIdent || t.text != "AND" {
		return left
	}
	out := &filterNode{op: nodeAnd, kids: []*filterNode{left}}
	for t != nil && t.kind == tIdent && t.text == "AND" {
		p.pos++
		right := p.parseNot(depth)
		if p.err != nil {
			return nil
		}
		if right == nil {
			return p.fail("AND needs a term after it", p.pos)
		}
		out.kids = append(out.kids, right)
		t = p.peek()
	}
	return out
}

func (p *parser) parseNot(depth int) *filterNode {
	t := p.peek()
	if t != nil && t.kind == tIdent && t.text == "NOT" {
		p.pos++
		kid := p.parseNot(depth + 1)
		if p.err != nil {
			return nil
		}
		if kid == nil {
			return p.fail("NOT needs a term after it", p.pos)
		}
		return &filterNode{op: nodeNot, kids: []*filterNode{kid}}
	}
	return p.parseAtom(depth)
}

func (p *parser) parseAtom(depth int) *filterNode {
	t := p.peek()
	if t == nil {
		return p.fail("the filter ends mid-term", p.pos)
	}
	if t.kind == tLParen {
		p.pos++
		n := p.parseOr(depth + 1)
		if p.err != nil {
			return nil
		}
		closer := p.peek()
		if closer == nil || closer.kind != tRParen {
			if closer != nil {
				return p.failAt("a closing parenthesis", closer.pos)
			}
			return p.fail("a closing parenthesis", p.pos)
		}
		p.pos++
		return n
	}
	if t.kind != tIdent {
		return p.failAt("a field name", t.pos)
	}
	field := t.text
	pos := t.pos
	def, ok := p.fields[field]
	if !ok {
		p.pos++
		return p.failAt(fmt.Sprintf("an unknown filter field %q", field), pos)
	}
	if def.unserved != "" {
		p.pos++
		return p.failAt(def.unserved, pos)
	}
	p.pos++
	opTok := p.peek()
	if opTok == nil {
		return p.fail("an operator after the field", p.pos)
	}
	var op filterOp
	switch {
	case opTok.kind == tOp:
		switch opTok.text {
		case "=":
			op = opEq
		case "!=":
			op = opNe
		case "<":
			op = opLt
		case "<=":
			op = opLe
		case ">":
			op = opGt
		case ">=":
			op = opGe
		case ":":
			op = opHas
		default:
			return p.failAt("an operator after the field", opTok.pos)
		}
		p.pos++
	case opTok.kind == tIdent && opTok.text == "in":
		op = opIn
		p.pos++
	default:
		return p.failAt("an operator after the field", opTok.pos)
	}
	if !def.ops[op] {
		return p.failAt(fmt.Sprintf("operator %q is not allowed on %q", opText(op), field), pos)
	}
	if op == opHas && !def.repeated {
		return p.failAt(fmt.Sprintf("operator %q needs a repeated field", opText(op)), pos)
	}
	atom := filterAtom{field: field, op: op, offset: pos}
	if op == opIn {
		open := p.peek()
		if open == nil || open.kind != tLParen {
			if open != nil {
				return p.failAt("a value list after in", open.pos)
			}
			return p.fail("a value list after in", p.pos)
		}
		p.pos++
		for {
			v, ok := p.parseValue(def, field)
			if !ok {
				return nil
			}
			v.appendTo(&atom)
			if atom.count > maxInValues {
				return p.failAt("an in list holds over 200 values", pos)
			}
			next := p.peek()
			if next != nil && next.kind == tComma {
				p.pos++
				continue
			}
			break
		}
		closer := p.peek()
		if closer == nil || closer.kind != tRParen {
			if closer != nil {
				return p.failAt("a closing parenthesis after the value list", closer.pos)
			}
			return p.fail("a closing parenthesis after the value list", p.pos)
		}
		p.pos++
	} else {
		v, ok := p.parseValue(def, field)
		if !ok {
			return nil
		}
		v.appendTo(&atom)
	}
	p.terms++
	return &filterNode{op: nodeAtom, atom: atom}
}

// filterValue is one parsed literal.
type filterValue struct {
	str     string
	boolean bool
	num     float64
	tm      int64
	isStr   bool
	isBool  bool
	isNum   bool
	isTime  bool
}

func (v filterValue) appendTo(a *filterAtom) {
	switch {
	case v.isStr:
		a.strs = append(a.strs, v.str)
	case v.isBool:
		a.bools = append(a.bools, v.boolean)
	case v.isNum:
		a.nums = append(a.nums, v.num)
	case v.isTime:
		a.times = append(a.times, v.tm)
	}
	a.count++
}

func (p *parser) parseValue(def filterField, field string) (filterValue, bool) {
	t := p.peek()
	if t == nil {
		p.fail("a value after the operator", p.pos)
		return filterValue{}, false
	}
	switch def.kind {
	case kindString:
		if t.kind != tString {
			p.failAt(fmt.Sprintf("a quoted string for %q", field), t.pos)
			return filterValue{}, false
		}
		p.pos++
		return filterValue{str: t.text, isStr: true}, true
	case kindBool:
		if t.kind != tBool {
			p.failAt(fmt.Sprintf("true or false for %q", field), t.pos)
			return filterValue{}, false
		}
		p.pos++
		return filterValue{boolean: t.boolean, isBool: true}, true
	case kindNumber:
		if t.kind != tNumber {
			p.failAt(fmt.Sprintf("a number for %q", field), t.pos)
			return filterValue{}, false
		}
		p.pos++
		return filterValue{num: t.num, isNum: true}, true
	case kindTime:
		if t.kind != tString {
			p.failAt(fmt.Sprintf("a quoted RFC 3339 time for %q", field), t.pos)
			return filterValue{}, false
		}
		tm, err := time.Parse(time.RFC3339, t.text)
		if err != nil {
			p.failAt(fmt.Sprintf("an RFC 3339 time for %q", field), t.pos)
			return filterValue{}, false
		}
		p.pos++
		return filterValue{tm: tm.UnixMilli(), isTime: true}, true
	case kindEnum:
		if t.kind != tIdent {
			p.failAt(fmt.Sprintf("an enum short name for %q", field), t.pos)
			return filterValue{}, false
		}
		if !def.enum[t.text] {
			p.failAt(fmt.Sprintf("an unknown %q value %q", field, t.text), t.pos)
			return filterValue{}, false
		}
		p.pos++
		return filterValue{str: t.text, isStr: true}, true
	}
	p.failAt(fmt.Sprintf("a value for %q", field), t.pos)
	return filterValue{}, false
}

func opText(op filterOp) string {
	switch op {
	case opEq:
		return "="
	case opNe:
		return "!="
	case opLt:
		return "<"
	case opLe:
		return "<="
	case opGt:
		return ">"
	case opGe:
		return ">="
	case opHas:
		return ":"
	case opIn:
		return "in"
	}
	return "?"
}
