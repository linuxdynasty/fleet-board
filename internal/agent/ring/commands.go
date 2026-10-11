package ring

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Errors of the command log.
var (
	// ErrUnknownCommand means no entry has that command_id.
	ErrUnknownCommand = errors.New("ring: no such command in the log")
	// ErrResultLogged means the command already has its one result; the
	// logged one stays.
	ErrResultLogged = errors.New("ring: the command already has a result")
)

// Status is where a logged command stands.
type Status string

// The statuses of a command log entry. Running is set by the executor
// (plan row 6) as steps are reached.
const (
	StatusReceived Status = "received"
	StatusRunning  Status = "running"
	StatusFinished Status = "finished"
)

// Command is what the log keeps of a received command's envelope.
type Command struct {
	ID       string
	Kind     string
	Epoch    uint64
	Seq      uint64
	Received time.Time
	NotAfter time.Time
	// Body is the encoded Command, kept so an entry the agent had not
	// finished can be run or resumed after a restart (ruling fork 4 iii).
	Body []byte
}

// Entry is one command log row.
type Entry struct {
	Command
	Status Status
	Step   int
	// Result is the encoded CommandResult, nil until finished.
	Result   []byte
	Finished time.Time
	// Acked is when the hub's ResultAck arrived; zero until then.
	Acked time.Time
}

// LogCommand saves c before CommandAck is sent. When c.ID is already logged
// it saves nothing and returns the logged entry with dup set, so the caller
// answers from the log without running the command again (02i).
func (d *DB) LogCommand(ctx context.Context, c Command) (Entry, bool, error) {
	epoch, seq, err := c.ints()
	if err != nil {
		return Entry{}, false, err
	}
	var out Entry
	var dup bool
	err = d.s.Write(ctx, func(tx *sql.Tx) error {
		var err error
		out, dup, err = insertCommand(ctx, tx, c, epoch, seq)
		return err
	})
	if err != nil {
		return Entry{}, false, err
	}
	return out, dup, nil
}

// ints checks c's id and kind and converts its epoch and seq for SQLite.
func (c Command) ints() (epoch, seq int64, err error) {
	if c.ID == "" || c.Kind == "" {
		return 0, 0, errors.New("ring: a command needs an id and a kind")
	}
	if epoch, err = toInt("epoch", c.Epoch); err != nil {
		return 0, 0, err
	}
	seq, err = toInt("seq", c.Seq)
	return epoch, seq, err
}

// insertCommand logs c unless its id is logged, and returns the entry; dup
// is true when it was already there and nothing was saved.
func insertCommand(ctx context.Context, tx *sql.Tx, c Command, epoch, seq int64) (Entry, bool, error) {
	res, err := tx.ExecContext(ctx, `INSERT INTO command_log(command_id, kind, epoch, seq, received_time,
		not_after_time, status, command) SELECT ?, ?, ?, ?, ?, ?, ?, ?
		WHERE NOT EXISTS (SELECT 1 FROM command_log WHERE command_id = ?)`,
		c.ID, c.Kind, epoch, seq, c.Received.UnixMilli(), c.NotAfter.UnixMilli(), string(StatusReceived), c.Body, c.ID)
	if err != nil {
		return Entry{}, false, fmt.Errorf("ring: log command: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return Entry{}, false, fmt.Errorf("ring: log command: %w", err)
	}
	e, err := readEntry(ctx, tx, c.ID)
	return e, n == 0, err
}

// RecordResult stores the command's one result. A second result is
// [ErrResultLogged] and the first is kept; an unknown id is
// [ErrUnknownCommand].
func (d *DB) RecordResult(ctx context.Context, id string, result []byte, at time.Time) error {
	if result == nil {
		result = []byte{}
	}
	return d.s.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE command_log SET status = ?, result = ?, finished_time = ?
			WHERE command_id = ? AND status <> ?`,
			string(StatusFinished), result, at.UnixMilli(), id, string(StatusFinished))
		if err != nil {
			return fmt.Errorf("ring: record result: %w", err)
		}
		if n, err := res.RowsAffected(); err != nil || n == 1 {
			return err
		}
		if _, err := readEntry(ctx, tx, id); err != nil {
			return err
		}
		return ErrResultLogged
	})
}

// Command reads one entry; an unknown id is [ErrUnknownCommand].
func (d *DB) Command(ctx context.Context, id string) (Entry, error) {
	var e Entry
	err := d.s.Read(ctx, func(tx *sql.Tx) error {
		var err error
		e, err = readEntry(ctx, tx, id)
		return err
	})
	return e, err
}

func readEntry(ctx context.Context, tx *sql.Tx, id string) (Entry, error) {
	var e Entry
	var epoch, seq, received, notAfter int64
	var status string
	var finished, acked sql.NullInt64
	err := tx.QueryRowContext(ctx, `SELECT command_id, kind, epoch, seq, received_time, not_after_time, status,
		step_reached, result, finished_time, acked_time, command FROM command_log WHERE command_id = ?`, id).
		Scan(&e.ID, &e.Kind, &epoch, &seq, &received, &notAfter, &status, &e.Step, &e.Result, &finished, &acked, &e.Body)
	if errors.Is(err, sql.ErrNoRows) {
		return Entry{}, ErrUnknownCommand
	}
	if err != nil {
		return Entry{}, fmt.Errorf("ring: read command log: %w", err)
	}
	e.Epoch, e.Seq, e.Status = toUint(epoch), toUint(seq), Status(status)
	e.Received, e.NotAfter = time.UnixMilli(received).UTC(), time.UnixMilli(notAfter).UTC()
	if finished.Valid {
		e.Finished = time.UnixMilli(finished.Int64).UTC()
	}
	if acked.Valid {
		e.Acked = time.UnixMilli(acked.Int64).UTC()
	}
	return e, nil
}
