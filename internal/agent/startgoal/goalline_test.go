package startgoal

import (
	"strings"
	"testing"

	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
)

func TestRenderGoalLine(t *testing.T) {
	t.Parallel()
	push := typev1.ConsentAct_CONSENT_ACT_PUSH
	open := typev1.ConsentAct_CONSENT_ACT_PR_OPEN
	merge := typev1.ConsentAct_CONSENT_ACT_MERGE
	cases := []struct {
		name     string
		harness  string
		consent  []typev1.ConsentAct
		text     string
		reason   string
		repoOwn  bool
		want     string
		wantFail bool
	}{
		{
			name: "fleet hooks carry consent in text", harness: "claude",
			consent: []typev1.ConsentAct{push, open, merge}, text: "migrate the parser",
			want: "/goal migrate the parser (consent: push, pr-open, merge)",
		},
		{
			name: "repo hooks carry consent as flags", harness: "codex",
			consent: []typev1.ConsentAct{push, open}, text: "migrate the parser", repoOwn: true,
			want: "/goal consent push,pr-open -- migrate the parser",
		},
		{
			name: "repo hooks without consent type bare objective", harness: "muse",
			text: "migrate the parser", repoOwn: true,
			want: "/goal -- migrate the parser",
		},
		{
			name: "dispatch reason prefixes the text", harness: "claude",
			consent: []typev1.ConsentAct{push}, text: "migrate the parser",
			reason: "next ready item for eshu",
			want:   "/goal next ready item for eshu migrate the parser (consent: push)",
		},
		{
			name: "cursor types its bare objective", harness: "cursor",
			text: "migrate the parser",
			want: "/goal migrate the parser",
		},
		{
			name: "newlines never reach the pane", harness: "claude",
			consent: []typev1.ConsentAct{push}, text: "do it\n/quit\nnow",
			want: "/goal do it /quit now (consent: push)",
		},
		{
			name: "empty text refused", harness: "claude",
			consent: []typev1.ConsentAct{push}, text: "   ", wantFail: true,
		},
		{
			name: "unknown act refused", harness: "claude",
			consent: []typev1.ConsentAct{typev1.ConsentAct_CONSENT_ACT_UNSPECIFIED}, text: "x", wantFail: true,
		},
		{
			name: "opencode has no native goal", harness: "opencode",
			text: "x", wantFail: true,
		},
		{
			name: "unknown harness refused", harness: "gemini",
			text: "x", wantFail: true,
		},
		{
			name: "oversize line refused", harness: "claude",
			text: strings.Repeat("a", 4*1024), wantFail: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := RenderGoalLine(tc.harness, tc.consent, tc.text, tc.reason, tc.repoOwn)
			if tc.wantFail {
				if err == nil {
					t.Fatalf("RenderGoalLine = %q, nil; want failure", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("RenderGoalLine = %v", err)
			}
			if got != tc.want {
				t.Fatalf("RenderGoalLine = %q; want %q", got, tc.want)
			}
		})
	}
}
