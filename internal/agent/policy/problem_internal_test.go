package policy

import (
	"strings"
	"testing"
)

func TestAProblemIsCappedAndPrintable(t *testing.T) {
	t.Parallel()
	p := problem(CodeBadValue, 12, strings.Repeat("scope.", 200), "a reason\twith\x00control é characters")
	if len(p.Error()) > maxErrorBytes {
		t.Errorf("len = %d, want at most %d", len(p.Error()), maxErrorBytes)
	}
	for _, r := range p.Error() {
		if r < ' ' || r > '~' {
			t.Fatalf("%q has a character outside printable ASCII", p.Error())
		}
	}
	if !strings.HasPrefix(p.Error(), "line 12: scope.") {
		t.Errorf("Error() = %q", p.Error())
	}
	short := problem(CodeBadValue, 3, "k", "bad \x07 é")
	if short.Error() != "line 3: k: bad ? ?" {
		t.Errorf("Error() = %q, want control and non-ASCII characters replaced", short.Error())
	}
}
