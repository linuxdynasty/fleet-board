// Package ingest applies what an agent reports to the hub store: in plan
// row 5 PR 5c1 the Snapshot (03f, 03n W2, 02i).
//
// [Clean] makes a Snapshot's stored text safe on the stream's ingest
// goroutine, before any writer job: control and format characters and
// invalid UTF-8 dropped, every string cut to its cap on a character
// boundary, and the agent's free text (version strings, the policy error)
// passed through internal/scrub only after that cut. [SubmitSnapshot] queues the Snapshot transaction as ONE writer
// job, whose result [Pending.Wait] reads (again, after a wait that gave up): statedb.ApplySnapshot (the current-epoch check, agent rollback,
// facts, resources, policy, drain, counters, the seq floor), then each
// [Applier] in order (the seam plan rows 7 to 10 fill), then
// outbox.Expire for the machine's due commands ([Pending.Expired]), then
// statedb.MarkOnline. Any error rolls the whole Snapshot back
// ([ErrApplier] wraps an applier's error or recovered panic).
package ingest
