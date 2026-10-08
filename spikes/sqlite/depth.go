package main

import (
	"database/sql"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
)

// btreeDepth counts the levels of the B-tree rooted at page root (a leaf root
// is depth 1) by following right-most child pointers in the database file.
// Every leaf of a SQLite B-tree is at the same depth, so one path is enough
// (fileformat2.html, "B-tree Pages"). The file must be checkpointed first.
func btreeDepth(r io.ReaderAt, root uint32) (int, error) {
	hdr := make([]byte, 100)
	if _, err := r.ReadAt(hdr, 0); err != nil {
		return 0, fmt.Errorf("read header: %w", err)
	}
	ps := int64(binary.BigEndian.Uint16(hdr[16:]))
	if ps == 1 {
		ps = 65536
	}
	page := make([]byte, 12)
	seen := map[uint32]bool{}
	for depth, pg := 1, root; ; depth++ {
		if pg == 0 || seen[pg] {
			return 0, fmt.Errorf("bad page %d at depth %d", pg, depth)
		}
		seen[pg] = true
		off := int64(pg-1) * ps
		if pg == 1 {
			off += 100
		}
		if _, err := r.ReadAt(page, off); err != nil {
			return 0, fmt.Errorf("read page %d: %w", pg, err)
		}
		switch page[0] {
		case 10, 13:
			return depth, nil
		case 2, 5:
			pg = binary.BigEndian.Uint32(page[8:])
		default:
			return 0, fmt.Errorf("page %d type %d is not a B-tree page", pg, page[0])
		}
	}
}

// printDepths checkpoints the WAL into the file, then prints the depth and
// page count of every table and index B-tree.
func printDepths(label string, w *sql.DB, path string) {
	var busy, logFrames, done int
	check(w.QueryRow("PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &logFrames, &done))
	if busy != 0 {
		panic("printDepths: the WAL checkpoint was blocked; the file may hold old pages")
	}
	var pages int
	check(w.QueryRow("PRAGMA page_count").Scan(&pages))
	rows := must(w.Query("SELECT name, rootpage FROM sqlite_schema WHERE type IN ('table','index') AND rootpage > 0"))
	roots := map[string]uint32{}
	for rows.Next() {
		var n string
		var rp uint32
		check(rows.Scan(&n, &rp))
		roots[n] = rp
	}
	check(rows.Err())
	f := must(os.Open(path))
	defer f.Close()
	var names, out []string
	for n := range roots {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		out = append(out, fmt.Sprintf("%s=%d", n, must(btreeDepth(f, roots[n]))))
	}
	fmt.Printf("btree_depth %s: page_count=%d %s\n", label, pages, strings.Join(out, " "))
}
