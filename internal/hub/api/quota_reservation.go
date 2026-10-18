package api

import (
	"context"
	"sync"

	"connectrpc.com/connect"

	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
)

// quotaReservation holds one write allowance across RevokeMachine's file
// staging gap. A terminal state is chosen once. A refund credits only the
// window in which this permit was issued, never the next minute's budget.
type quotaReservation struct {
	mu         sync.Mutex
	quota      *Quotas
	generation uint64
	finished   bool
}

func (r *quotaReservation) consume() {
	if r == nil {
		return // the kernel-checked admin socket has no TCP budget
	}
	r.mu.Lock()
	r.finished = true
	r.mu.Unlock()
}

func (r *quotaReservation) refund() {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.finished {
		return
	}
	r.finished = true
	r.quota.mu.Lock()
	if r.quota.write.generation == r.generation && r.quota.write.used > 0 {
		r.quota.write.used--
	}
	r.quota.mu.Unlock()
}

// reserveWriteQuota is called only from the short, non-mutating revoke
// precheck writer job. The request lock guards against a second charge by
// this request; the quota lock covers only counter state.
func reserveWriteQuota(ctx context.Context) (*quotaReservation, error) {
	qr, _ := ctx.Value(quotaContextKey{}).(*quotaRequest)
	if qr == nil {
		return nil, nil
	}
	qr.mu.Lock()
	defer qr.mu.Unlock()
	if qr.done {
		if qr.class != quotaWrite || (qr.result == nil && qr.reservation == nil) {
			return nil, apiError(connect.CodeInternal, typev1.ReasonCode_REASON_CODE_INTERNAL,
				"a request used two quota modes", nil, nil)
		}
		return qr.reservation, qr.result
	}
	qr.class, qr.done = quotaWrite, true
	qr.reservation, qr.result = qr.quota.reserveWrite(ctx)
	return qr.reservation, qr.result
}

func (q *Quotas) reserveWrite(ctx context.Context) (*quotaReservation, error) {
	q.mu.Lock()
	wait, allowed := q.write.take(q.now(), q.limits.Write)
	generation := q.write.generation
	q.mu.Unlock()
	if !allowed {
		return nil, q.rateLimited(ctx, quotaWrite, "budget", wait)
	}
	return &quotaReservation{quota: q, generation: generation}, nil
}
