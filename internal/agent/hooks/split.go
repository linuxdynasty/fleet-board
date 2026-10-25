package hooks

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// This file parses JSON while preserving every byte the merge does not
// own: member order, key spellings, values, separators and surrounding
// whitespace. A merge that changes only fleet entries therefore leaves
// every other byte of the file identical.

// rawObject is one JSON object with the exact source span of each part.
type rawObject struct {
	raw   []byte
	keys  []string
	key   [][]byte // exact `"key"` bytes
	val   [][]byte // exact value bytes
	pre   [][]byte // bytes from '{'/previous value end to key start
	colon [][]byte // bytes from key end to value start (holds ':')
	trail []byte   // bytes from last value end to '}'
}

// rawArray is one JSON array with the exact source span of each part.
type rawArray struct {
	raw   []byte
	elems [][]byte // exact element bytes
	gaps  [][]byte // bytes from '['/previous element end to element start
	trail []byte   // bytes from last element end to ']'
}

func parseObject(obj []byte) (*rawObject, error) {
	dec := json.NewDecoder(bytes.NewReader(obj))
	if t, err := dec.Token(); err != nil {
		return nil, err
	} else if d, ok := t.(json.Delim); !ok || d != '{' {
		return nil, fmt.Errorf("not an object")
	}
	o := &rawObject{raw: obj}
	pos := 1 // just past '{'
	for dec.More() {
		j := skipWS(obj, pos)
		if j < len(obj) && obj[j] == ',' {
			j++
		}
		start := skipWS(obj, j)
		kt, err := dec.Token()
		if err != nil {
			return nil, err
		}
		key, ok := kt.(string)
		if !ok {
			return nil, fmt.Errorf("non-string object key")
		}
		keyEnd := int(dec.InputOffset())
		c := skipWS(obj, keyEnd)
		if c >= len(obj) || obj[c] != ':' {
			return nil, fmt.Errorf("missing colon after key %q", key)
		}
		valStart := skipWS(obj, c+1)
		valRaw, valEnd, err := splitValue(obj, valStart)
		if err != nil {
			return nil, fmt.Errorf("key %q: %w", key, err)
		}
		var discard any
		if err := dec.Decode(&discard); err != nil {
			return nil, fmt.Errorf("key %q: %w", key, err)
		}
		if off := int(dec.InputOffset()); off < valEnd || len(bytes.TrimSpace(obj[valEnd:off])) > 0 {
			return nil, fmt.Errorf("key %q: decoder drift", key)
		}
		o.keys = append(o.keys, key)
		o.key = append(o.key, obj[start:keyEnd])
		o.val = append(o.val, valRaw)
		o.pre = append(o.pre, obj[pos:start])
		o.colon = append(o.colon, obj[keyEnd:valStart])
		pos = valEnd
	}
	end := skipWS(obj, pos)
	if end >= len(obj) || obj[end] != '}' {
		return nil, fmt.Errorf("missing object end")
	}
	if _, err := dec.Token(); err != nil {
		return nil, fmt.Errorf("object end: %w", err)
	}
	if off := int(dec.InputOffset()); off != end+1 {
		return nil, fmt.Errorf("decoder drift at object end")
	}
	o.trail = obj[pos:end]
	return o, nil
}

func parseArray(arr []byte) (*rawArray, error) {
	dec := json.NewDecoder(bytes.NewReader(arr))
	if t, err := dec.Token(); err != nil {
		return nil, err
	} else if d, ok := t.(json.Delim); !ok || d != '[' {
		return nil, fmt.Errorf("not an array")
	}
	a := &rawArray{raw: arr}
	pos := 1 // just past '['
	for dec.More() {
		j := skipWS(arr, pos)
		if j < len(arr) && arr[j] == ',' {
			j++
		}
		start := skipWS(arr, j)
		raw, end, err := splitValue(arr, start)
		if err != nil {
			return nil, err
		}
		var discard any
		if err := dec.Decode(&discard); err != nil {
			return nil, err
		}
		if off := int(dec.InputOffset()); off < end || len(bytes.TrimSpace(arr[end:off])) > 0 {
			return nil, fmt.Errorf("decoder drift in array")
		}
		a.elems = append(a.elems, raw)
		a.gaps = append(a.gaps, arr[pos:start])
		pos = end
	}
	end := skipWS(arr, pos)
	if end >= len(arr) || arr[end] != ']' {
		return nil, fmt.Errorf("missing array end")
	}
	if _, err := dec.Token(); err != nil {
		return nil, fmt.Errorf("array end: %w", err)
	}
	if off := int(dec.InputOffset()); off != end+1 {
		return nil, fmt.Errorf("decoder drift at array end")
	}
	a.trail = arr[pos:end]
	return a, nil
}

// index returns the first member with the key, or -1.
func (o *rawObject) index(want string) int {
	for i, k := range o.keys {
		if k == want {
			return i
		}
	}
	return -1
}

// build re-encodes the object from parts. Keeping a member means its key,
// value, separators and whitespace survive verbatim.
func (o *rawObject) build(keep []bool, vals [][]byte) []byte {
	var b bytes.Buffer
	b.WriteByte('{')
	first := true
	for i := range o.keys {
		if keep != nil && !keep[i] {
			continue
		}
		gap := o.pre[i]
		if first {
			gap = stripLeadingComma(gap)
		}
		b.Write(gap)
		b.Write(o.key[i])
		b.Write(o.colon[i])
		if vals != nil {
			b.Write(vals[i])
		} else {
			b.Write(o.val[i])
		}
		first = false
	}
	b.Write(o.trail)
	b.WriteByte('}')
	return b.Bytes()
}

// build re-encodes the array from parts.
func (a *rawArray) build(keep []bool, elems [][]byte) []byte {
	var b bytes.Buffer
	b.WriteByte('[')
	first := true
	src := a.elems
	if elems != nil {
		src = elems
	}
	for i := range src {
		if keep != nil && !keep[i] {
			continue
		}
		gap := a.gaps[i]
		if first {
			gap = stripLeadingComma(gap)
		}
		b.Write(gap)
		b.Write(src[i])
		first = false
	}
	b.Write(a.trail)
	b.WriteByte(']')
	return b.Bytes()
}

// appendElem adds an element before the closing bracket; only the new
// element uses a canonical separator.
func (a *rawArray) appendElem(elem []byte) []byte {
	cut := len(a.raw) - len(a.trail) - 1
	var b bytes.Buffer
	b.Write(a.raw[:cut])
	b.WriteByte(',')
	b.Write(elem)
	b.Write(a.trail)
	b.WriteByte(']')
	return b.Bytes()
}

// stripLeadingComma drops the separator comma from a gap, keeping the
// whitespace after it, so a kept element can move to first position.
func stripLeadingComma(gap []byte) []byte {
	if i := bytes.IndexByte(gap, ','); i >= 0 {
		return gap[i+1:]
	}
	return gap
}

// splitValue decodes the single JSON value starting at src[start] and
// returns its exact source bytes and the offset just past it.
func splitValue(src []byte, start int) ([]byte, int, error) {
	if start >= len(src) {
		return nil, 0, fmt.Errorf("value past end of input")
	}
	dec := json.NewDecoder(bytes.NewReader(src[start:]))
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, 0, err
	}
	end := start + int(dec.InputOffset())
	return src[start:end], end, nil
}

func skipWS(src []byte, i int) int {
	for i < len(src) && (src[i] == ' ' || src[i] == '\t' || src[i] == '\n' || src[i] == '\r') {
		i++
	}
	return i
}
