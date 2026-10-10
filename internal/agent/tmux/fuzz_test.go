package tmux_test

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"slices"
	"testing"
	"testing/iotest"

	"github.com/linuxdynasty/fleet-board/internal/agent/tmux"
)

const (
	fuzzMaxLine  = 64
	fuzzMaxReply = 256
)

var errKinds = []tmux.ErrKind{
	tmux.ErrLineTooLong, tmux.ErrPartialLine, tmux.ErrStrayLine, tmux.ErrBadGuard, tmux.ErrUnmatchedGuard,
	tmux.ErrBadNotification, tmux.ErrBadEscape, tmux.ErrTruncatedReply, tmux.ErrReplyTooLarge,
}

// parseAll parses data to its end, failing on a stream that never ends.
func parseAll(t *testing.T, r io.Reader, lines int) []tmux.Message {
	t.Helper()
	p := tmux.NewParser(r, tmux.Options{MaxLineBytes: fuzzMaxLine, MaxReplyBytes: fuzzMaxReply})
	var out []tmux.Message
	for {
		m, err := p.Next()
		if errors.Is(err, io.EOF) {
			return out
		}
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		out = append(out, m)
		// Every message uses up at least one line, except the one
		// truncated reply or partial line at the end.
		if len(out) > lines+2 {
			t.Fatalf("%d messages from %d lines", len(out), lines)
		}
	}
}

// FuzzParse holds the parser's promises for any input: it ends, it keeps
// every bound, it never makes bytes up, it gives the same messages whatever
// the read sizes, and every failure has a class from the closed set.
func FuzzParse(f *testing.F) {
	for _, v := range transcriptVersions {
		f.Add(readTranscript(f, v))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		lines := bytes.Count(data, []byte("\n"))
		msgs := parseAll(t, bytes.NewReader(data), lines)
		emitted := 0
		for _, m := range msgs {
			switch v := m.(type) {
			case tmux.Reply:
				body := 0
				for _, l := range v.Body {
					if len(l) > fuzzMaxLine || bytes.IndexByte([]byte(l), '\n') >= 0 || !bytes.Contains(data, []byte(l)) {
						t.Fatalf("body line %q breaks a bound or is not in the input", l)
					}
					body += len(l)
				}
				if body > fuzzMaxReply || v.Dropped < 0 {
					t.Fatalf("reply body %d bytes, dropped %d", body, v.Dropped)
				}
				emitted += body
			case tmux.Output:
				if len(v.Data) > fuzzMaxLine || v.Pane < 0 || v.Age < 0 {
					t.Fatalf("output %+v breaks a bound", v)
				}
				emitted += len(v.Data)
			case tmux.SubscriptionChanged:
				if v.Name == "" || !bytes.Contains(data, []byte(v.Value)) {
					t.Fatalf("subscription %+v is not from the input", v)
				}
				emitted += len(v.Name) + len(v.Value)
			case tmux.Exit:
				emitted += len(v.Reason)
			case tmux.Unknown:
				if v.Name == "" || len(v.Name) > 64 || !bytes.Contains(data, []byte("%"+v.Name)) {
					t.Fatalf("unknown name %q", v.Name)
				}
			case tmux.Discarded:
				if !slices.Contains(errKinds, v.ErrKind) || v.Bytes < 0 || v.Bytes > len(data) {
					t.Fatalf("discarded %+v", v)
				}
			case tmux.WindowRenamed:
				if v.Window < 0 || !bytes.Contains(data, []byte(v.Name)) {
					t.Fatalf("window renamed %+v is not from the input", v)
				}
				emitted += len(v.Name)
			case tmux.SessionChanged:
				if v.Session < 0 || !bytes.Contains(data, []byte(v.Name)) {
					t.Fatalf("session changed %+v is not from the input", v)
				}
				emitted += len(v.Name)
			case tmux.WindowAdd:
				if v.Window < 0 {
					t.Fatalf("window add %+v", v)
				}
			case tmux.WindowClose:
				if v.Window < 0 || !v.Unlinked {
					t.Fatalf("window close %+v", v)
				}
			case tmux.Pause, tmux.Continue:
			default:
				t.Fatalf("unexpected message %T", m)
			}
		}
		if emitted > len(data) {
			t.Fatalf("emitted %d bytes from %d", emitted, len(data))
		}
		again := parseAll(t, iotest.OneByteReader(bytes.NewReader(data)), lines)
		if fmt.Sprint(again) != fmt.Sprint(msgs) {
			t.Fatalf("one-byte reads differ:\n%v\n%v", again, msgs)
		}
	})
}

// encodeOutput escapes bytes as tmux does for %output: below space and the
// backslash as \ooo, everything else as it is.
func encodeOutput(b []byte) []byte {
	var out []byte
	for _, c := range b {
		if c < ' ' || c == '\\' {
			out = fmt.Appendf(out, "\\%03o", c)
			continue
		}
		out = append(out, c)
	}
	return out
}

// FuzzOutputRoundTrip: any bytes a pane writes, escaped as tmux escapes them,
// come back unchanged, in %output and %extended-output alike.
func FuzzOutputRoundTrip(f *testing.F) {
	f.Add([]byte("tab\there back\\slash \x1b[1mbold\x1b[0m café\r\n"))
	f.Add([]byte{0, 1, 0x1f, '\\', 0x7f, 0x80, 0xff})
	f.Fuzz(func(t *testing.T, pane []byte) {
		enc := encodeOutput(pane)
		in := "%output %3 " + string(enc) + "\n%extended-output %3 7 : " + string(enc) + "\n"
		p := tmux.NewParser(bytes.NewReader([]byte(in)), tmux.Options{})
		for _, extended := range []bool{false, true} {
			m, err := p.Next()
			o, ok := m.(tmux.Output)
			if err != nil || !ok || o.Extended != extended || o.Pane != 3 || !bytes.Equal(o.Data, pane) {
				t.Fatalf("got %#v, %v; want %q (extended %v)", m, err, pane, extended)
			}
		}
	})
}
