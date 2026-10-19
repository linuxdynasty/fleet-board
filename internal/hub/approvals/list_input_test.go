package approvals_test

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/approvals"
)

// TestValidateListInputKeepsListErrors checks the public input contract
// without reading a page for validation. List remains the source of tokens.
func TestValidateListInputKeepsListErrors(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := newStore(t)
	if err := s.Write(ctx, func(tx *sql.Tx) error {
		for i := 0; i < 2; i++ {
			in := confirmIn()
			in.ID = ""
			if _, err := approvals.CreateApproval(ctx, tx, in); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var token string
	if err := s.Read(ctx, func(tx *sql.Tx) error {
		_, next, err := approvals.List(ctx, tx, approvals.Filter{PageSize: 1}, t0)
		token = next
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if token == "" {
		t.Fatal("first page has no cursor")
	}
	for _, tc := range []struct {
		name    string
		in      approvals.Filter
		now     time.Time
		want    error
		message string
	}{
		{name: "valid cursor", in: approvals.Filter{PageSize: 1, PageToken: token}, now: t0},
		{name: "default page", in: approvals.Filter{}, now: t0},
		{name: "capped page", in: approvals.Filter{PageSize: 5000}, now: t0},
		{name: "missing time", in: approvals.Filter{PageSize: 1, PageToken: token}, want: approvals.ErrInvalid},
		{name: "garbled cursor", in: approvals.Filter{PageSize: 1, PageToken: "garbled"}, now: t0, want: approvals.ErrInvalid},
		{name: "wrong page size", in: approvals.Filter{PageSize: 2, PageToken: token}, now: t0, want: approvals.ErrInvalid},
		{name: "expired cursor", in: approvals.Filter{PageSize: 1, PageToken: token}, now: t0.Add(11 * time.Minute), want: approvals.ErrInvalid},
		{name: "invalid state", in: approvals.Filter{HasState: true, State: hubv1.ApprovalState(99)}, now: t0, want: approvals.ErrInvalid},
		{name: "invalid kind", in: approvals.Filter{HasKind: true, Kind: hubv1.ApprovalKind(99)}, now: t0, want: approvals.ErrInvalid},
		{name: "cursor checked first", in: approvals.Filter{PageSize: 1, PageToken: "bad-cursor", HasState: true, State: hubv1.ApprovalState(99)}, now: t0, want: approvals.ErrInvalid, message: "bad page_token"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			inputErr := approvals.ValidateListInput(tc.in, tc.now)
			if !errors.Is(inputErr, tc.want) || (tc.want == nil && inputErr != nil) {
				t.Fatalf("ValidateListInput error = %v, want %v", inputErr, tc.want)
			}
			if tc.message != "" && !strings.Contains(inputErr.Error(), tc.message) {
				t.Errorf("ValidateListInput error = %v, want %q", inputErr, tc.message)
			}
			if err := s.Read(ctx, func(tx *sql.Tx) error {
				_, _, err := approvals.List(ctx, tx, tc.in, tc.now)
				return err
			}); !errors.Is(err, tc.want) || (tc.want == nil && err != nil) {
				t.Fatalf("List error = %v, want %v", err, tc.want)
			} else if tc.message != "" && !strings.Contains(err.Error(), tc.message) {
				t.Errorf("List error = %v, want %q", err, tc.message)
			}
		})
	}
}
