package exec

import (
	"context"
	"testing"
	"time"
)

// TestDecideRefusesShapes pins the file layout's guards: a malformed id
// never becomes a file name, and a second answer never overwrites the
// first.
func TestDecideRefusesShapes(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	for _, id := range []string{"", "../x", "A", "a/b", "a b", "a_1", "toolong-" + string(make([]byte, 40))} {
		if err := Decide(dir, id, true); err == nil {
			t.Fatalf("Decide(%q) accepted a malformed command id", id)
		}
	}
	if err := Decide(dir, "01kn farewell", true); err == nil {
		t.Fatalf("Decide accepted a command id with a space")
	}
	if err := Decide(dir, "01kn12345678901234567890", true); err != nil {
		t.Fatalf("Decide refused a valid command id: %v", err)
	}
	if err := Decide(dir, "01kn12345678901234567890", false); err == nil {
		t.Fatalf("Decide let a deny overwrite an approval")
	}
}

// TestFileApproverDecides proves the loop the envelope waits on: a CLI
// decision file written before or during the wait answers it, the file is
// consumed, and a deny answers denied.
func TestFileApproverDecides(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	a := FileApprover{Dir: dir}
	if err := Decide(dir, "01kapprove1", true); err != nil {
		t.Fatal(err)
	}
	approved, channel, err := a.Await(t.Context(), "01kapprove1", "RunHarnessCommand")
	if err != nil || !approved || channel != "cli" {
		t.Fatalf("Await = (%v, %q, %v), want (true, cli, nil)", approved, channel, err)
	}
	// Consumed: a second wait finds nothing and ends with the context.
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	if _, _, err := a.Await(ctx, "01kapprove1", "RunHarnessCommand"); err == nil {
		t.Fatal("Await answered from a consumed decision file")
	}
	if err := Decide(dir, "01kdeny0001", false); err != nil {
		t.Fatal(err)
	}
	approved, channel, err = a.Await(t.Context(), "01kdeny0001", "RunHarnessCommand")
	if err != nil || approved || channel != "cli" {
		t.Fatalf("Await = (%v, %q, %v), want (false, cli, nil)", approved, channel, err)
	}
}

// TestFileApproverWaitsForLateDecision proves a wait started before the
// person answers still gets it: the decision lands mid-wait.
func TestFileApproverWaitsForLateDecision(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	a := FileApprover{Dir: dir}
	done := make(chan struct{})
	var approved bool
	var channel string
	var aerr error
	go func() {
		defer close(done)
		approved, channel, aerr = a.Await(context.Background(), "01klate0001", "RespawnPane")
	}()
	time.Sleep(50 * time.Millisecond)
	if err := Decide(dir, "01klate0001", true); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Await never saw the late decision")
	}
	if aerr != nil || !approved || channel != "cli" {
		t.Fatalf("Await = (%v, %q, %v), want (true, cli, nil)", approved, channel, aerr)
	}
}

// TestFileApproverStopsWithContext proves a wait with no answer ends when
// its context does, and that a malformed id or a missing folder is no
// decision rather than a hang.
func TestFileApproverStopsWithContext(t *testing.T) {
	t.Parallel()
	a := FileApprover{Dir: t.TempDir()}
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	if _, _, err := a.Await(ctx, "01knothing1", "SendKeys"); err == nil {
		t.Fatal("Await answered with no decision file")
	}
	if _, _, err := a.Await(t.Context(), "../x", "SendKeys"); err == nil {
		t.Fatal("Await waited on a malformed command id")
	}
	if _, _, err := (FileApprover{}).Await(t.Context(), "01knothing2", "SendKeys"); err == nil {
		t.Fatal("Await waited with no approvals folder")
	}
}
