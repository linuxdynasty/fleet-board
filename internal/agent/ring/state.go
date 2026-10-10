package ring

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
)

// Errors of the epoch and command seq rules (02i "Connection epoch").
var (
	// ErrEpochNotAbove means a HelloAck.epoch at or below the last one: the
	// agent drops that connection (epoch_regressed).
	ErrEpochNotAbove = errors.New("ring: epoch is not above the last one")
	// ErrWrongEpoch means a command of an epoch that is not the current one.
	ErrWrongEpoch = errors.New("ring: command is not of the current epoch")
	// ErrSeqReplayed means a command seq at or below the last applied one in
	// this epoch (SEQ_REPLAYED).
	ErrSeqReplayed = errors.New("ring: command seq was already applied")
)

// Marks are the event seq high-water mark and the EventCounters, persisted
// together (02i rule 1, 03n W2). None of them ever decreases.
type Marks struct {
	SeqHWM    uint64
	Shed      uint64
	Coalesced uint64
	Evicted   uint64
}

// State is the agent_state row.
type State struct {
	InstallID string
	Marks     Marks
	// Epoch and CommandSeq are Hello.last_epoch and
	// Hello.last_applied_command_seq.
	Epoch      uint64
	CommandSeq uint64
}

// State reads the agent_state row.
func (d *DB) State(ctx context.Context) (State, error) {
	var st State
	err := d.s.Read(ctx, func(tx *sql.Tx) error {
		var err error
		st, err = readState(ctx, tx)
		return err
	})
	return st, err
}

func readState(ctx context.Context, tx *sql.Tx) (State, error) {
	var st State
	var v [6]int64
	err := tx.QueryRowContext(ctx, `SELECT install_id, seq_hwm, shed_total, coalesced_total, evicted_total,
		last_epoch, last_applied_command_seq FROM agent_state WHERE id = 1`).
		Scan(&st.InstallID, &v[0], &v[1], &v[2], &v[3], &v[4], &v[5])
	if err != nil {
		return State{}, fmt.Errorf("ring: read agent_state: %w", err)
	}
	st.Marks = Marks{SeqHWM: toUint(v[0]), Shed: toUint(v[1]), Coalesced: toUint(v[2]), Evicted: toUint(v[3])}
	st.Epoch, st.CommandSeq = toUint(v[4]), toUint(v[5])
	return st, nil
}

// RaiseMarks stores each field of m that is above the stored one and
// returns what is stored after the commit. A lower field is ignored, so
// marks never go backwards. Call it before handing out a seq above the
// stored SeqHWM (02i rule 1: reserve in blocks of 1024).
func (d *DB) RaiseMarks(ctx context.Context, m Marks) (Marks, error) {
	var args [4]int64
	for i, f := range []struct {
		name string
		v    uint64
	}{{"seq_hwm", m.SeqHWM}, {"shed_total", m.Shed}, {"coalesced_total", m.Coalesced}, {"evicted_total", m.Evicted}} {
		var err error
		if args[i], err = toInt(f.name, f.v); err != nil {
			return Marks{}, err
		}
	}
	var out Marks
	err := d.s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE agent_state SET seq_hwm = max(seq_hwm, ?), shed_total = max(shed_total, ?),
			coalesced_total = max(coalesced_total, ?), evicted_total = max(evicted_total, ?) WHERE id = 1`,
			args[0], args[1], args[2], args[3])
		if err != nil {
			return fmt.Errorf("ring: raise marks: %w", err)
		}
		st, err := readState(ctx, tx)
		out = st.Marks
		return err
	})
	if err != nil {
		return Marks{}, err
	}
	return out, nil
}

// StartEpoch records the epoch of a new connection (HelloAck.epoch) and
// starts its command seq at zero. An epoch at or below the stored one is
// [ErrEpochNotAbove], one above [MaxEpoch] [ErrOutOfRange]; neither changes
// anything.
func (d *DB) StartEpoch(ctx context.Context, epoch uint64) error {
	if err := CheckHubMarks(epoch, 0); err != nil {
		return err
	}
	e, err := toInt("epoch", epoch)
	if err != nil {
		return err
	}
	return d.s.Write(ctx, func(tx *sql.Tx) error {
		st, err := readState(ctx, tx)
		if err != nil {
			return err
		}
		if epoch <= st.Epoch {
			return fmt.Errorf("%w: %d after %d", ErrEpochNotAbove, epoch, st.Epoch)
		}
		_, err = tx.ExecContext(ctx, `UPDATE agent_state SET last_epoch = ?, last_applied_command_seq = 0 WHERE id = 1`, e)
		return err
	})
}

// ApplyCommandSeq records seq as the last applied command of epoch. A seq
// of another epoch is [ErrWrongEpoch]; one at or below the last applied is
// [ErrSeqReplayed]. Neither changes anything.
func (d *DB) ApplyCommandSeq(ctx context.Context, epoch, seq uint64) error {
	s, err := toInt("seq", seq)
	if err != nil {
		return err
	}
	return d.s.Write(ctx, func(tx *sql.Tx) error {
		st, err := readState(ctx, tx)
		if err != nil {
			return err
		}
		switch {
		case st.Epoch == 0 || epoch != st.Epoch:
			return fmt.Errorf("%w: %d, current %d", ErrWrongEpoch, epoch, st.Epoch)
		case seq <= st.CommandSeq:
			return fmt.Errorf("%w: %d after %d", ErrSeqReplayed, seq, st.CommandSeq)
		}
		_, err = tx.ExecContext(ctx, `UPDATE agent_state SET last_applied_command_seq = ? WHERE id = 1`, s)
		return err
	})
}

// The largest HelloAck values agent.db takes (02i). It stores them as
// SQLite's signed 64-bit integers and never lowers them. MaxEpoch keeps the
// hub's next epoch (the stored one plus 1) inside a signed 64-bit integer;
// the agent would refuse that next epoch, so it does not leave room for
// another stream. MaxHubLastSeq keeps the first block Next reserves past
// the floor (the floor plus 1024) inside one. A larger value, saved, would
// overflow those sums and stop every later epoch or seq.
const (
	MaxEpoch      uint64 = math.MaxInt64 - 1
	MaxHubLastSeq uint64 = math.MaxInt64 - seqBlock
)

// ErrOutOfRange means a HelloAck epoch or hub_last_event_seq above
// [MaxEpoch] or [MaxHubLastSeq]; nothing was saved.
var ErrOutOfRange = errors.New("ring: the hub's value is too large for agent.db")

// CheckHubMarks refuses a HelloAck epoch or hub_last_event_seq that
// agent.db cannot keep ([ErrOutOfRange]). Check both before saving either.
func CheckHubMarks(epoch, hubLastSeq uint64) error {
	switch {
	case epoch > MaxEpoch:
		return fmt.Errorf("%w: epoch %d is above %d", ErrOutOfRange, epoch, MaxEpoch)
	case hubLastSeq > MaxHubLastSeq:
		return fmt.Errorf("%w: hub_last_event_seq %d is above %d", ErrOutOfRange, hubLastSeq, MaxHubLastSeq)
	}
	return nil
}
