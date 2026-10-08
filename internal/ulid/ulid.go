package ulid

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"time"
)

// alphabet is Crockford's base 32 in lowercase (no i, l, o, u).
const alphabet = "0123456789abcdefghjkmnpqrstvwxyz"

// Len is the length of every id New returns.
const Len = 26

// maxMillis is the largest time a ULID holds: 48 bits of Unix milliseconds.
const maxMillis = 1<<48 - 1

// New mints a ULID: 48 bits of now in Unix milliseconds, then 80 bits read
// from random, as 26 lowercase Crockford base-32 characters. Ids minted in
// later milliseconds sort after earlier ones as strings; within one
// millisecond their order is random.
func New(now time.Time, random io.Reader) (string, error) {
	ms := now.UnixMilli()
	if ms < 0 || ms > maxMillis {
		return "", errors.New("ulid: time is outside 1970 to 10889")
	}
	var b [16]byte
	binary.BigEndian.PutUint64(b[:8], uint64(ms)<<16) // ms is in [0, 2^48)
	if _, err := io.ReadFull(random, b[6:]); err != nil {
		return "", fmt.Errorf("ulid: random: %w", err)
	}
	// 128 bits make 26 groups of 5 once two zero bits lead: group 0 is the
	// top 3 bits, every later group the next 5.
	var out [Len]byte
	for i := range Len {
		out[i] = alphabet[group(&b, i*5-2)]
	}
	return string(out[:]), nil
}

// group is the 5 bits of b starting at bit pos (bit 0 is the top bit of
// b[0]); positions before 0 read as zero.
func group(b *[16]byte, pos int) byte {
	var v byte
	for k := range 5 {
		v <<= 1
		if p := pos + k; p >= 0 && b[p/8]&(0x80>>(p%8)) != 0 {
			v |= 1
		}
	}
	return v
}
