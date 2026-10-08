package backup

import (
	"errors"
	"io"
	"strings"
	"testing"
)

// readLine reads the passphrase: one line, nothing past it, a trailing
// carriage return dropped, at most maxPassphrase (1024) bytes.
func TestReadLine(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("a", 1024)
	for _, tc := range []struct {
		name, in, want, unread string
		wantErr                error // nil: success
		anyErr                 bool  // an error that is not a sentinel
	}{
		{name: "stops at the newline", in: "secret\nnext line", want: "secret", unread: "next line"},
		{name: "drops a trailing carriage return", in: "secret\r\nrest", want: "secret", unread: "rest"},
		{name: "keeps a carriage return inside", in: "se\rcret\n", want: "se\rcret"},
		{name: "an empty line", in: "\nrest", want: "", unread: "rest"},
		{name: "1024 bytes is the limit", in: long + "\n", want: long},
		{name: "1025 bytes is refused", in: long + "b\n", anyErr: true},
		{name: "a partial line then EOF", in: "secret", want: "secret"},
		{name: "nothing then EOF", in: "", wantErr: io.EOF},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := strings.NewReader(tc.in)
			got, err := readLine(r)
			switch {
			case tc.anyErr:
				if err == nil {
					t.Fatalf("readLine = %d bytes, want an error", len(got))
				}
				if got != nil {
					t.Errorf("readLine returned %d bytes with its error", len(got))
				}
				return
			case tc.wantErr != nil:
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("readLine error = %v, want %v", err, tc.wantErr)
				}
				return
			case err != nil:
				t.Fatalf("readLine: %v", err)
			}
			if string(got) != tc.want {
				t.Errorf("readLine = %q, want %q", got, tc.want)
			}
			rest, _ := io.ReadAll(r)
			if string(rest) != tc.unread {
				t.Errorf("left %q unread, want %q", rest, tc.unread)
			}
		})
	}
}
