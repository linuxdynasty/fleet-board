// Spike S2 for docs/specs/04-technology-review.md: how well does zstd
// compress terminal output when the agent ships it in 10 s chunks instead of
// one stream (01c measured 115x on a whole hour)? Throwaway code.
//
//	tmux pipe-pane -o -t <pane> 'zstdchunk rec /path/run'   # writes run.raw + run.idx
//	zstdchunk analyze /path/run
package main

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/klauspost/compress/zstd"
)

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: zstdchunk rec|analyze <path-prefix>")
		os.Exit(2)
	}
	switch os.Args[1] {
	case "rec":
		rec(os.Args[2])
	case "analyze":
		analyze(os.Args[2])
	}
}

// rec copies stdin to <p>.raw and writes "<unix_ms> <offset>" to <p>.idx
// once a second, so chunks can be cut by time afterwards.
func rec(p string) {
	raw := must(os.OpenFile(p+".raw", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600))
	idx := must(os.OpenFile(p+".idx", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600))
	var off int64
	data := make(chan []byte, 1024)
	go func() {
		defer close(data)
		buf := make([]byte, 64<<10)
		for {
			n, err := os.Stdin.Read(buf)
			if n > 0 {
				data <- append([]byte(nil), buf[:n]...)
			}
			if err != nil {
				return
			}
		}
	}()
	t := time.NewTicker(time.Second)
	for {
		select {
		case b, ok := <-data:
			if !ok {
				fmt.Fprintf(idx, "%d %d\n", time.Now().UnixMilli(), off)
				return
			}
			must(raw.Write(b))
			off += int64(len(b))
		case now := <-t.C:
			fmt.Fprintf(idx, "%d %d\n", now.UnixMilli(), off)
		}
	}
}

type mark struct{ ms, off int64 }

func analyze(p string) {
	raw := must(os.ReadFile(p + ".raw"))
	var marks []mark
	sc := bufio.NewScanner(bytes.NewReader(must(os.ReadFile(p + ".idx"))))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		marks = append(marks, mark{atoi(f[0]), atoi(f[1])})
	}
	hours := float64(marks[len(marks)-1].ms-marks[0].ms) / 3.6e6
	fmt.Printf("recording: %.2f h, raw %.1f MB (%.1f MB/h)\n\n", hours, mb(len(raw)), mb(len(raw))/hours)
	fmt.Printf("%-44s %10s %8s %12s %14s\n", "method (zstd level 3)", "MB/h", "ratio", "B/s/pane", "chunk p50 B")
	line := func(name string, total int, chunks []int) {
		p50 := "-"
		if len(chunks) > 0 {
			sort.Ints(chunks)
			p50 = strconv.Itoa(chunks[len(chunks)/2])
		}
		fmt.Printf("%-44s %10.3f %7.0fx %12.0f %14s\n", name, mb(total)/hours, float64(len(raw))/float64(total), float64(total)/(hours*3600), p50)
	}

	enc := must(zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedDefault)))
	line("whole recording, one frame (01c baseline)", len(enc.EncodeAll(raw, nil)), nil)

	for _, sec := range []int64{10, 30, 60} {
		total, sizes := 0, []int{}
		for _, c := range cut(raw, marks, sec) {
			n := len(enc.EncodeAll(c, nil))
			total += n
			sizes = append(sizes, n)
		}
		line(fmt.Sprintf("independent frame per %d s chunk", sec), total, sizes)
	}

	// One frame per 5 minutes, flushed every 10 s: each flush is shippable,
	// but decoding a byte needs the frame from its start (seek point = 5 min).
	for _, frameMin := range []int64{5, 60} {
		var out bytes.Buffer
		total, sizes := 0, []int{}
		chunks := cut(raw, marks, 10)
		per := int(frameMin * 6)
		for i := 0; i < len(chunks); i += per {
			out.Reset()
			w := must(zstd.NewWriter(&out, zstd.WithEncoderLevel(zstd.SpeedDefault)))
			prev := 0
			for j := i; j < i+per && j < len(chunks); j++ {
				must(w.Write(chunks[j]))
				check(w.Flush())
				sizes = append(sizes, out.Len()-prev)
				prev = out.Len()
			}
			check(w.Close())
			total += out.Len()
		}
		line(fmt.Sprintf("frame per %d min, flushed every 10 s", frameMin), total, sizes)
	}

	// Dictionary trained on the first 20 % of 10 s chunks, applied to the rest.
	chunks := cut(raw, marks, 10)
	split := len(chunks) / 5
	if dict := train(chunks[:split]); dict != nil {
		denc := must(zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedDefault), zstd.WithEncoderDict(dict)))
		rest, total, sizes := 0, 0, []int{}
		for _, c := range chunks[split:] {
			n := len(denc.EncodeAll(c, nil))
			rest += len(c)
			total += n
			sizes = append(sizes, n)
		}
		frac := float64(rest) / float64(len(raw))
		fmt.Printf("%-44s %10.3f %7.0fx %12.0f %14d  (dict %d B, last 80%%)\n", "independent 10 s chunk + trained dictionary",
			mb(total)/(hours*frac), float64(rest)/float64(total), float64(total)/(hours*frac*3600), sizes[len(sizes)/2], len(dict))
	}
}

// cut splits raw output into chunks of sec seconds using the idx marks.
func cut(raw []byte, marks []mark, sec int64) [][]byte {
	var out [][]byte
	start := marks[0]
	for _, m := range marks[1:] {
		if m.ms-start.ms >= sec*1000 {
			if m.off > start.off {
				out = append(out, raw[start.off:m.off])
			}
			start = m
		}
	}
	if int64(len(raw)) > start.off {
		out = append(out, raw[start.off:])
	}
	return out
}

// train uses the zstd CLI's dictionary trainer on sample chunks.
func train(samples [][]byte) []byte {
	dir := must(os.MkdirTemp("", "zdict"))
	defer os.RemoveAll(dir)
	args := []string{"--train", "-q", "--maxdict=65536", "-o", filepath.Join(dir, "dict")}
	for i, s := range samples {
		f := filepath.Join(dir, strconv.Itoa(i))
		check(os.WriteFile(f, s, 0o600))
		args = append(args, f)
	}
	if out, err := exec.Command("zstd", args...).CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "dictionary training failed: %v %s\n", err, out)
		return nil
	}
	return must(os.ReadFile(filepath.Join(dir, "dict")))
}

func mb(n int) float64 { return float64(n) / 1e6 }

func atoi(s string) int64 { v, err := strconv.ParseInt(s, 10, 64); check(err); return v }

func must[T any](v T, err error) T {
	if err != nil && err != io.EOF {
		panic(err)
	}
	return v
}

func check(err error) {
	if err != nil {
		panic(err)
	}
}
