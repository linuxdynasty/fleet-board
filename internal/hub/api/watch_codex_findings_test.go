package api_test

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"connectrpc.com/connect"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
)

// Two concurrent renames of one machine share a writer batch. Each commit
// stamps its own row; the publishes must carry each job's own name in
// hub_seq order. Re-reading the row after the shared commit publishes the
// batch-final name twice and loses the first transition (#403 P1).
func TestWatchBatchOverwritePublishesBothTransitions(t *testing.T) {
	t.Parallel()
	adm := &testMachineAdmin{revoker: &fakeRevoker{}}
	e := newMachineEnv(t, localUser("alice"), adm)
	id := "01jbatchmachine000000001"
	seedMachine(t, e, id)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	stream, err := e.client.Watch(ctx, connect.NewRequest(&hubv1.WatchRequest{
		Subscriptions: []*hubv1.TopicSubscription{watchSub(hubv1.WatchTopic_WATCH_TOPIC_MACHINES, "", true)},
	}))
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	drainInitial(t, stream, hubv1.WatchTopic_WATCH_TOPIC_MACHINES)
	const rounds = 8
	for k := 0; k < rounds; k++ {
		nameA, nameB := "batch-a", "batch-b"
		reqA, reqB := fmt.Sprintf("batch-a-req-%d", k), fmt.Sprintf("batch-b-req-%d", k)
		start := make(chan struct{})
		errs := make(chan error, 2)
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			_, err := e.client.UpdateMachine(context.Background(), connect.NewRequest(
				updateMachineRequest(id, reqA, nameA, []string{"desk"}, 0,
					[]string{"spec.display_name"})))
			errs <- err
		}()
		go func() {
			defer wg.Done()
			<-start
			_, err := e.client.UpdateMachine(context.Background(), connect.NewRequest(
				updateMachineRequest(id, reqB, nameB, []string{"desk"}, 0,
					[]string{"spec.display_name"})))
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
		m1 := recvWatch(t, stream)
		m2 := recvWatch(t, stream)
		if m2.GetHubSeq() <= m1.GetHubSeq() {
			t.Fatalf("round %d: hub_seq %d then %d, want ascending", k, m1.GetHubSeq(), m2.GetHubSeq())
		}
		got := map[string]bool{
			m1.GetMachine().GetSpec().GetDisplayName(): true,
			m2.GetMachine().GetSpec().GetDisplayName(): true,
		}
		if !got[nameA] || !got[nameB] {
			t.Fatalf("round %d: publishes %q and %q, want batch-a and batch-b (lost transition)",
				k, m1.GetMachine().GetSpec().GetDisplayName(), m2.GetMachine().GetSpec().GetDisplayName())
		}
	}
}

// Hook publishes race the publish tick's summary state. Before the fix the
// API hooks reached offerWatch without watchMu while PublishTick held it,
// losing the dirty flag under -race (#403 P1).
func TestWatchHookVsTickSummaryRace(t *testing.T) {
	t.Parallel()
	adm := &testMachineAdmin{revoker: &fakeRevoker{}}
	e := newMachineEnv(t, localUser("alice"), adm)
	id := "01jracemachine00000000001"
	seedMachine(t, e, id)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			for k := 0; k < 10; k++ {
				_, _ = e.client.UpdateMachine(context.Background(), connect.NewRequest(
					updateMachineRequest(id, fmt.Sprintf("race-req-%d-%d", n, k), "renamed", []string{"desk"}, 0,
						[]string{"spec.display_name"})))
			}
		}(i)
		wg.Add(1)
		go func() {
			defer wg.Done()
			for k := 0; k < 10; k++ {
				_ = e.fleet.PublishTick(context.Background())
			}
		}()
	}
	wg.Wait()
}

// Two concurrent account creates stamp resource then audit per job. The bus
// must carry resource, audit, resource, audit in ascending hub_seq. A tick
// tail after hook resources inverts to resource, resource, audit, audit
// (#403 P1).
func TestWatchAuditPublishesInSeqOrder(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"), trusted(false))
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	stream, err := e.client.Watch(ctx, connect.NewRequest(&hubv1.WatchRequest{
		Subscriptions: []*hubv1.TopicSubscription{
			watchSub(hubv1.WatchTopic_WATCH_TOPIC_ACCOUNTS, "", true),
			watchSub(hubv1.WatchTopic_WATCH_TOPIC_AUDIT, "", true),
		},
	}))
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	drainInitial(t, stream, hubv1.WatchTopic_WATCH_TOPIC_ACCOUNTS)
	drainInitial(t, stream, hubv1.WatchTopic_WATCH_TOPIC_AUDIT)
	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		spec := accountSpec()
		spec.DisplayName = "audit-a"
		_, err := e.client.CreateAccount(context.Background(), connect.NewRequest(&hubv1.CreateAccountRequest{
			RequestId: "audit-a-req", Harness: &typev1.Harness{Name: "opencode"},
			Provider: "anthropic", Spec: spec,
		}))
		errs <- err
	}()
	go func() {
		defer wg.Done()
		<-start
		spec := accountSpec()
		spec.DisplayName = "audit-b"
		_, err := e.client.CreateAccount(context.Background(), connect.NewRequest(&hubv1.CreateAccountRequest{
			RequestId: "audit-b-req", Harness: &typev1.Harness{Name: "opencode"},
			Provider: "anthropic", Spec: spec,
		}))
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
	if err := e.fleet.PublishTick(context.Background()); err != nil {
		t.Fatalf("PublishTick: %v", err)
	}
	var seqs []uint64
	var topics []hubv1.WatchTopic
	for i := 0; i < 4; i++ {
		m := recvWatch(t, stream)
		seqs = append(seqs, m.GetHubSeq())
		topics = append(topics, m.GetTopic())
	}
	for i := 1; i < len(seqs); i++ {
		if seqs[i] <= seqs[i-1] {
			t.Fatalf("hub_seq %v not ascending (topics %v): resume would skip", seqs, topics)
		}
	}
}
