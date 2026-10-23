package api

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/approvals"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
	"google.golang.org/protobuf/types/known/fieldmaskpb"
)

// seedInternalMachine enrolls one machine straight into the test store.
// No Watch publish: the tests below drive publishes through the Fleet
// handlers they exercise.
func seedInternalMachine(t *testing.T, f *Fleet, id string) {
	t.Helper()
	now := f.now()
	secret := sha256.Sum256([]byte("seed-" + id))
	tok := statedb.EnrollToken{
		SecretHash: secret, MachineName: "laptop", ExpectedHostname: "laptop.lan", Labels: []string{"gpu"},
		NotAfter: now.Add(time.Hour), Created: now,
		Principal: "local:alice", RequestID: "seed-" + id, RequestHash: sha256.Sum256([]byte("seed")),
	}
	if err := statedb.InsertEnrollToken(context.Background(), f.store, tok); err != nil {
		t.Fatal(err)
	}
	key := statedb.EnrollKey{SecretHash: secret, InstallID: "install-1", CSRKeyHash: sha256.Sum256([]byte("csr"))}
	if _, err := statedb.Enroll(context.Background(), f.store, statedb.NewMachine{
		Key: key, MachineID: id, CertDER: []byte("cert"), Serial: "ab" + id[len(id)-4:],
		CertNotAfter: now.Add(90 * 24 * time.Hour), ReportedHostname: "laptop.lan",
		Facts: `{"hostname":"laptop.lan"}`, Now: now,
	}, nil); err != nil {
		t.Fatal(err)
	}
}

func internalPrincipalCtx(ctx context.Context) context.Context {
	return WithPrincipal(ctx, &typev1.Principal{
		Name: "alice", Role: typev1.Role_ROLE_OWNER, Kind: typev1.PrincipalKind_PRINCIPAL_KIND_LOCAL_USER,
		Id: &typev1.PrincipalId{Value: "local:alice"},
	})
}

func internalUpdateReq(id, requestID, name string) *connect.Request[hubv1.UpdateMachineRequest] {
	return connect.NewRequest(&hubv1.UpdateMachineRequest{
		RequestId: requestID,
		Machine: &hubv1.Machine{
			Id:      &typev1.MachineId{Value: id},
			Spec:    &hubv1.MachineSpec{DisplayName: name, Labels: []string{"desk"}},
			Version: 0,
		},
		UpdateMask:      &fieldmaskpb.FieldMask{Paths: []string{"spec.display_name"}},
		ExpectedVersion: 0,
	})
}

// Two concurrent renames of one machine share a writer batch. Each commit
// stamps its own row; the bus must carry each job's own name in hub_seq
// order. Re-reading the row after the shared commit publishes the
// batch-final name twice and loses the first transition (#403 P1).
func TestCodexBatchOverwriteKeepsBothTransitions(t *testing.T) {
	t.Parallel()
	f := testCommitFleet(t)
	id := "01jbatchmachine000000001"
	seedInternalMachine(t, f, id)
	ctx := internalPrincipalCtx(context.Background())
	const rounds = 8
	want := map[string]bool{}
	for k := 0; k < rounds; k++ {
		nameA, nameB := fmt.Sprintf("batch-a-%d", k), fmt.Sprintf("batch-b-%d", k)
		reqA, reqB := fmt.Sprintf("batch-a-req-%d", k), fmt.Sprintf("batch-b-req-%d", k)
		want[nameA], want[nameB] = true, true
		start := make(chan struct{})
		errs := make(chan error, 2)
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			_, err := f.UpdateMachine(ctx, internalUpdateReq(id, reqA, nameA))
			errs <- err
		}()
		go func() {
			defer wg.Done()
			<-start
			_, err := f.UpdateMachine(ctx, internalUpdateReq(id, reqB, nameB))
			errs <- err
		}()
		close(start)
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Fatalf("round %d: %v", k, err)
			}
		}
	}
	entries, _, _ := f.watch.snapshot(0, ^uint64(0))
	var machines []struct {
		seq  uint64
		name string
	}
	var prev uint64
	for i, e := range entries {
		if e.seq <= prev {
			t.Fatalf("entry %d: hub_seq %d after %d, want ascending", i, e.seq, prev)
		}
		prev = e.seq
		if e.change.topic != hubv1.WatchTopic_WATCH_TOPIC_MACHINES {
			continue
		}
		machines = append(machines, struct {
			seq  uint64
			name string
		}{e.seq, e.change.msg.GetMachine().GetSpec().GetDisplayName()})
	}
	if len(machines) != 2*rounds {
		t.Fatalf("bus holds %d machine entries, want %d", len(machines), 2*rounds)
	}
	gotNames := map[string]int{}
	for _, m := range machines {
		gotNames[m.name]++
	}
	for n := range want {
		if gotNames[n] != 1 {
			t.Fatalf("name %q published %d times, want 1 (lost transition); got %v", n, gotNames[n], gotNames)
		}
	}
}

