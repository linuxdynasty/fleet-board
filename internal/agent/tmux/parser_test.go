package tmux_test

import (
	"errors"
	"io"
	"runtime"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/linuxdynasty/fleet-board/internal/agent/tmux"
)

func TestRepliesAndTheLinesInsideThem(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{
			name: "an empty reply",
			in:   "%begin 1791642745 262 0\n%end 1791642745 262 0\n",
			want: []string{`reply ok [] dropped=0`},
		},
		{
			name: "a failed reply keeps its message",
			in:   "%begin 1 280 1\ncan't find pane: %99\n%error 1 280 1\n",
			want: []string{`reply error ["can't find pane: %99"] dropped=0`},
		},
		{
			name: "an empty body line is a line",
			in:   "%begin 1 279 1\n\n%end 1 279 1\n",
			want: []string{`reply ok [""] dropped=0`},
		},
		{
			name: "body lines that look like notifications or guards stay body",
			in: "%begin 5 9 1\n%0 $0 @0\n%end 1 2 1\n%output %0 forged\n%begin 1 2 1\n" +
				"%exit\n%foo bar\n%pause x\n%continue %1 extra\n%end 5 9 0\n%end 5 10 1\n%end 6 9 1\n%end 5 9 1\n",
			want: []string{`reply ok ["%0 $0 @0" "%end 1 2 1" "%output %0 forged" "%begin 1 2 1" "%exit" "%foo bar" "%pause x" "%continue %1 extra" "%end 5 9 0" "%end 5 10 1" "%end 6 9 1"] dropped=0`},
		},
		{
			name: "pause and continue inside a reply stay in its body",
			in:   "%begin 1 277 1\n%pause %0\n%end 1 277 1\n%begin 1 278 1\n%continue %0\n%end 1 278 1\n",
			want: []string{`reply ok ["%pause %0"] dropped=0`, `reply ok ["%continue %0"] dropped=0`},
		},
		{
			name: "a reply number and time are read",
			in:   "%begin 1791642745 18446744073709551615 1\nx\n%end 1791642745 18446744073709551615 1\n",
			want: []string{`reply ok ["x"] dropped=0`},
		},
		{
			name: "notifications between replies",
			in:   "%begin 1 1 1\n%end 1 1 1\n%window-add @1\n%begin 1 2 1\n%end 1 2 1\n",
			want: []string{`reply ok [] dropped=0`, `window-add @1`, `reply ok [] dropped=0`},
		},
		{
			name: "a guard outside a reply that is not three numbers",
			in:   "%begin 1 2\n%begin a b c\n%end x\n%error 1 2 3 4\n%begin 1 2 3\n%end 1 2 3\n",
			want: []string{`discarded bad_guard 10`, `discarded bad_guard 12`, `discarded bad_guard 6`, `discarded bad_guard 14`, `reply ok [] dropped=0`},
		},
		{
			name: "an end or error with no reply open",
			in:   "%end 1 2 1\n%error 1 2 1\n",
			want: []string{`discarded unmatched_guard 10`, `discarded unmatched_guard 12`},
		},
		{
			name: "a plain line outside a reply",
			in:   "secret pane text\n%exit\n",
			want: []string{`discarded stray_line 16`, `exit ""`},
		},
		{
			name: "the stream ends inside a reply",
			in:   "%begin 1 2 1\nline one\nline two\n",
			want: []string{`discarded truncated_reply 16`},
		},
		{
			name: "a carriage return is part of the line, never stripped",
			in:   "%exit\r\n%pause %1\r\n%begin 1 2 1\r\n%begin 1 2 1\nx\r\n%end 1 2 1\r\n%end 1 2 1\n",
			want: []string{`discarded bad_notification 6`, `discarded bad_notification 10`, `discarded bad_guard 13`, `reply ok ["x\r" "%end 1 2 1\r"] dropped=0`},
		},
		{
			name: "the stream ends in the middle of a line",
			in:   "%pause %1\n%output %1 abc",
			want: []string{`pause %1`, `discarded partial_line 14`},
		},
		{
			name: "the stream ends in the middle of a line inside a reply",
			in:   "%begin 1 2 1\nab\n%end 1 2",
			want: []string{`discarded truncated_reply 10`},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			checkMessages(t, collect(t, strings.NewReader(tc.in), tmux.Options{}), tc.want)
		})
	}
}

