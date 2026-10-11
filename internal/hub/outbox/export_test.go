package outbox

import "context"

// MarkSent records one send the way Run does after it queued the command,
// so a test can put another writer job between the sender's read and its
// mark.
func MarkSent(s *Service, machine, id string, epoch, seq uint64) {
	s.markSent(context.Background(), []mark{{machine: machine, id: id, epoch: epoch, seq: seq}})
}
