package ring

import (
	"bufio"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/store"
	"github.com/linuxdynasty/fleet-board/internal/telemetry"
)

// nativeBatchFixture uses the public journal methods, then reopens the same
// SQLite store with a two-job batch. The long window gives the second caller
// time to enqueue without putting a synchronization wait inside a SQL job.
func nativeBatchFixture(t *testing.T) (*DB, *telemetry.Telemetry, string, NativeSessionRecord, NativeEffectRecord) {
	t.Helper()
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "agent")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	d, err := Open(ctx, dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	var tel *telemetry.Telemetry
	t.Cleanup(func() {
		_ = d.Close()
		if tel != nil {
			_ = tel.Shutdown(context.Background())
		}
	})
	state, err := d.State(ctx)
	if err != nil {
		t.Fatal(err)
	}
	session := NativeSessionRecord{
		SessionID: "session-a", MachineID: "machine-a", InstallID: state.InstallID,
		Harness: "codex", Interface: "app-server", AccountRef: "account-a",
		ProfileRef: "profile-a", WorkdirRef: "repo-a", Model: "model-a", Effort: "high",
		QualificationRef: "qual-a", ControllerID: "controller-a", Generation: 1,
		ControlState: NativeControlManaged,
	}
	if _, err := d.CreateNativeSession(ctx, session); err != nil {
		t.Fatal(err)
	}
	body := []byte("encoded command")
	if _, dup, err := d.LogCommand(ctx, Command{ID: "command-a", Kind: "native.send_turn", Body: body}); err != nil || dup {
		t.Fatalf("log command: duplicate=%v err=%v", dup, err)
	}
	digest := sha256.Sum256(body)
	effect, dup, err := d.AdmitNativeEffect(ctx, NativeEffectRecord{
		EffectID: "effect-a", CommandID: "command-a", SessionID: session.SessionID,
		Generation: 1, Kind: NativeEffectSendTurn, CommandBodyDigest: hex.EncodeToString(digest[:]),
	})
	if err != nil || dup {
		t.Fatalf("admit effect: duplicate=%v err=%v", dup, err)
	}
	if err := d.s.Close(); err != nil {
		t.Fatal(err)
	}
	tel, err = telemetry.New(telemetry.Config{
		Service: telemetry.Agent, Version: "test", InstanceID: "native-claim-test",
		LogOutput: io.Discard,
	})
	if err != nil {
		t.Fatal(err)
	}
	d.s, err = store.Open(ctx, store.Options{
		Path: Path(dir), Name: "agent", Migrations: Migrations(), MustExist: true,
		BatchMax: 2, BatchWindow: 1500 * time.Millisecond, Telemetry: tel,
	})
	if err != nil {
		t.Fatal(err)
	}
	return d, tel, dir, session, effect
}

func nativeBatchCount(t *testing.T, tel *telemetry.Telemetry) float64 {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/metrics", nil)
	req.RemoteAddr = "127.0.0.1:40000"
	rec := httptest.NewRecorder()
	tel.MetricsHandler(nil).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("metrics scrape status %d", rec.Code)
	}
	for sc := bufio.NewScanner(strings.NewReader(rec.Body.String())); sc.Scan(); {
		line := sc.Text()
		if !strings.HasPrefix(line, `fleet_agent_writer_batch_messages_count{`) || !strings.Contains(line, `db="agent"`) {
			continue
		}
		fields := strings.Fields(line)
		count, err := strconv.ParseFloat(fields[len(fields)-1], 64)
		if err != nil {
			t.Fatal(err)
		}
		return count
	}
	t.Fatal("agent writer batch count missing from metrics")
	return 0
}

