package ring_test

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/agent/ring"
)

var t0 = time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)

func command(id string, epoch, seq uint64) ring.Command {
	return ring.Command{ID: id, Kind: "capture_pane", Epoch: epoch, Seq: seq, Received: t0, NotAfter: t0.Add(time.Minute)}
}

// 02i: a known command_id is answered from the log; a re-issued command keeps
// its id under a new epoch and seq, and the log keeps the first entry.
func TestACommandIDIsLoggedOnce(t *testing.T) {
	t.Parallel()
	db := open(t, stateDir(t))
	ctx := context.Background()

	first, dup, err := db.LogCommand(ctx, command("c-1", 3, 10))
	if err != nil || dup {
		t.Fatalf("first LogCommand: dup %v, err %v; want a new entry", dup, err)
	}
	if first.Status != ring.StatusReceived || first.Result != nil {
		t.Errorf("a new entry is %+v, want received with no result", first)
	}
	again, dup, err := db.LogCommand(ctx, command("c-1", 4, 2))
	if err != nil || !dup {
		t.Fatalf("re-issued LogCommand: dup %v, err %v; want the logged entry", dup, err)
	}
	if again.Epoch != 3 || again.Seq != 10 {
		t.Errorf("the duplicate answered epoch %d seq %d, want the logged 3 and 10", again.Epoch, again.Seq)
	}

	result := []byte("result-1")
	if err := db.RecordResult(ctx, "c-1", result, t0.Add(time.Second)); err != nil {
		t.Fatalf("RecordResult: %v", err)
	}
	logged, dup, err := db.LogCommand(ctx, command("c-1", 5, 1))
	if err != nil || !dup {
		t.Fatalf("LogCommand after the result: dup %v, err %v", dup, err)
	}
	if logged.Status != ring.StatusFinished || !bytes.Equal(logged.Result, result) || !logged.Finished.Equal(t0.Add(time.Second)) {
		t.Errorf("the duplicate answered %+v, want the logged result", logged)
	}
	if err := db.RecordResult(ctx, "c-1", []byte("result-2"), t0.Add(2*time.Second)); !errors.Is(err, ring.ErrResultLogged) {
		t.Errorf("a second result: %v, want ErrResultLogged", err)
	}
	if got, err := db.Command(ctx, "c-1"); err != nil || !bytes.Equal(got.Result, result) {
		t.Errorf("after a second result the log holds %q (err %v), want the first", got.Result, err)
	}
	if err := db.RecordResult(ctx, "c-unknown", result, t0); !errors.Is(err, ring.ErrUnknownCommand) {
		t.Errorf("a result for an unknown command: %v, want ErrUnknownCommand", err)
	}
	if _, err := db.Command(ctx, "c-unknown"); !errors.Is(err, ring.ErrUnknownCommand) {
		t.Errorf("Command of an unknown id: %v, want ErrUnknownCommand", err)
	}
}

// Many deliveries of one command at once: the unique key lets exactly one in.
func TestConcurrentDeliveriesLogOneEntry(t *testing.T) {
	t.Parallel()
	db := open(t, stateDir(t))
	const n = 20
	var wg sync.WaitGroup
	news := make(chan bool, n)
	for i := range n {
		wg.Go(func() {
			_, dup, err := db.LogCommand(context.Background(), command("c-race", 1, uint64(i+1))) //nolint:gosec // G115: i is below 20
			if err != nil {
				t.Errorf("LogCommand: %v", err)
				return
			}
			news <- !dup
		})
	}
	wg.Wait()
	close(news)
	fresh := 0
	for isNew := range news {
		if isNew {
			fresh++
		}
	}
	if fresh != 1 {
		t.Errorf("%d deliveries were logged as new, want exactly 1", fresh)
	}
}

// The file backs the rules: a second row for one command_id and a changed
// result are refused whoever writes them.
func TestTheFileKeepsOneEntryAndItsResult(t *testing.T) {
	t.Parallel()
	dir := stateDir(t)
	db := open(t, dir)
	ctx := context.Background()
	if _, _, err := db.LogCommand(ctx, command("c-1", 1, 1)); err != nil {
		t.Fatal(err)
	}
	if err := db.RecordResult(ctx, "c-1", []byte("r"), t0); err != nil {
		t.Fatal(err)
	}
	f := raw(t, dir)
	for _, q := range []struct{ sql, why string }{
		{`INSERT INTO command_log(command_id, kind, epoch, seq, received_time, not_after_time, status) VALUES ('c-1', 'k', 2, 2, 0, 0, 'received')`, "UNIQUE constraint"},
		{`UPDATE command_log SET result = x'00' WHERE command_id = 'c-1'`, "result is kept"},
		{`UPDATE command_log SET status = 'received', result = NULL, finished_time = NULL WHERE command_id = 'c-1'`, "result is kept"},
		{`INSERT INTO command_log(command_id, kind, epoch, seq, received_time, not_after_time, status) VALUES ('c-2', 'k', 2, 2, 0, 0, 'finished')`, "CHECK constraint"},
	} {
		if _, err := f.ExecContext(ctx, q.sql); err == nil || !strings.Contains(err.Error(), q.why) {
			t.Errorf("%q gave %v, want a refusal naming %q", q.sql, err, q.why)
		}
	}
}

func TestLogCommandRefusesAnEmptyID(t *testing.T) {
	t.Parallel()
	db := open(t, stateDir(t))
	if _, _, err := db.LogCommand(context.Background(), command("", 1, 1)); err == nil {
		t.Error("LogCommand with an empty command_id succeeded")
	}
}