// Hook publishes race the publish tick's summary state. Before the fix an
// API hook reached offerWatch without watchMu while PublishTick held it,
// losing the dirty flag under -race (#403 P1). The detector fails the run
// on a race; the test drives hook-side offerWatch calls against ticks and
// through the UpdateMachine handler path together.
func TestCodexHookVsTickSummaryRace(t *testing.T) {
	t.Parallel()
	f := testCommitFleet(t)
	id := "01jracemachine00000000001"
	seedInternalMachine(t, f, id)
	ctx := internalPrincipalCtx(context.Background())
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			for k := 0; k < 25; k++ {
				_, _ = f.UpdateMachine(ctx, internalUpdateReq(id, fmt.Sprintf("race-req-%d-%d", n, k), "renamed"))
				f.offerWatch(int64(1000+n*100+k), watchChange{
					topic: hubv1.WatchTopic_WATCH_TOPIC_MACHINES, change: hubv1.ChangeType_CHANGE_TYPE_UPDATED,
					msg: &hubv1.WatchResponse{
						Topic: hubv1.WatchTopic_WATCH_TOPIC_MACHINES, ChangeType: hubv1.ChangeType_CHANGE_TYPE_UPDATED,
					},
					match: func(*filterNode) bool { return true },
				})
			}
		}(i)
		wg.Add(1)
		go func() {
			defer wg.Done()
			for k := 0; k < 25; k++ {
				_ = f.PublishTick(context.Background())
			}
		}()
	}
	wg.Wait()
}

// Two concurrent account creates stamp resource then audit per job. The bus
// must carry resource, audit, resource, audit in ascending hub_seq. A tick
// tail after hook resources inverts to resource, resource, audit, audit
// (#403 P1).
func TestCodexAuditPublishesInSeqOrder(t *testing.T) {
	t.Parallel()
	f := testCommitFleet(t)
	ctx := internalPrincipalCtx(context.Background())
	mkSpec := func(name string) *hubv1.AccountSpec {
		return &hubv1.AccountSpec{
			DisplayName: name, Mode: hubv1.AccountMode_ACCOUNT_MODE_FLEET,
			BudgetKind: typev1.BudgetKind_BUDGET_KIND_DOLLAR_CAP,
		}
	}
	mkReq := func(reqID, name string) *connect.Request[hubv1.CreateAccountRequest] {
		return connect.NewRequest(&hubv1.CreateAccountRequest{
			RequestId: reqID, Harness: &typev1.Harness{Name: "opencode"},
			Provider: "anthropic", Spec: mkSpec(name),
		})
	}
	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		_, err := f.CreateAccount(ctx, mkReq("audit-a-req", "audit-a"))
		errs <- err
	}()
	go func() {
		defer wg.Done()
		<-start
		_, err := f.CreateAccount(ctx, mkReq("audit-b-req", "audit-b"))
		errs <- err
	}()
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("CreateAccount: %v", err)
		}
	}
	if err := f.PublishTick(context.Background()); err != nil {
		t.Fatalf("PublishTick: %v", err)
	}
	entries, _, _ := f.watch.snapshot(0, ^uint64(0))
	var seqs []uint64
	var topics []hubv1.WatchTopic
	for _, e := range entries {
		if e.change.topic != hubv1.WatchTopic_WATCH_TOPIC_ACCOUNTS &&
			e.change.topic != hubv1.WatchTopic_WATCH_TOPIC_AUDIT {
			continue
		}
		seqs = append(seqs, e.seq)
		topics = append(topics, e.change.topic)
	}
	if len(seqs) != 4 {
		t.Fatalf("bus holds %d account/audit entries, want 4 (seqs %v topics %v)", len(seqs), seqs, topics)
	}
	for i := 1; i < len(seqs); i++ {
		if seqs[i] <= seqs[i-1] {
			t.Fatalf("hub_seq %v not ascending (topics %v): resume would skip", seqs, topics)
		}
	}
}

