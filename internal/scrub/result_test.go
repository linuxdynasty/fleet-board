package scrub_test

import (
	"strings"
	"testing"

	"github.com/linuxdynasty/fleet-board/internal/scrub"
)

// Scrub reports what happened, so callers can count it; Text is its Text.
func TestScrubReports(t *testing.T) {
	t.Parallel()
	aws := "AKIA" + strings.Repeat("Z", 16)
	google := "AIza" + strings.Repeat("x", 35)
	cases := []struct {
		name string
		in   string
		opt  scrub.Options
		want scrub.Result
	}{
		{"plain text", "all good", scrub.Options{MaxBytes: 64}, scrub.Result{Text: "all good"}},
		{"a secret", "TOKEN=abc", scrub.Options{MaxBytes: 64}, scrub.Result{Text: "TOKEN=[redacted]", Changed: true}},
		{"the home folder", "cd /Users/al/x", scrub.Options{MaxBytes: 64, Home: "/Users/al"}, scrub.Result{Text: "cd ~/x", Changed: true}},
		{"invalid utf-8 alone is not a change", "a\xffb", scrub.Options{MaxBytes: 64}, scrub.Result{Text: "a\uFFFDb"}},
		{"cut", "hello", scrub.Options{MaxBytes: 4}, scrub.Result{Text: "hell", Cut: true}},
		{"a secret and a cut", "id TOKEN=abc", scrub.Options{MaxBytes: 5}, scrub.Result{Text: "id ", Changed: true, Cut: true}},
		{"zero cap", "hello", scrub.Options{MaxBytes: 0}, scrub.Result{Cut: true}},
		{"zero cap, empty text", "", scrub.Options{MaxBytes: 0}, scrub.Result{}},
		// Four glued keys of alternating kinds need four changing rounds, one
		// more than the bound allows, so the field goes whole.
		{
			"unsettled", aws + google + aws + google,
			scrub.Options{MaxBytes: 256},
			scrub.Result{Text: "[redacted]", Changed: true, Unsettled: true},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := scrub.Scrub(tc.in, tc.opt)
			if got != tc.want {
				t.Errorf("Scrub(%q, %+v)\n got %+v\nwant %+v", tc.in, tc.opt, got, tc.want)
			}
			if text := scrub.Text(tc.in, tc.opt); text != got.Text {
				t.Errorf("Text(%q) = %q, Scrub gave %q", tc.in, text, got.Text)
			}
		})
	}
}
