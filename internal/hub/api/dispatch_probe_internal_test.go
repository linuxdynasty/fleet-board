package api

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"connectrpc.com/connect"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

// TestProbeRunNowGateOnePerMinute pins the rule behind the ProbeAccount
// RATE_LIMITED branch: peek closes the account for a minute after a mark
// and reopens it after, per account. The RPC reaches the branch exactly
// when peek closes; no success path can mark it until account keys land,
// so the RPC-level refusal ordering stays in dispatch_probe_test.go.
func TestProbeRunNowGateOnePerMinute(t *testing.T) {
	t.Parallel()
	f := &Fleet{probeRunNow: map[string]time.Time{}}
	now := time.Now()
	if !f.peekProbeRunNow("a1", now) {
		t.Fatal("first peek on an unmarked account is open")
	}
	f.markProbeRunNow("a1", now)
	if f.peekProbeRunNow("a1", now) {
		t.Fatal("peek right after a mark is closed")
	}
	if f.peekProbeRunNow("a1", now.Add(59*time.Second)) {
		t.Fatal("peek 59 s after a mark is still closed")
	}
	if !f.peekProbeRunNow("a1", now.Add(time.Minute)) {
		t.Fatal("peek a minute after a mark is open again")
	}
	if !f.peekProbeRunNow("a2", now) {
		t.Fatal("a mark on one account leaves the others open")
	}
}

// TestCheckProbeReplayReturnsStoredOperation pins the replay-hit path the
// ProbeAccount comment promises: a repeated request_id with the same body
// returns the stored operation instead of queueing a second probe, and a
// different body under the same key is a conflict, never a silent insert.
func TestCheckProbeReplayReturnsStoredOperation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "state")
	if err := statedb.EnsureDir(dir); err != nil {
		t.Fatal(err)
	}
	// Create writes a complete state.db; the values are dummies the
	// replay path never reads.
	if err := statedb.Create(ctx, dir, statedb.Identity{
		FleetID:       "6f1c2a4e-0d3b-4c5e-9f7a-1b2c3d4e5f60",
		CACert:        []byte("ca-cert-der"),
		CAKey:         []byte("ca-key-der"),
		ServerCert:    []byte("server-cert-der"),
		ServerKey:     []byte("server-key-der"),
		ServerNames:   []string{"hub"},
		AccountSalt:   bytes.Repeat([]byte{7}, 32),
		SaltVersion:   1,
		SettingsEpoch: "0b9d3c1e-6a2f-4e8d-8c7b-5a4f3e2d1c0b",
	}, statedb.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	st, err := statedb.Open(ctx, dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	now := time.Now()
	m := &hubv1.ProbeAccountRequest{
		RequestId: "req-1",
		Id:        &typev1.AccountId{Value: "a1"},
	}
	hash := dispatchHash(m)
	if len(hash) == 0 {
		t.Fatal("dispatchHash of the probe request is empty")
	}
	err = st.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO request_log
			(principal_id, request_id, command_id, approval_id, request_hash, create_time)
			VALUES(?, ?, ?, ?, ?, ?)`,
			"local:alice", "req-1", "cmd-1", "", hash, now.UnixMilli()); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO commands
			(command_id, machine_id, kind, command, request_id, principal_id,
			create_time, not_after_time, update_time, hub_seq)
			VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			"cmd-1", "m1", "ensure_probe", []byte{0}, "req-1", "local:alice",
			now.UnixMilli(), now.Add(time.Hour).UnixMilli(), now.UnixMilli(), 1)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	f := &Fleet{}
	err = st.Read(ctx, func(tx *sql.Tx) error {
		cmdID, approvalID, replay, err := f.checkProbeReplay(ctx, tx, "local:alice", m)
		if err != nil {
			return err
		}
		if !replay || cmdID != "cmd-1" || approvalID != "" {
			t.Fatalf("replay=%v cmd %q approval %q, want true cmd-1 empty", replay, cmdID, approvalID)
		}
		op, err := loadOperation(ctx, tx, cmdID)
		if err != nil {
			return err
		}
		if op.GetId().GetValue() != "cmd-1" {
			t.Fatalf("stored operation %q, want cmd-1", op.GetId().GetValue())
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	// A different body under the same key conflicts instead of replaying.
	clash := &hubv1.ProbeAccountRequest{
		RequestId: "req-1",
		Id:        &typev1.AccountId{Value: "other"},
	}
	err = st.Read(ctx, func(tx *sql.Tx) error {
		_, _, replay, err := f.checkProbeReplay(ctx, tx, "local:alice", clash)
		if err == nil || replay {
			t.Fatalf("replay=%v err=%v, want a conflict and no replay", replay, err)
		}
		var ce *connect.Error
		if !errors.As(err, &ce) || ce.Code() != connect.CodeAlreadyExists {
			t.Fatalf("conflict %v, want AlreadyExists", err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