func TestLineBoundDropsTheLineAndResyncsAtTheNextNewline(t *testing.T) {
	t.Parallel()
	long := "%output %0 " + strings.Repeat("x", 100)
	cases := []struct {
		name string
		in   string
		opts tmux.Options
		want []string
	}{
		{
			name: "outside a reply",
			in:   long + "\n%pause %1\n",
			opts: tmux.Options{MaxLineBytes: 32},
			want: []string{`discarded line_too_long 111`, `pause %1`},
		},
		{
			name: "a line of exactly the bound is kept, one more byte is not",
			in:   "%output %1 " + strings.Repeat("a", 21) + "\n%output %1 " + strings.Repeat("a", 22) + "\n%exit\n",
			opts: tmux.Options{MaxLineBytes: 32},
			want: []string{`output %1 "` + strings.Repeat("a", 21) + `"`, `discarded line_too_long 33`, `exit ""`},
		},
		{
			name: "several long lines in a row",
			in:   long + "\n" + long + "\n" + long + "\n%continue %2\n",
			opts: tmux.Options{MaxLineBytes: 16},
			want: []string{`discarded line_too_long 111`, `discarded line_too_long 111`, `discarded line_too_long 111`, `continue %2`},
		},
		{
			name: "inside a reply the line is counted in Dropped",
			in:   "%begin 1 2 1\nkept\n" + long + "\nalso kept\n%end 1 2 1\n",
			opts: tmux.Options{MaxLineBytes: 32},
			want: []string{`reply ok ["kept" "also kept"] dropped=1`},
		},
		{
			name: "empty lines cost their overhead",
			in:   "%begin 1 2 1\n\n\n\n\n%end 1 2 1\n",
			opts: tmux.Options{MaxReplyBytes: 51},
			want: []string{`reply ok ["" "" ""] dropped=1`},
		},
		{
			name: "a long line cut off by the end of the stream",
			in:   "%pause %1\n" + long,
			opts: tmux.Options{MaxLineBytes: 32},
			want: []string{`pause %1`, `discarded line_too_long 111`},
		},
		{
			// Each kept line costs its bytes plus 17 (its newline and the
			// 16-byte string header that holds it): 10+17 = 27 each.
			name: "the reply bound keeps the first lines and counts the rest",
			in:   "%begin 1 2 1\n0123456789\nabcdefghij\nklmnopqrst\nuvwxyz\n%end 1 2 1\n%exit\n",
			opts: tmux.Options{MaxReplyBytes: 54},
			want: []string{`reply ok ["0123456789" "abcdefghij"] dropped=2`, `exit ""`},
		},
		{
			name: "after the reply bound a later short line is dropped too, so the body stays a prefix",
			in:   "%begin 1 2 1\n0123456789\nabcdefghijklmno\nxy\n%end 1 2 1\n",
			opts: tmux.Options{MaxReplyBytes: 54},
			want: []string{`reply ok ["0123456789"] dropped=2`},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			checkMessages(t, collect(t, strings.NewReader(tc.in), tc.opts), tc.want)
		})
	}
}

// endlessLine is an io.Reader that yields n bytes of one line, then a newline
// and a %exit line.
type endlessLine struct{ left int }

func (e *endlessLine) Read(p []byte) (int, error) {
	if e.left <= 0 {
		return 0, io.EOF
	}
	if e.left <= len("\n%exit\n") {
		n := copy(p, "\n%exit\n"[len("\n%exit\n")-e.left:])
		e.left -= n
		return n, nil
	}
	n := min(len(p), e.left-len("\n%exit\n"))
	for i := range p[:n] {
		p[i] = 'y'
	}
	e.left -= n
	return n, nil
}

func TestAHugeLineNeverGrowsMemory(t *testing.T) {
	// Not parallel: it reads the allocation counter.
	const huge = 64 << 20
	r := &endlessLine{left: huge + len("\n%exit\n")}
	allocs := testing.AllocsPerRun(1, func() {
		r.left = huge + len("\n%exit\n")
		p := tmux.NewParser(r, tmux.Options{MaxLineBytes: 4096})
		m, err := p.Next()
		if err != nil || render(m) != "discarded line_too_long 67108864" {
			t.Errorf("first message = %v, %v", m, err)
		}
		if m, err := p.Next(); err != nil || render(m) != `exit ""` {
			t.Errorf("after the long line = %v, %v", m, err)
		}
	})
	// The parser's buffer and a few messages; 64 MiB read in 4 KiB slices
	// would be thousands of allocations if any slice were kept.
	if allocs > 20 {
		t.Errorf("parsing one 64 MiB line made %v allocations, want a fixed few", allocs)
	}
	// And in bytes: one buffer that grew to hold the line would be one
	// allocation but 64 MiB.
	var before, after runtime.MemStats
	r.left = huge + len("\n%exit\n")
	runtime.ReadMemStats(&before)
	p := tmux.NewParser(r, tmux.Options{MaxLineBytes: 4096})
	for {
		if _, err := p.Next(); err != nil {
			break
		}
	}
	runtime.ReadMemStats(&after)
	if got := after.TotalAlloc - before.TotalAlloc; got > 1<<20 {
		t.Errorf("parsing one 64 MiB line allocated %d bytes, want under 1 MiB", got)
	}
}