func waitNativeFirstJob(t *testing.T, d *DB) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for d.s.OldestJobAge(time.Now()) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("first native job did not reach the store writer")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestNativeClaimReceiptExistsInCommittedSnapshotBeforeRecovery(t *testing.T) {
	d, tel, _, session, effect := nativeBatchFixture(t)
	ctx := context.Background()
	type claimResult struct {
		record NativeEffectRecord
		err    error
	}
	claim := make(chan claimResult, 1)
	go func() {
		r, err := d.ClaimNativeEffect(ctx, effect.EffectID, session.ControllerID, 1)
		claim <- claimResult{r, err}
	}()
	waitNativeFirstJob(t, d)
	recovery := make(chan error, 1)
	go func() {
		_, err := d.RecoverNativeEffects(ctx, "", 10)
		recovery <- err
	}()
	got := <-claim
	if got.err != nil || got.record.State != NativeEffectMayHaveRun {
		t.Fatalf("claim: %+v", got)
	}
	if err := <-recovery; err != nil {
		t.Fatal(err)
	}
	if batches := nativeBatchCount(t, tel); batches != 2 {
		t.Fatalf("claim and recovery committed in %.0f batches, want two distinct commits", batches)
	}
	committed, err := d.ReadNativeEffect(ctx, effect.EffectID)
	if err != nil || committed.State != NativeEffectReconcileRequired {
		t.Fatalf("recovered row: %+v err=%v", committed, err)
	}
}

func TestNativeClaimReceiptExistsInCommittedSnapshotBeforeResolution(t *testing.T) {
	d, tel, _, session, effect := nativeBatchFixture(t)
	ctx := context.Background()
	type claimResult struct {
		record NativeEffectRecord
		err    error
	}
	claim := make(chan claimResult, 1)
	go func() {
		r, err := d.ClaimNativeEffect(ctx, effect.EffectID, session.ControllerID, 1)
		claim <- claimResult{r, err}
	}()
	waitNativeFirstJob(t, d)
	resolution := make(chan error, 1)
	go func() {
		_, err := d.ResolveNativeEffect(ctx, effect.EffectID, session.ControllerID, 1,
			NativeEffectNoEffect, NativeEvidence{
				Kind:   NativeEvidenceProviderAbsence,
				Digest: hex.EncodeToString(make([]byte, 32)),
			})
		resolution <- err
	}()
	got := <-claim
	if got.err != nil || got.record.State != NativeEffectMayHaveRun {
		t.Fatalf("claim: %+v", got)
	}
	if err := <-resolution; err != nil {
		t.Fatal(err)
	}
	if batches := nativeBatchCount(t, tel); batches != 2 {
		t.Fatalf("claim and resolution committed in %.0f batches, want two distinct commits", batches)
	}
	committed, err := d.ReadNativeEffect(ctx, effect.EffectID)
	if err != nil || committed.State != NativeEffectNoEffect {
		t.Fatalf("resolved row: %+v err=%v", committed, err)
	}
}