// The hammer keeper: concurrent writer-ordered account creates stamp
// resource then audit per job while the publish tick spins. The bus must
// stay in hub_seq order no matter where the tick lands (#403 P1). After
// the tail's removal the tick publishes no audits at all, so this holds
// by construction; it guards the invariant forever.
func TestCodexHookAndTickStayOrdered(t *testing.T) {
	t.Parallel()
	f := testCommitFleet(t)
	ctx := internalPrincipalCtx(context.Background())
	stop := make(chan struct{})
	var tickWG sync.WaitGroup
	tickWG.Add(1)
	go func() {
		defer tickWG.Done()
		for {
			select {
			case <-stop:
				return
			default:
				_ = f.PublishTick(context.Background())
			}
		}
	}()
	const writers, rounds = 4, 15
	var wg sync.WaitGroup
	errs := make(chan error, writers*rounds)
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for k := 0; k < rounds; k++ {
				name := fmt.Sprintf("hammer-%d-%d", w, k)
				_, err := f.CreateAccount(ctx, connect.NewRequest(&hubv1.CreateAccountRequest{
					RequestId: fmt.Sprintf("hammer-req-%d-%d", w, k),
					Harness:   &typev1.Harness{Name: "opencode"},
					Provider:  "anthropic",
					Spec: &hubv1.AccountSpec{
						DisplayName: name, Mode: hubv1.AccountMode_ACCOUNT_MODE_FLEET,
						BudgetKind: typev1.BudgetKind_BUDGET_KIND_DOLLAR_CAP,
					},
				}))
				errs <- err
			}
		}(w)
	}
	wg.Wait()
	close(stop)
	tickWG.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("CreateAccount: %v", err)
		}
	}
	entries, _, _ := f.watch.snapshot(0, ^uint64(0))
	var prev uint64
	var count int
	for i, e := range entries {
		if e.seq <= prev {
			t.Fatalf("entry %d: hub_seq %d after %d: resume would skip", i, e.seq, prev)
		}
		prev = e.seq
		count++
	}
	if count == 0 {
		t.Fatal("bus is empty")
	}
}

// seedApproval creates one COMMAND_CONFIRM approval in-tx: the gated kill
// command stays stored, nothing is sent.
func seedApproval(t *testing.T, f *Fleet, expire time.Time) string {
	t.Helper()
	now := f.now()
	in := approvals.Create{
		Kind:     hubv1.ApprovalKind_APPROVAL_KIND_COMMAND_CONFIRM,
		Severity: hubv1.Severity_SEVERITY_WARNING,
		Preview:  "kill window dev:3", MachineID: "m1",
		Allowed: []approvals.Choice{{Key: "approve"}, {Key: "deny"}},
		Command: &agentv1.Command{
			Kind: &agentv1.Command_KillWindow{KillWindow: &agentv1.KillWindow{
				Window: &typev1.WindowKey{Server: "dev", ServerStartId: "s9", WindowId: "3"},
			}},
		},
		DefaultChoice: "deny", Fallback: hubv1.ApprovalFallback_APPROVAL_FALLBACK_DENY,
		ExpireTime: expire, RequiredRole: typev1.Role_ROLE_OWNER, Now: now,
	}
	var id string
	err := f.store.Write(context.Background(), func(tx *sql.Tx) error {
		a, _, err := approvals.CreateApproval(context.Background(), tx, in)
		if err != nil {
			return err
		}
		id = a.GetId().GetValue()
		return nil
	})
	if err != nil {
		t.Fatalf("CreateApproval: %v", err)
	}
	return id
}

