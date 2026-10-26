package preflight

import (
	"context"
	"errors"
	"testing"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
)

// stubReader is the fake Reader seam: canned Facts, no I/O, no keys.
type stubReader struct {
	facts Facts
	err   error
}

func (s stubReader) Facts(context.Context, *agentv1.GoalSpec) (Facts, error) {
	return s.facts, s.err
}

// countTyper is the fake key-typing path: it records every call so the
// tests prove Check never types a key on any path.
type countTyper struct{ calls int }

func (t *countTyper) TypeKeys(context.Context, string) error {
	t.calls++
	return nil
}

func testSpec() *agentv1.GoalSpec {
	return &agentv1.GoalSpec{
		Harness: &typev1.Harness{Name: "Muse"},
		Repo:    &typev1.RepoRef{Host: "github.com", Path: "acme/api"},
	}
}

func goodFacts() Facts {
	return Facts{
		HarnessSignedIn: true,
		CheckoutFound:   true,
		PushKnown:       true,
		PushAccess:      true,
		DiskKnown:       true,
		DiskFreeBytes:   20 * 1024 * 1024 * 1024,
	}
}

func reasons(res *agentv1.PreflightResult) []typev1.ReasonCode {
	var out []typev1.ReasonCode
	for _, f := range res.GetFailedChecks() {
		out = append(out, f.GetReason())
	}
	return out
}

func sameReasons(t *testing.T, got []typev1.ReasonCode, want []typev1.ReasonCode) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("reasons = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("reasons = %v, want %v", got, want)
		}
	}
}

func TestCheck(t *testing.T) {
	t.Parallel()
	FiveGiB := uint64(5 * 1024 * 1024 * 1024)
	cases := []struct {
		name      string
		facts     Facts
		wantOK    bool
		wantWhy   []typev1.ReasonCode
		wantRetry []bool
	}{
		{
			name:    "all clear passes",
			facts:   goodFacts(),
			wantOK:  true,
			wantWhy: nil,
		},
		{
			name: "harness not signed in",
			facts: func() Facts {
				f := goodFacts()
				f.HarnessSignedIn = false
				return f
			}(),
			wantOK:    false,
			wantWhy:   []typev1.ReasonCode{typev1.ReasonCode_REASON_CODE_HARNESS_NOT_SIGNED_IN},
			wantRetry: []bool{false},
		},
		{
			name: "checkout missing",
			facts: func() Facts {
				f := goodFacts()
				f.CheckoutFound = false
				return f
			}(),
			wantOK:    false,
			wantWhy:   []typev1.ReasonCode{typev1.ReasonCode_REASON_CODE_CHECKOUT_MISSING},
			wantRetry: []bool{false},
		},
		{
			name: "push access missing",
			facts: func() Facts {
				f := goodFacts()
				f.PushAccess = false
				return f
			}(),
			wantOK:    false,
			wantWhy:   []typev1.ReasonCode{typev1.ReasonCode_REASON_CODE_PUSH_ACCESS_MISSING},
			wantRetry: []bool{false},
		},
		{
			name: "folder untrusted",
			facts: func() Facts {
				f := goodFacts()
				f.FolderUntrusted = true
				return f
			}(),
			wantOK:    false,
			wantWhy:   []typev1.ReasonCode{typev1.ReasonCode_REASON_CODE_FOLDER_UNTRUSTED},
			wantRetry: []bool{false},
		},
		{
			name: "disk low",
			facts: func() Facts {
				f := goodFacts()
				f.DiskFreeBytes = FiveGiB - 1
				return f
			}(),
			wantOK:    false,
			wantWhy:   []typev1.ReasonCode{typev1.ReasonCode_REASON_CODE_DISK_LOW},
			wantRetry: []bool{true},
		},
		{
			name: "disk exactly at the point still passes",
			facts: func() Facts {
				f := goodFacts()
				f.DiskFreeBytes = FiveGiB
				return f
			}(),
			wantOK:  true,
			wantWhy: nil,
		},
		{
			name: "unknown push access is skipped never failed",
			facts: func() Facts {
				f := goodFacts()
				f.PushKnown = false
				f.PushAccess = false
				return f
			}(),
			wantOK:  true,
			wantWhy: nil,
		},
		{
			name: "unknown disk is skipped never failed",
			facts: func() Facts {
				f := goodFacts()
				f.DiskKnown = false
				f.DiskFreeBytes = 0
				return f
			}(),
			wantOK:  true,
			wantWhy: nil,
		},
		{
			name: "every failure reported in fixed order",
			facts: Facts{
				FolderUntrusted: true,
				DiskKnown:       true,
				PushKnown:       true,
			},
			wantOK: false,
			wantWhy: []typev1.ReasonCode{
				typev1.ReasonCode_REASON_CODE_HARNESS_NOT_SIGNED_IN,
				typev1.ReasonCode_REASON_CODE_CHECKOUT_MISSING,
				typev1.ReasonCode_REASON_CODE_PUSH_ACCESS_MISSING,
				typev1.ReasonCode_REASON_CODE_FOLDER_UNTRUSTED,
				typev1.ReasonCode_REASON_CODE_DISK_LOW,
			},
			wantRetry: []bool{false, false, false, false, true},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			typer := &countTyper{}
			c := &Checker{Reader: stubReader{facts: tc.facts}, Typer: typer}
			res, err := c.Check(context.Background(), testSpec())
			if err != nil {
				t.Fatalf("Check error = %v", err)
			}
			if res.GetOk() != tc.wantOK {
				t.Errorf("ok = %v, want %v", res.GetOk(), tc.wantOK)
			}
			sameReasons(t, reasons(res), tc.wantWhy)
			for i, want := range tc.wantRetry {
				if res.GetFailedChecks()[i].GetRetryable() != want {
					t.Errorf("failed_checks[%d].retryable = %v, want %v", i,
						res.GetFailedChecks()[i].GetRetryable(), want)
				}
				if res.GetFailedChecks()[i].GetDetail() == "" {
					t.Errorf("failed_checks[%d].detail is empty", i)
				}
			}
			if typer.calls != 0 {
				t.Errorf("typed %d key lines, want none", typer.calls)
			}
		})
	}
}

