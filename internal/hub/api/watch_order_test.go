package api_test

import (
	"context"
	"crypto/sha256"
	"fmt"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

// enrollOrderedMachine enrolls one machine straight into the store with
// its Watch publish in the enroll commit hook, the way the gateway enroll
// path publishes (gateway/enroll.go): the publish lands in commit order
// with every other commit's hook publish (#393).
func enrollOrderedMachine(e *fleetEnv, id, name string) error {
	secret := sha256.Sum256([]byte("seed-" + id))
	tok := statedb.EnrollToken{
		SecretHash: secret, MachineName: name, ExpectedHostname: name + ".lan", Labels: []string{"gpu"},
		NotAfter: fleetNow.Add(time.Hour), Created: fleetNow,
		Principal: "local:alice", RequestID: "seed-" + id, RequestHash: sha256.Sum256([]byte("seed")),
	}
	if err := statedb.InsertEnrollToken(context.Background(), e.st, tok); err != nil {
		return err
	}
	key := statedb.EnrollKey{SecretHash: secret, InstallID: "install-1", CSRKeyHash: sha256.Sum256([]byte("csr"))}
	pctx := context.WithoutCancel(context.Background())
	_, err := statedb.Enroll(context.Background(), e.st, statedb.NewMachine{
		Key: key, MachineID: id, CertDER: []byte("cert"), Serial: "or" + id[len(id)-4:],
		ReportedHostname: name + ".lan",
		Facts:            `{"hostname":"` + name + `.lan"}`, Now: fleetNow,
	}, func(enrolled statedb.Enrolled) {
		e.fleet.PublishMachineCreated(pctx, enrolled.Row, enrolled.Replayed)
	})
	return err
}

// Concurrent external (gateway-style enroll) and api (rename) commits
// publish through writer commit hooks, so the bus stays in commit order
// on every interleaving: no schedule inverts it, and a resume from any
// position sees every earlier commit (#393).
func TestWatchConcurrentCommitsStayOrdered(t *testing.T) {
	t.Parallel()
	adm := &testMachineAdmin{revoker: &fakeRevoker{}}
	e := newMachineEnv(t, localUser("alice"), adm)
	seedMachine(t, e, "01jordermachine00000001")
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
	var prev uint64
	for k := uint64(0); k < rounds; k++ {
		id := fmt.Sprintf("01jordermachine%07d", k)
		name := fmt.Sprintf("laptop-%d", k)
		start := make(chan struct{})
		errs := make(chan error, 2)
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			errs <- enrollOrderedMachine(e, id, name)
		}()
		go func() {
			defer wg.Done()
			<-start
			_, err := e.client.UpdateMachine(context.Background(), connect.NewRequest(
				updateMachineRequest("01jordermachine00000001", fmt.Sprintf("req-%d", k),
					"renamed", []string{fmt.Sprintf("desk-%d", k)}, k+1,
					[]string{"spec.display_name", "spec.labels"})))
			errs <- err
		}()
		// Release both at once so the commits genuinely race on the writer.
		close(start)
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Fatalf("round %d: %v", k, err)
			}
		}
		// Both commits published before their calls returned (a hook runs
		// before its Write signals done), so exactly two messages wait.
		for range 2 {
			m := recvWatch(t, stream)
			if m.GetHubSeq() <= prev {
				t.Fatalf("round %d: hub_seq %d after %d, want ascending", k, m.GetHubSeq(), prev)
			}
			prev = m.GetHubSeq()
		}
	}
}