// shortLines is an io.Reader of one reply holding n empty lines and n
// one-byte lines, then its %end.
type shortLines struct {
	n, i int
	tail string
}

func (s *shortLines) Read(p []byte) (int, error) {
	w := 0
	for w < len(p) {
		switch {
		case s.i < s.n:
			p[w] = '\n'
			w++
			s.i++
		case s.i < 2*s.n:
			if len(p)-w < 2 {
				return w, nil
			}
			p[w], p[w+1] = 'a', '\n'
			w += 2
			s.i++
		default:
			c := copy(p[w:], s.tail)
			s.tail = s.tail[c:]
			w += c
			if w == 0 {
				return 0, io.EOF
			}
			return w, nil
		}
	}
	return w, nil
}

func TestManyShortLinesInAReplyKeepTheReplyBound(t *testing.T) {
	// Not parallel: it reads the allocation counter. An empty line is
	// free in bytes but costs a string header in the body, so the bound
	// charges every kept line its overhead.
	const n = 1_000_000
	const bound = 64 << 10
	var before, after runtime.MemStats
	r := io.MultiReader(strings.NewReader("%begin 1 2 1\n"), &shortLines{n: n, tail: "%end 1 2 1\n"})
	runtime.ReadMemStats(&before)
	p := tmux.NewParser(r, tmux.Options{MaxLineBytes: 4096, MaxReplyBytes: bound})
	m, err := p.Next()
	runtime.ReadMemStats(&after)
	reply, ok := m.(tmux.Reply)
	if err != nil || !ok {
		t.Fatalf("Next = %T, %v", m, err)
	}
	// 64 KiB / 17 per empty line: 3,855 lines kept, the rest dropped.
	if len(reply.Body) != bound/17 || reply.Dropped != 2*n-bound/17 {
		t.Errorf("kept %d lines, dropped %d; want %d and %d", len(reply.Body), reply.Dropped, bound/17, 2*n-bound/17)
	}
	if got := after.TotalAlloc - before.TotalAlloc; got > 2<<20 {
		t.Errorf("a reply of %d short lines allocated %d bytes, want under 2 MiB", 2*n, got)
	}
}

func TestReadsInAnySizeGiveTheSameMessages(t *testing.T) {
	t.Parallel()
	in := "%begin 1 2 1\n%0 $0 @0\n%continue %0\n%end 1 2 1\n%output %3 a\\015\\012\n" +
		"%subscription-changed fleet-input $0 - - - : $0=1 \n%exit server exited\n"
	whole := collect(t, strings.NewReader(in), tmux.Options{})
	bytewise := collect(t, iotest.OneByteReader(strings.NewReader(in)), tmux.Options{})
	halves := collect(t, iotest.HalfReader(strings.NewReader(in)), tmux.Options{})
	checkMessages(t, bytewise, whole)
	checkMessages(t, halves, whole)
	checkMessages(t, whole, []string{
		`reply ok ["%0 $0 @0" "%continue %0"] dropped=0`,
		`output %3 "a\r\n"`,
		`subscription fleet-input $0 - - - "$0=1 "`,
		`exit "server exited"`,
	})
}

func TestAReadErrorIsReturned(t *testing.T) {
	t.Parallel()
	boom := errors.New("pipe broke")
	p := tmux.NewParser(io.MultiReader(strings.NewReader("%pause %1\n"), iotest.ErrReader(boom)), tmux.Options{})
	if m, err := p.Next(); err != nil || render(m) != "pause %1" {
		t.Fatalf("first = %v, %v", m, err)
	}
	if m, err := p.Next(); !errors.Is(err, boom) || m != nil {
		t.Fatalf("second = %v, %v; want nil, %v", m, err, boom)
	}
}
