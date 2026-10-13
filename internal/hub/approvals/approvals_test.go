package approvals_test

import (
	"bytes"
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/approvals"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
	"github.com/linuxdynasty/fleet-board/internal/store"
)

var t0 = time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)

// newStore is a real state.db in t.TempDir().
func newStore(t *testing.T) *store.Store {
	t.Helper()
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "state")
	if err := statedb.EnsureDir(dir); err != nil {
		t.Fatal(err)
	}
	id := statedb.Identity{
		FleetID: "fleet-1", CACert: []byte("ca"), CAKey: []byte("key"), ServerCert: []byte("cert"),
		ServerKey: []byte("skey"), ServerNames: []string{"hub"}, AccountSalt: bytes.Repeat([]byte{7}, 32),
		SaltVersion: 1, SettingsEpoch: "epoch-1", CreatedTime: t0,
	}
	if err := statedb.Create(ctx, dir, id, statedb.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	s, err := statedb.Open(ctx, dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// confirmIn is a COMMAND_CONFIRM approval: KillWindow on a developer window,
// with the gated command stored and nothing sent.
func confirmIn() approvals.Create {
	return approvals.Create{
		Kind:      hubv1.ApprovalKind_APPROVAL_KIND_COMMAND_CONFIRM,
		Severity:  hubv1.Severity_SEVERITY_WARNING,
		Preview:   "kill window dev:3",
		MachineID: "m1",
		Allowed:   []approvals.Choice{{Key: "approve"}, {Key: "deny"}},
		Command: &agentv1.Command{
			Kind: &agentv1.Command_KillWindow{KillWindow: &agentv1.KillWindow{
				Window: &typev1.WindowKey{Server: "dev", ServerStartId: "s9", WindowId: "3"},
			}},
		},
		DefaultChoice: "deny",
		Fallback:      hubv1.ApprovalFallback_APPROVAL_FALLBACK_DENY,
		ExpireTime:    t0.Add(30 * time.Minute),
		RequiredRole:  typev1.Role_ROLE_OWNER,
		Now:           t0,
	}
}

func TestCreateGet(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	ctx := context.Background()
	var got *hubv1.Approval
	err := s.Write(ctx, func(tx *sql.Tx) error {
		a, err := approvals.CreateApproval(ctx, tx, confirmIn())
		if err != nil {
			return err
		}
		got, err = approvals.Get(ctx, tx, a.GetId().GetValue(), t0)
		return err
	})
	if err != nil {
		t.Fatalf("create+get: %v", err)
	}
	if got.GetVersion() != 1 {
		t.Fatalf("version = %d, want 1", got.GetVersion())
	}
	if got.GetState() != hubv1.ApprovalState_APPROVAL_STATE_PENDING {
		t.Fatalf("state = %v, want PENDING", got.GetState())
	}
	if got.GetPreview() != "kill window dev:3" {
		t.Fatalf("preview = %q", got.GetPreview())
	}
	if got.GetId().GetValue() == "" {
		t.Fatal("id is empty")
	}
}

// TestStoredCommandRoundTrip: the gated command is stored marshaled and
// comes back identical, so DecideApproval sends exactly what was gated.
func TestStoredCommandRoundTrip(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	ctx := context.Background()
	id := ""
	err := s.Write(ctx, func(tx *sql.Tx) error {
		a, err := approvals.CreateApproval(ctx, tx, confirmIn())
		if err != nil {
			return err
		}
		id = a.GetId().GetValue()
		cmd, err := approvals.StoredCommand(ctx, tx, id)
		if err != nil {
			return err
		}
		if cmd.GetKillWindow().GetWindow().GetWindowId() != "3" {
			t.Fatalf("stored command kills window %q, want 3",
				cmd.GetKillWindow().GetWindow().GetWindowId())
		}
		return nil
	})
	if err != nil {
		t.Fatalf("round trip: %v", err)
	}
	if id == "" {
		t.Fatal("no id")
	}
}

// TestAbsentFieldsStayAbsent: a confirm carries no pane, session, goal,
// triage item or operation, so the read-back message leaves them unset
// instead of returning empty values.
func TestAbsentFieldsStayAbsent(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	ctx := context.Background()
	var id string
	if err := s.Write(ctx, func(tx *sql.Tx) error {
		a, err := approvals.CreateApproval(ctx, tx, confirmIn())
		if err != nil {
			return err
		}
		id = a.GetId().GetValue()
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var got *hubv1.Approval
	if err := s.Read(ctx, func(tx *sql.Tx) error {
		var err error
		got, err = approvals.Get(ctx, tx, id, t0)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if got.GetMachine() == nil {
		t.Error("machine is unset, want m1")
	}
	for name, absent := range map[string]bool{
		"session": got.GetSession() == nil, "pane": got.GetPane() == nil,
		"goal": got.GetGoal() == nil, "triage_item": got.GetTriageItem() == nil,
		"operation": got.GetOperation() == nil,
	} {
		if !absent {
			t.Errorf("%s is set, want absent", name)
		}
	}
}

func TestListFiltersState(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	ctx := context.Background()
	err := s.Write(ctx, func(tx *sql.Tx) error {
		if _, err := approvals.CreateApproval(ctx, tx, confirmIn()); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	var list []*hubv1.Approval
	var next string
	err = s.Read(ctx, func(tx *sql.Tx) error {
		var err error
		list, next, err = approvals.List(ctx, tx, approvals.Filter{
			State: hubv1.ApprovalState_APPROVAL_STATE_PENDING, HasState: true, PageSize: 10,
		}, t0)
		return err
	})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("pending list = %d approvals, want 1", len(list))
	}
	if next != "" {
		t.Fatalf("next page token = %q on the last page, want empty", next)
	}
	err = s.Read(ctx, func(tx *sql.Tx) error {
		var err error
		list, next, err = approvals.List(ctx, tx, approvals.Filter{
			State: hubv1.ApprovalState_APPROVAL_STATE_DECIDED, HasState: true, PageSize: 10,
		}, t0)
		return err
	})
	if err != nil {
		t.Fatalf("list decided: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("decided list = %d approvals, want 0", len(list))
	}
}

// TestListPages: 51 approvals read back as 50 plus a token, then 1,
// with no id seen twice.
func TestListPages(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	ctx := context.Background()
	err := s.Write(ctx, func(tx *sql.Tx) error {
		for i := 0; i < 51; i++ {
			in := confirmIn()
			in.ID = ""
			if _, err := approvals.CreateApproval(ctx, tx, in); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	var first []*hubv1.Approval
	var token string
	err = s.Read(ctx, func(tx *sql.Tx) error {
		var err error
		first, token, err = approvals.List(ctx, tx, approvals.Filter{PageSize: 50}, t0)
		return err
	})
	if err != nil {
		t.Fatalf("page 1: %v", err)
	}
	if len(first) != 50 || token == "" {
		t.Fatalf("page 1 = %d approvals token %q, want 50 and a token", len(first), token)
	}
	var second []*hubv1.Approval
	var last string
	err = s.Read(ctx, func(tx *sql.Tx) error {
		var err error
		second, last, err = approvals.List(ctx, tx, approvals.Filter{PageSize: 50, PageToken: token}, t0)
		return err
	})
	if err != nil {
		t.Fatalf("page 2: %v", err)
	}
	if len(second) != 1 || last != "" {
		t.Fatalf("page 2 = %d approvals token %q, want 1 and empty", len(second), last)
	}
	seen := map[string]bool{}
	for _, a := range append(first, second...) {
		id := a.GetId().GetValue()
		if seen[id] {
			t.Fatalf("approval %s on both pages", id)
		}
		seen[id] = true
	}
	if len(seen) != 51 {
		t.Fatalf("paged ids = %d, want 51", len(seen))
	}
}

// TestListPagesSurviveInserts: rows created between pages sort in front of
// the cursor, so the second page neither repeats nor skips.
func TestListPagesSurviveInserts(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	ctx := context.Background()
	// Distinct create times order the rows: the test must not depend on
	// random id order.
	created := []time.Time{t0, t0.Add(time.Second), t0.Add(2 * time.Second)}
	for _, at := range created {
		at := at
		if err := s.Write(ctx, func(tx *sql.Tx) error {
			in := confirmIn()
			in.ID = ""
			in.Now = at
			in.ExpireTime = at.Add(30 * time.Minute)
			_, err := approvals.CreateApproval(ctx, tx, in)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	var token string
	var first []*hubv1.Approval
	if err := s.Read(ctx, func(tx *sql.Tx) error {
		var err error
		first, token, err = approvals.List(ctx, tx, approvals.Filter{PageSize: 2}, t0)
		return err
	}); err != nil {
		t.Fatalf("page 1: %v", err)
	}
	// A row arriving between pages sorts strictly newest, in front of
	// the cursor.
	if err := s.Write(ctx, func(tx *sql.Tx) error {
		in := confirmIn()
		in.ID = ""
		in.Now = t0.Add(3 * time.Second)
		in.ExpireTime = t0.Add(33 * time.Minute)
		_, err := approvals.CreateApproval(ctx, tx, in)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	var second []*hubv1.Approval
	var last string
	if err := s.Read(ctx, func(tx *sql.Tx) error {
		var err error
		second, last, err = approvals.List(ctx, tx, approvals.Filter{PageSize: 2, PageToken: token}, t0)
		return err
	}); err != nil {
		t.Fatalf("page 2: %v", err)
	}
	if len(second) != 1 || last != "" {
		t.Fatalf("page 2 = %d approvals token %q, want 1 and empty", len(second), last)
	}
	for _, a := range second {
		for _, b := range first {
			if a.GetId().GetValue() == b.GetId().GetValue() {
				t.Fatalf("approval %s repeated across the insert", a.GetId().GetValue())
			}
		}
	}
	// The same token with another page size is another query: rejected.
	if err := s.Read(ctx, func(tx *sql.Tx) error {
		_, _, lerr := approvals.List(ctx, tx, approvals.Filter{PageSize: 3, PageToken: token}, t0)
		return lerr
	}); err == nil {
		t.Fatal("token from a size-2 query accepted on a size-3 query")
	}
}

// TestListRejectsBadTokens: a garbled token, one minted for another query
// and an expired one all fail invalid.
func TestListRejectsBadTokens(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	ctx := context.Background()
	if err := s.Write(ctx, func(tx *sql.Tx) error {
		_, err := approvals.CreateApproval(ctx, tx, confirmIn())
		return err
	}); err != nil {
		t.Fatal(err)
	}
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
		var err error
		_, token, err = approvals.List(ctx, tx, approvals.Filter{PageSize: 1}, t0)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if token == "" {
		t.Fatal("two rows in pages of 1 left no token")
	}
	cases := map[string]approvals.Filter{
		"garbled":      {PageSize: 1, PageToken: "not-a-token"},
		"other query":  {PageSize: 2, PageToken: token},
		"other filter": {PageSize: 1, PageToken: token, HasState: true, State: hubv1.ApprovalState_APPROVAL_STATE_PENDING},
	}
	for name, f := range cases {
		if err := s.Read(ctx, func(tx *sql.Tx) error {
			_, _, err := approvals.List(ctx, tx, f, t0)
			return err
		}); err == nil {
			t.Errorf("%s token accepted", name)
		}
	}
	// Expired: eleven minutes past the mint.
	if err := s.Read(ctx, func(tx *sql.Tx) error {
		_, _, err := approvals.List(ctx, tx, approvals.Filter{PageSize: 1, PageToken: token}, t0.Add(11*time.Minute))
		return err
	}); err == nil {
		t.Error("expired token accepted")
	}
}

// TestListCapsPageSize: a client asking over the 02b max gets 500 rows.
func TestListCapsPageSize(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	ctx := context.Background()
	if err := s.Write(ctx, func(tx *sql.Tx) error {
		_, err := approvals.CreateApproval(ctx, tx, confirmIn())
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Read(ctx, func(tx *sql.Tx) error {
		list, next, err := approvals.List(ctx, tx, approvals.Filter{PageSize: 5000}, t0)
		if err != nil {
			return err
		}
		if len(list) != 1 || next != "" {
			t.Fatalf("capped list = %d approvals token %q, want 1 and empty", len(list), next)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
