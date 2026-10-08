package ulid_test

import (
	"bytes"
	"crypto/rand"
	"errors"
	"io"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/ulid"
)

// hubID is the id format of api/fleet/type/v1/ids.proto.
var hubID = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)

// The time part of the ULID spec's example 01ARZ3NDEKTSV4RRFFQ69G5FAV is
// 1469922850259 ms; the rest was worked out by hand in base 32.
func TestNewMatchesTheSpecVectors(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		ms      int64
		entropy []byte
		want    string
	}{
		{"spec example time, entropy 1..10", 1469922850259, []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}, "01arz3ndek041061050r3gg28a"},
		{"zero", 0, make([]byte, 10), "00000000000000000000000000"},
		{"largest", 1<<48 - 1, bytes.Repeat([]byte{0xff}, 10), "7zzzzzzzzzzzzzzzzzzzzzzzzz"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := ulid.New(time.UnixMilli(tc.ms), bytes.NewReader(tc.entropy))
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			if got != tc.want {
				t.Errorf("New = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestNewIsAHubIDOf26Characters(t *testing.T) {
	t.Parallel()
	for range 100 {
		id, err := ulid.New(time.Now(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		if len(id) != 26 || !hubID.MatchString(id) || strings.ContainsAny(id, "ilou-") {
			t.Fatalf("%q is not a 26-character lowercase Crockford id", id)
		}
	}
}

func TestLaterMillisecondsSortLater(t *testing.T) {
	t.Parallel()
	base := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	var ids []string
	for i := range 50 {
		id, err := ulid.New(base.Add(time.Duration(i)*time.Millisecond), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	if !slices.IsSorted(ids) {
		t.Errorf("ids minted one millisecond apart do not sort by time: %v", ids)
	}
}

func TestNewRefusesTimesOutsideTheRange(t *testing.T) {
	t.Parallel()
	for _, at := range []time.Time{time.UnixMilli(-1), time.UnixMilli(1 << 48)} {
		if _, err := ulid.New(at, rand.Reader); err == nil {
			t.Errorf("New(%d ms) succeeded", at.UnixMilli())
		}
	}
}

func TestNewReportsAShortRandomSource(t *testing.T) {
	t.Parallel()
	_, err := ulid.New(time.Now(), bytes.NewReader([]byte{1, 2, 3}))
	if err == nil {
		t.Fatal("New succeeded with 3 random bytes")
	}
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Errorf("error %q does not wrap the random source's failure", err)
	}
}
