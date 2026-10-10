package ring

import (
	"context"
	"sync"
)

// seqBlock is how many seqs one write of seq_hwm reserves (02i rule 1).
const seqBlock = 1024

// Seqs hands out agent-wide event seqs (02i "Sequence numbers"). Use one per
// process: it keeps the next seq in memory and reserves blocks in agent.db
// before handing any of them out, so a crash or restart never reissues one.
type Seqs struct {
	db *DB

	mu   sync.Mutex
	next uint64
	hwm  uint64
	last uint64
}

// Seqs starts the allocator above the stored seq_hwm: seqs that were
// reserved but never handed out are skipped, never reissued.
func (d *DB) Seqs(ctx context.Context) (*Seqs, error) {
	st, err := d.State(ctx)
	if err != nil {
		return nil, err
	}
	return &Seqs{db: d, next: st.Marks.SeqHWM + 1, hwm: st.Marks.SeqHWM}, nil
}

// Next hands out the next seq. When it is above the reserved block it first
// stores seq_hwm = seq + 1023 (one fsync per block).
func (s *Seqs) Next(ctx context.Context) (uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.next > s.hwm {
		m, err := s.db.RaiseMarks(ctx, Marks{SeqHWM: s.next + seqBlock - 1})
		if err != nil {
			return 0, err
		}
		s.hwm = m.SeqHWM
	}
	v := s.next
	s.next++
	s.last = v
	return v, nil
}

// Floor raises the next seq above hubLast, the HelloAck.hub_last_event_seq
// (02i rule 2), so an agent whose state folder was wiped resumes above what
// the hub holds. A floor above the reserved block is saved as seq_hwm at
// once, so a restart before the next seq resumes above it too. A floor at or
// below the next seq changes nothing; one above [MaxHubLastSeq] is
// [ErrOutOfRange] and changes nothing.
func (s *Seqs) Floor(ctx context.Context, hubLast uint64) error {
	if err := CheckHubMarks(0, hubLast); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if hubLast > s.hwm {
		m, err := s.db.RaiseMarks(ctx, Marks{SeqHWM: hubLast})
		if err != nil {
			return err
		}
		s.hwm = m.SeqHWM
	}
	s.next = max(s.next, hubLast+1)
	return nil
}

// Last is the highest seq this allocator handed out, 0 before its first
// Next: Heartbeat.event_seq (02i "Gaps"). A reserved seq or a floor that no
// Next has used yet is not counted; seqs of an earlier process are all below
// the first one this process hands out.
func (s *Seqs) Last() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.last
}