func TestNativeCanceledQueuedClaimKeepsCompetitorOutUntilTerminalOutcome(t *testing.T) {
	d, tel, _, session, effect := nativeBatchFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	type claimResult struct {
		record NativeEffectRecord
		err    error
	}
	claim := make(chan claimResult, 1)
	go func() {
		r, err := d.ClaimNativeEffect(ctx, effect.EffectID, session.ControllerID, 1)
		claim <- claimResult{r, err}
	}()
	waitNativeFirstJob(t, d)
	cancel()
	select {
	case got := <-claim:
		if got.record != (NativeEffectRecord{}) || !errors.Is(got.err, context.Canceled) {
			t.Fatalf("canceled queued claim returned a receipt: %+v", got)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("canceled queued claim did not return promptly")
	}
	recovery := make(chan error, 1)
	go func() {
		_, err := d.RecoverNativeEffects(context.Background(), "", 10)
		recovery <- err
	}()
	if err := <-recovery; err != nil {
		t.Fatal(err)
	}
	if batches := nativeBatchCount(t, tel); batches != 2 {
		t.Fatalf("canceled claim and recovery committed in %.0f batches, want two terminal outcomes", batches)
	}
	stored, err := d.ReadNativeEffect(context.Background(), effect.EffectID)
	if err != nil || stored.State != NativeEffectPrepared {
		t.Fatalf("canceled claim changed durable row: %+v err=%v", stored, err)
	}
}

func TestNativeCancelBeforeEnqueueAndWhileWaitingForGate(t *testing.T) {
	d, tel, dir, session, effect := nativeBatchFixture(t)
	preCanceled, cancel := context.WithCancel(context.Background())
	cancel()
	if got, err := d.ClaimNativeEffect(preCanceled, effect.EffectID, session.ControllerID, 1); got != (NativeEffectRecord{}) || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel before enqueue: %+v err=%v", got, err)
	}
	claim := make(chan error, 1)
	go func() {
		_, err := d.ClaimNativeEffect(context.Background(), effect.EffectID, session.ControllerID, 1)
		claim <- err
	}()
	waitNativeFirstJob(t, d)
	waitCtx, stopWaiting := context.WithCancel(context.Background())
	waiting := make(chan error, 1)
	started := make(chan struct{})
	go func() {
		close(started)
		_, err := d.RecoverNativeEffects(waitCtx, "", 10)
		waiting <- err
	}()
	<-started
	stopWaiting()
	select {
	case err := <-waiting:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("gate wait returned %v, want cancellation", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("canceled gate wait did not return promptly")
	}
	if err := <-claim; err != nil {
		t.Fatal(err)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	if batches := nativeBatchCount(t, tel); batches != 1 {
		t.Fatalf("pre-enqueue and waiting cancellations created jobs: batches=%.0f", batches)
	}
	reopened, err := Open(context.Background(), dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	stored, err := reopened.ReadNativeEffect(context.Background(), effect.EffectID)
	if err != nil || stored.State != NativeEffectMayHaveRun {
		t.Fatalf("waiting recovery changed row: %+v err=%v", stored, err)
	}
}

func TestOrdinaryCommandWriteCanJoinNativeClaimBatch(t *testing.T) {
	d, tel, _, session, effect := nativeBatchFixture(t)
	claim := make(chan error, 1)
	go func() {
		_, err := d.ClaimNativeEffect(context.Background(), effect.EffectID, session.ControllerID, 1)
		claim <- err
	}()
	waitNativeFirstJob(t, d)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, duplicate, err := d.LogCommand(ctx, Command{ID: "ordinary-command", Kind: "unrelated", Body: []byte("ordinary")})
	if err != nil || duplicate {
		t.Fatalf("ordinary command while native gate held: duplicate=%v err=%v", duplicate, err)
	}
	if err := <-claim; err != nil {
		t.Fatal(err)
	}
	if batches := nativeBatchCount(t, tel); batches != 1 {
		t.Fatalf("ordinary command could not share native claim batch: batches=%.0f", batches)
	}
}

func TestNativeGateReleasesAfterJobRollback(t *testing.T) {
	d, tel, dir, session, effect := nativeBatchFixture(t)
	ctx := context.Background()
	file, err := sql.Open("sqlite", Path(dir))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	if _, err := file.ExecContext(ctx, `CREATE TRIGGER reject_native_claim BEFORE UPDATE OF state ON native_effects
		WHEN NEW.state = 'may_have_run' BEGIN SELECT RAISE(ABORT, 'claim refused'); END`); err != nil {
		t.Fatal(err)
	}
	if got, err := d.ClaimNativeEffect(ctx, effect.EffectID, session.ControllerID, 1); got != (NativeEffectRecord{}) || err == nil {
		t.Fatalf("rolled-back claim returned a receipt: %+v err=%v", got, err)
	}
	if _, err := file.ExecContext(ctx, `DROP TRIGGER reject_native_claim`); err != nil {
		t.Fatal(err)
	}
	if got, err := d.ClaimNativeEffect(ctx, effect.EffectID, session.ControllerID, 1); err != nil || got.State != NativeEffectMayHaveRun {
		t.Fatalf("claim after rollback: %+v err=%v", got, err)
	}
	if batches := nativeBatchCount(t, tel); batches != 2 {
		t.Fatalf("failed and successful claims had %.0f terminal batches, want two", batches)
	}
}

func TestNativeCloseDrainsClaimAndJoinsGate(t *testing.T) {
	d, _, dir, session, effect := nativeBatchFixture(t)
	claim := make(chan error, 1)
	go func() {
		_, err := d.ClaimNativeEffect(context.Background(), effect.EffectID, session.ControllerID, 1)
		claim <- err
	}()
	waitNativeFirstJob(t, d)
	closed := make(chan error, 1)
	go func() { closed <- d.Close() }()
	if err := <-claim; err != nil {
		t.Fatalf("queued claim during Close: %v", err)
	}
	if err := <-closed; err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := d.Close(); err != nil {
		t.Fatalf("repeated Close: %v", err)
	}
	reopened, err := Open(context.Background(), dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	stored, err := reopened.ReadNativeEffect(context.Background(), effect.EffectID)
	if err != nil || stored.State != NativeEffectMayHaveRun {
		t.Fatalf("claim lost across Close and reopen: %+v err=%v", stored, err)
	}
}

func TestNativeCloseJoinsCanceledQueuedClaim(t *testing.T) {
	d, _, dir, session, effect := nativeBatchFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	claim := make(chan error, 1)
	go func() {
		_, err := d.ClaimNativeEffect(ctx, effect.EffectID, session.ControllerID, 1)
		claim <- err
	}()
	waitNativeFirstJob(t, d)
	cancel()
	if err := <-claim; !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled claim: %v", err)
	}
	closed := make(chan error, 1)
	go func() { closed <- d.Close() }()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatalf("Close: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not join the canceled native job")
	}
	reopened, err := Open(context.Background(), dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	stored, err := reopened.ReadNativeEffect(context.Background(), effect.EffectID)
	if err != nil || (stored.State != NativeEffectPrepared && stored.State != NativeEffectMayHaveRun) {
		t.Fatalf("uncertain canceled claim after reopen: %+v err=%v", stored, err)
	}
}

func TestNativeClaimReceiptIsHistoricalAfterLaterMutation(t *testing.T) {
	for _, tc := range []struct {
		name  string
		later func(context.Context, *DB, NativeSessionRecord, NativeEffectRecord) error
		want  NativeEffectState
	}{
		{"recovery", func(ctx context.Context, d *DB, _ NativeSessionRecord, _ NativeEffectRecord) error {
			_, err := d.RecoverNativeEffects(ctx, "", 10)
			return err
		}, NativeEffectReconcileRequired},
		{"resolution", func(ctx context.Context, d *DB, s NativeSessionRecord, e NativeEffectRecord) error {
			_, err := d.ResolveNativeEffect(ctx, e.EffectID, s.ControllerID, 1, NativeEffectNoEffect,
				NativeEvidence{Kind: NativeEvidenceProviderAbsence, Digest: hex.EncodeToString(make([]byte, 32))})
			return err
		}, NativeEffectNoEffect},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, _, _, session, effect := nativeBatchFixture(t)
			ctx := context.Background()
			receipt, err := d.ClaimNativeEffect(ctx, effect.EffectID, session.ControllerID, 1)
			if err != nil || receipt.State != NativeEffectMayHaveRun {
				t.Fatalf("claim receipt: %+v err=%v", receipt, err)
			}
			if err := tc.later(ctx, d, session, effect); err != nil {
				t.Fatal(err)
			}
			current, err := d.ReadNativeEffect(ctx, effect.EffectID)
			if err != nil || current.State != tc.want || receipt.State != NativeEffectMayHaveRun {
				t.Fatalf("historical receipt=%s current=%+v err=%v", receipt.State, current, err)
			}
		})
	}
}
