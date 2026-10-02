package main

import (
	"bufio"
	"os"
	"strconv"
	"strings"
)

// diskWritten is write_bytes from /proc/self/io: bytes this process caused to
// be sent to the storage layer (WAL, checkpoints and growth together).
func diskWritten() int64 {
	f, err := os.Open("/proc/self/io")
	if err != nil {
		return 0
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if v, ok := strings.CutPrefix(sc.Text(), "write_bytes: "); ok {
			n, _ := strconv.ParseInt(v, 10, 64)
			return n
		}
	}
	return 0
}
