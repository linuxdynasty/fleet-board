// Package outbox is the hub's command path (docs/specs/03f-hub-internals.md
// "The command path", docs/specs/02i-delivery-and-sequencing.md "Command
// lifecycle"): one commands row per command_id holds the outbox state and
// the Operation, and command_results holds each command's first result.
//
// [Enqueue] runs inside the writer job of the decision that caused the
// command and is a no-op for a known command_id; the caller hands the
// committed [Op] to [Service.Notify]. [Service.Run] is the one sender: once
// a machine's Snapshot of its current epoch committed ([Service.Ready],
// called through the gateway's ready seam), it reads that machine's pending
// rows through the read pool, queues each on the stream of exactly that
// epoch ([Streams]) with the epoch's next seq, and records the sends in one
// writer job afterwards. A send is not a state: a row stays pending until
// CommandAck, so a stream that ends leaves it pending and the next epoch
// re-sends it with the same command_id.
//
// [Service.Ack], [Service.Progress] and [Service.Result] apply the agent's
// answers on the caller's goroutine (the stream's ingest goroutine): free
// text is cut to its 1 KiB cap and scrubbed there, never in the writer's
// transaction, then one writer job applies the message. A message for a
// command with no row, of another machine, or from a stream that is no
// longer live is [Refused] and writes nothing, and so is a refusal the
// agent sends without logging the command (EPOCH_MISMATCH, SEQ_REPLAYED):
// the command goes out again under the live epoch.
//
// Deadlines: [Enqueue] fills an unset not_after_time with the kind's
// [DefaultDeadline]. [Expire] closes due open rows inside a caller's job
// (the Snapshot transaction, ingest.SubmitSnapshot) and
// [Service.ExpireDue] is the housekeeping job that does it for every
// machine; [CloseForRevoke] closes a revoked machine's open rows inside
// the revoke's job; [Service.Closed] logs and counts them after the
// commit. [Service.Prune] deletes rows closed more than [Retention] ago.
//
// Reads: [Lookup] serves one row to the API's GetOperation on the read
// pool — never the writer — with the stored command (whose RequestedBy
// is the full requesting principal) and the first result, when one
// arrived.
package outbox
