package ring

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// The ring's caps (defaults.md "Agent event ring cap"): one week or 64 MiB,
// whichever comes first.
const (
	MaxRingBytes = 64 << 20
	MaxRingAge   = 7 * 24 * time.Hour
)

// DefaultCaps are the caps of defaults.md.
var DefaultCaps = Caps{MaxBytes: MaxRingBytes, MaxAge: MaxRingAge}

// Caps bound the event ring. The oldest entry is evicted first, whatever its
// class, and each eviction is counted in evicted_total (loss).
type Caps struct {
	MaxBytes int64
	MaxAge   time.Duration
}

// EventRow is one ring entry: a NEVER_DROP event (Key "") or a COALESCE
// event (its coalesce_key), encoded whole.
type EventRow struct {
	Seq     uint64
	Key     string
	Created time.Time
	Body    []byte
}

// Size is what the ring holds: AgentHealth.ring_events and ring_bytes.
type Size struct {
	Events, Bytes uint64
}

// Appended is what one [DB.SubmitEvent] did besides the insert.
type Appended struct {
	// Coalesced is 1 when the entry replaced one with its key.
	Coalesced int
	// Evicted is how many entries the caps removed; OldestEvicted the lowest
	// seq among them.
	Evicted       int
	OldestEvicted uint64
	Size          Size
}

var errBadEvent = errors.New("ring: an event needs a seq, a body and a seq that fits agent.db")

// SubmitEvent queues e for the store writer and returns a wait for its
// commit. In one job it deletes the entry with e's key (coalesced_total + 1),
// inserts e, and evicts the oldest entries while the ring is above caps
// (evicted_total + n), measuring age against e.Created. The writer runs jobs
// in queue order, so entries submitted in seq order commit in seq order (the
// file refuses one at or below the newest). The wait answers once the job's
// batch committed or failed, whatever happens to ctx.
func (d *DB) SubmitEvent(ctx context.Context, e EventRow, caps Caps) (func() (Appended, error), error) {
	seq, err := toInt("seq", e.Seq)
	if err != nil || e.Seq == 0 || len(e.Body) == 0 {
		return nil, errBadEvent
	}
	var out Appended
	done, err := d.s.Submit(ctx, func(tx *sql.Tx) error {
		out = Appended{}
		if e.Key != "" {
			res, err := tx.ExecContext(ctx, `DELETE FROM event_ring WHERE coalesce_key = ?`, e.Key)
			if err != nil {
				return fmt.Errorf("ring: coalesce: %w", err)
			}
			if n, err := res.RowsAffected(); err != nil || n > 0 {
				out.Coalesced = int(n)
				if err == nil {
					_, err = tx.ExecContext(ctx, `UPDATE agent_state SET coalesced_total = coalesced_total + ? WHERE id = 1`, n)
				}
				if err != nil {
					return fmt.Errorf("ring: coalesce: %w", err)
				}
			}
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO event_ring(seq, coalesce_key, created_time, bytes, event) VALUES (?, ?, ?, ?, ?)`,
			seq, e.Key, e.Created.UnixMilli(), len(e.Body), e.Body); err != nil {
			return fmt.Errorf("ring: insert event %d: %w", e.Seq, err)
		}
		if err := evict(ctx, tx, e.Created.Add(-caps.MaxAge).UnixMilli(), caps.MaxBytes, &out); err != nil {
			return err
		}
		var err error
		out.Size, err = readSize(ctx, tx)
		return err
	})
	if err != nil {
		return nil, err
	}
	return func() (Appended, error) {
		if err := <-done; err != nil {
			return Appended{}, err
		}
		return out, nil
	}, nil
}

// evict deletes, oldest first, the entries created before old and then as
// many more as keep the ring at or under maxBytes, and counts them.
func evict(ctx context.Context, tx *sql.Tx, old, maxBytes int64, out *Appended) error {
	size, err := readSize(ctx, tx)
	if err != nil {
		return err
	}
	excess := int64(size.Bytes) - maxBytes //nolint:gosec // G115: bytes is a sum of lengths, far below 2^63
	var upTo, first int64
	var n int
	rows, err := tx.QueryContext(ctx, `SELECT seq, bytes, created_time FROM event_ring ORDER BY seq`)
	if err != nil {
		return fmt.Errorf("ring: evict: %w", err)
	}
	for rows.Next() {
		var seq, b, created int64
		if err := rows.Scan(&seq, &b, &created); err != nil {
			_ = rows.Close()
			return fmt.Errorf("ring: evict: %w", err)
		}
		if excess <= 0 && created >= old {
			break
		}
		if n == 0 {
			first = seq
		}
		upTo, excess, n = seq, excess-b, n+1
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return fmt.Errorf("ring: evict: %w", err)
	}
	if n == 0 {
		return nil
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM event_ring WHERE seq <= ?`, upTo); err != nil {
		return fmt.Errorf("ring: evict: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE agent_state SET evicted_total = evicted_total + ? WHERE id = 1`, n); err != nil {
		return fmt.Errorf("ring: evict: %w", err)
	}
	out.Evicted, out.OldestEvicted = n, toUint(first)
	return nil
}

func readSize(ctx context.Context, tx *sql.Tx) (Size, error) {
	var ev, b int64
	if err := tx.QueryRowContext(ctx, `SELECT events, bytes FROM event_ring_size WHERE id = 1`).Scan(&ev, &b); err != nil {
		return Size{}, fmt.Errorf("ring: read ring size: %w", err)
	}
	return Size{Events: toUint(ev), Bytes: toUint(b)}, nil
}

// EventsBetween returns at most limit entries with after < seq < before, in
// seq order.
func (d *DB) EventsBetween(ctx context.Context, after, before uint64, limit int) ([]EventRow, error) {
	lo, err := toInt("seq", min(after, MaxHubLastSeq))
	if err != nil {
		return nil, err
	}
	hi, err := toInt("seq", min(before, uint64(1<<63-1)))
	if err != nil {
		return nil, err
	}
	var out []EventRow
	err = d.s.Read(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT seq, coalesce_key, created_time, event FROM event_ring
			WHERE seq > ? AND seq < ? ORDER BY seq LIMIT ?`, lo, hi, limit)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var seq, created int64
			var r EventRow
			if err := rows.Scan(&seq, &r.Key, &created, &r.Body); err != nil {
				return err
			}
			r.Seq, r.Created = toUint(seq), time.UnixMilli(created).UTC()
			out = append(out, r)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("ring: read events: %w", err)
	}
	return out, nil
}

// TrimEvents deletes the entries at or below upTo, which the hub holds (a
// HelloAck floor or an EventAck; 02i). It is not loss: no counter moves.
func (d *DB) TrimEvents(ctx context.Context, upTo uint64) (int, Size, error) {
	v, err := toInt("seq", min(upTo, uint64(1<<63-1)))
	if err != nil {
		return 0, Size{}, err
	}
	var n int64
	var size Size
	err = d.s.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `DELETE FROM event_ring WHERE seq <= ?`, v)
		if err != nil {
			return fmt.Errorf("ring: trim events: %w", err)
		}
		if n, err = res.RowsAffected(); err != nil {
			return fmt.Errorf("ring: trim events: %w", err)
		}
		size, err = readSize(ctx, tx)
		return err
	})
	if err != nil {
		return 0, Size{}, err
	}
	return int(n), size, nil
}

// EventsSize is what the ring holds now.
func (d *DB) EventsSize(ctx context.Context) (Size, error) {
	var size Size
	err := d.s.Read(ctx, func(tx *sql.Tx) error {
		var err error
		size, err = readSize(ctx, tx)
		return err
	})
	return size, err
}