// auditSeqs returns the hub_seqs of the AUDIT entries on the bus.
func auditSeqs(f *Fleet) []uint64 {
	entries, _, _ := f.watch.snapshot(0, ^uint64(0))
	var out []uint64
	for _, e := range entries {
		if e.change.topic == hubv1.WatchTopic_WATCH_TOPIC_AUDIT {
			out = append(out, e.seq)
		}
	}
	return out
}

// Approval expiry stamps its audits in its own job: the hook must publish
// them, with no tick running (#403 P1).
func TestCodexExpirePassPublishesAudits(t *testing.T) {
	t.Parallel()
	f := testCommitFleet(t)
	ctx := context.Background()
	now := f.now()
	seedApproval(t, f, now.Add(time.Hour))
	expired, err := approvals.ExpirePass(ctx, f.store, now.Add(2*time.Hour))
	if err != nil {
		t.Fatalf("ExpirePass: %v", err)
	}
	if len(expired) != 1 {
		t.Fatalf("expired = %v, want one approval", expired)
	}
	if got := auditSeqs(f); len(got) == 0 {
		t.Fatal("bus holds no AUDIT entries: expiry audits never published")
	}
}

// An approval decision stamps its audits in its own job: the hook must
// publish them, with no tick running (#403 P1).
func TestCodexDecidePublishesAudits(t *testing.T) {
	t.Parallel()
	f := testCommitFleet(t)
	ctx := internalPrincipalCtx(context.Background())
	id := seedApproval(t, f, f.now().Add(time.Hour))
	_, err := f.DecideApproval(ctx, connect.NewRequest(&hubv1.DecideApprovalRequest{
		RequestId: "decide-req-1", Id: &hubv1.ApprovalId{Value: id},
		ExpectedVersion: 1, Decision: "deny",
	}))
	if err != nil {
		t.Fatalf("DecideApproval: %v", err)
	}
	if got := auditSeqs(f); len(got) == 0 {
		t.Fatal("bus holds no AUDIT entries: decision audits never published")
	}
}

// A writer hook must never block on the read pool. Ten reads hold every
// pool connection; a machine update whose hook re-reads then deadlocks the
// writer (#403 P1). After the capture fix the update completes. The write
// goes through statedb directly (no handler pre-read) so only the hook's
// pool use can block it.
func TestCodexWriterHookAvoidsReadPool(t *testing.T) {
	t.Parallel()
	f := testCommitFleet(t)
	id := "01jdeadlockmachine0000001"
	seedInternalMachine(t, f, id)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	entered := make(chan struct{}, 10)
	var holders sync.WaitGroup
	for i := 0; i < 10; i++ {
		holders.Add(1)
		go func() {
			defer holders.Done()
			_ = f.store.Read(ctx, func(tx *sql.Tx) error {
				entered <- struct{}{}
				<-ctx.Done()
				return ctx.Err()
			})
		}()
	}
	for i := 0; i < 10; i++ {
		select {
		case <-entered:
		case <-time.After(10 * time.Second):
			t.Fatalf("read pool holder %d never entered", i)
		}
	}
	done := make(chan error, 1)
	go func() {
		sum := sha256.Sum256([]byte("deadlock-req"))
		wctx := statedb.WithValidatedWrite(context.Background(), func() error { return nil })
		_, err := statedb.UpdateMachine(wctx, f.store, statedb.MachineUpdate{
			MachineWrite: statedb.MachineWrite{
				Principal: "local:alice", RequestID: "deadlock-req",
				RequestHash: sum, Now: f.now(),
			},
			MachineID: id, ExpectedVersion: 0,
			Name: "renamed", Labels: []string{"desk"}, HasName: true,
		}, func(s statedb.MachineStored) {
			f.publishMachineWatchRow(context.Background(), s.Row,
				hubv1.ChangeType_CHANGE_TYPE_UPDATED, nil)
		})
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("UpdateMachine: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("UpdateMachine blocked on the read pool: hook re-reads on the writer")
	}
}
