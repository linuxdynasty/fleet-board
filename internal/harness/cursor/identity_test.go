package cursor

import (
	"strings"
	"testing"

	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
)

// The 0.22 account-identity rule (01b) as parser tests: `cursor-agent
// status` prints "Logged in as <email>", `cursor-agent about` prints the
// email on its "User Email" row, and anything without either marker fails
// with no reading. Every expectation below was worked out from the fixture
// by hand; the redacted address stands in for the owner's real one.
func TestParseIdentityFixture(t *testing.T) {
	cases := []struct {
		name    string
		output  string
		outcome typev1.ParseOutcome
		email   string
	}{
		{
			name:    "status names the signed-in email",
			output:  loadScreen(t, "status.txt"),
			outcome: typev1.ParseOutcome_PARSE_OUTCOME_OK,
			email:   "owner@example.com",
		},
		{
			name:    "about names the email on its own row",
			output:  loadScreen(t, "about.txt"),
			outcome: typev1.ParseOutcome_PARSE_OUTCOME_OK,
			email:   "owner@example.com",
		},
		{
			name:    "truncated about fails with no reading",
			output:  loadScreen(t, "identity-truncated.txt"),
			outcome: typev1.ParseOutcome_PARSE_OUTCOME_FAILED,
		},
		{
			name:    "empty output fails",
			output:  "",
			outcome: typev1.ParseOutcome_PARSE_OUTCOME_FAILED,
		},
		{
			name:    "unrelated text fails, never guesses",
			output:  "Usage • Pro+   Resets Nov 4\n",
			outcome: typev1.ParseOutcome_PARSE_OUTCOME_FAILED,
		},
		{
			name:    "login line with no address fails",
			output:  "✓ Logged in as \n",
			outcome: typev1.ParseOutcome_PARSE_OUTCOME_FAILED,
		},
		{
			name:    "a mention mid-line is not identity",
			output:  "last session Logged in as owner@example.com ended\n",
			outcome: typev1.ParseOutcome_PARSE_OUTCOME_FAILED,
		},
		{
			name:    "oversized output fails cleanly",
			output:  strings.Repeat("x", 64*1024+1),
			outcome: typev1.ParseOutcome_PARSE_OUTCOME_FAILED,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := ParseIdentity(tc.output)
			if got.ParseOutcome != tc.outcome {
				t.Fatalf("outcome = %v, want %v", got.ParseOutcome, tc.outcome)
			}
			if got.Email != tc.email {
				t.Fatalf("email = %q, want %q", got.Email, tc.email)
			}
		})
	}
}