// TestRefusalTypesNothing walks every refusal path and proves none types
// a key: the plan row 21 gate for I1.
func TestRefusalTypesNothing(t *testing.T) {
	t.Parallel()
	combos := []Facts{
		{},
		goodFacts(),
		{DiskKnown: true},
		{PushKnown: true, PushAccess: true, DiskKnown: true, DiskFreeBytes: 1},
		{FolderUntrusted: true},
		{HarnessSignedIn: true, CheckoutFound: true, PushKnown: true, FolderUntrusted: true},
	}
	for i, facts := range combos {
		typer := &countTyper{}
		c := &Checker{Reader: stubReader{facts: facts}, Typer: typer}
		if _, err := c.Check(context.Background(), testSpec()); err != nil {
			t.Fatalf("combo %d: Check error = %v", i, err)
		}
		if typer.calls != 0 {
			t.Fatalf("combo %d: typed %d key lines, want none", i, typer.calls)
		}
	}
}

func TestCheckReaderError(t *testing.T) {
	t.Parallel()
	c := &Checker{Reader: stubReader{err: errors.New("screen read failed")}, Typer: &countTyper{}}
	if _, err := c.Check(context.Background(), testSpec()); err == nil {
		t.Fatal("Check with a failing Reader = nil error, want an error")
	}
}

func TestCheckNilSpec(t *testing.T) {
	t.Parallel()
	c := &Checker{Reader: stubReader{facts: goodFacts()}, Typer: &countTyper{}}
	if _, err := c.Check(context.Background(), nil); err == nil {
		t.Fatal("Check with nil spec = nil error, want an error")
	}
}
