package main

import (
	"bytes"
	"encoding/binary"
	"testing"
)

const testPage = 512

// fakeFile builds a database image of n pages; kinds maps a page number to its
// B-tree page type and right-most child pointer (0 for a leaf).
func fakeFile(n int, pageSize uint16, kinds map[uint32][2]uint32) []byte {
	ps := int(pageSize)
	if pageSize == 1 {
		ps = 65536
	}
	b := make([]byte, n*ps)
	copy(b, "SQLite format 3\x00")
	binary.BigEndian.PutUint16(b[16:], pageSize)
	for pg, k := range kinds {
		off := int(pg-1) * ps
		if pg == 1 {
			off += 100
		}
		b[off] = byte(k[0])
		if k[0] == 2 || k[0] == 5 {
			binary.BigEndian.PutUint32(b[off+8:], k[1])
		}
	}
	return b
}

func TestBtreeDepth(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		pages    int
		pageSize uint16
		kinds    map[uint32][2]uint32
		root     uint32
		want     int
		wantErr  bool
	}{
		{"leaf table root", 3, testPage, map[uint32][2]uint32{2: {13, 0}}, 2, 1, false},
		{"leaf index root", 3, testPage, map[uint32][2]uint32{3: {10, 0}}, 3, 1, false},
		{"table two levels", 4, testPage, map[uint32][2]uint32{2: {5, 4}, 4: {13, 0}}, 2, 2, false},
		{"index three levels", 6, testPage, map[uint32][2]uint32{2: {2, 5}, 5: {2, 3}, 3: {10, 0}}, 2, 3, false},
		{"page one header offset", 2, testPage, map[uint32][2]uint32{1: {5, 2}, 2: {13, 0}}, 1, 2, false},
		{"page size 65536 encoded as 1", 2, 1, map[uint32][2]uint32{2: {13, 0}}, 2, 1, false},
		{"not a btree page", 3, testPage, map[uint32][2]uint32{2: {0, 0}}, 2, 0, true},
		{"cycle", 3, testPage, map[uint32][2]uint32{2: {5, 3}, 3: {5, 2}}, 2, 0, true},
		{"child past end", 3, testPage, map[uint32][2]uint32{2: {5, 9}}, 2, 0, true},
		{"root zero", 3, testPage, nil, 0, 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := bytes.NewReader(fakeFile(tc.pages, tc.pageSize, tc.kinds))
			got, err := btreeDepth(r, tc.root)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if got != tc.want {
				t.Fatalf("depth = %d, want %d", got, tc.want)
			}
		})
	}
}
