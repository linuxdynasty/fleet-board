package gateway_test

import (
	"sync"
	"testing"
	"time"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
)

// Concurrent first enrolls with one token make exactly one machine row.
// With different installs one wins and the rest are ENROLL_TOKEN_USED; with
// the same install and key every call gets the one machine, all but the
// first as a replay.
func TestConcurrentFirstEnrollsMakeOneMachine(t *testing.T) {
	t.Parallel()
	const n = 8
	t.Run("different installs", func(t *testing.T) {
		t.Parallel()
		h := startHub(t)
		tok := h.token(t, h.clock.now().Add(time.Hour))
		reqs := make([]*agentv1.EnrollRequest, n)
		for i := range reqs {
			reqs[i] = enrollRequest(tok, newAgentKey(t), "install-"+string(rune('a'+i)))
		}
		results, errs := enrollAll(t, h, reqs)
		won := 0
		for i, err := range errs {
			if err == nil {
				won++
				if results[i].GetReplayed() {
					t.Error("the winner was answered as a replay")
				}
				continue
			}
			if got := refusalOf(t, err); got.reason.String() != "REASON_CODE_ENROLL_TOKEN_USED" {
				t.Errorf("a loser got %v, want ENROLL_TOKEN_USED", err)
			}
		}
		if won != 1 || h.machineCount(t) != 1 {
			t.Errorf("%d enrolls won and %d machine rows, want 1 and 1", won, h.machineCount(t))
		}
	})
	t.Run("same install and key", func(t *testing.T) {
		t.Parallel()
		h := startHub(t)
		req := enrollRequest(h.token(t, h.clock.now().Add(time.Hour)), newAgentKey(t), "install-1")
		reqs := make([]*agentv1.EnrollRequest, n)
		for i := range reqs {
			reqs[i] = req
		}
		results, errs := enrollAll(t, h, reqs)
		ids := map[string]bool{}
		fresh := 0
		for i, err := range errs {
			if err != nil {
				t.Fatalf("Enroll %d: %v", i, err)
			}
			ids[results[i].GetMachineId().GetValue()] = true
			if !results[i].GetReplayed() {
				fresh++
			}
		}
		if len(ids) != 1 || fresh != 1 || h.machineCount(t) != 1 {
			t.Errorf("%d machine ids, %d fresh answers, %d rows; want 1, 1, 1", len(ids), fresh, h.machineCount(t))
		}
	})
}

func enrollAll(t *testing.T, h *hub, reqs []*agentv1.EnrollRequest) ([]*agentv1.EnrollResponse, []error) {
	t.Helper()
	results := make([]*agentv1.EnrollResponse, len(reqs))
	errs := make([]error, len(reqs))
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i, req := range reqs {
		wg.Go(func() {
			<-start
			results[i], errs[i] = h.enroll(t, req)
		})
	}
	close(start)
	wg.Wait()
	return results, errs
}
