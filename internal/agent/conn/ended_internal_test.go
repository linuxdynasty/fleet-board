package conn

import (
	"bufio"
	"errors"
	"math/rand/v2"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

const endedHelperEnv = "FLEET_CONN_ENDED_HELPER"

// A kill at any point of the ended.json write leaves no file or a whole
// one, never a part: a child process rewrites it in a loop, alternating two
// records of different sizes, and is killed with SIGKILL (no flush, no
// cleanup) at a random moment, twenty times.
func TestAKillDuringTheEndWriteLeavesNoneOrAWholeOne(t *testing.T) {
	t.Parallel()
	id := Identity{MachineID: "01J0000000000000000000KILL"}
	var none, whole int
	for range 20 {
		cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestEndedWriteHelper$", "-test.count=1") //nolint:gosec // G204: re-runs this test binary as the helper
		cmd.Env = append(os.Environ(), endedHelperEnv+"=1")
		out, err := cmd.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		// The helper makes its own state folder and prints "writing <dir>"
		// just before its first write.
		line, err := bufio.NewReader(out).ReadString('\n')
		dir, ok := strings.CutPrefix(strings.TrimSpace(line), "writing ")
		if err != nil || !ok {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			t.Fatalf("helper said %q, %v", line, err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(dir) })
		time.Sleep(time.Duration(rand.IntN(40)) * time.Millisecond) //nolint:gosec // G404: a random kill moment, not a secret
		if err := cmd.Process.Kill(); err != nil {
			t.Fatal(err)
		}
		_ = cmd.Wait()
		removed, err := CheckEnded(dir, id)
		var e *EndedError
		switch {
		case removed:
			t.Fatal("the helper's record names another machine")
		case err == nil:
			none++
		case errors.As(err, &e) && e.End == EndRevoked && strings.HasPrefix(e.Peer, "hub"):
			whole++
		default:
			t.Fatalf("after a kill: %v", err)
		}
	}
	t.Logf("20 kills: %d left no ended.json, %d a whole one", none, whole)
}

// TestEndedWriteHelper is the child process of
// TestAKillDuringTheEndWriteLeavesNoneOrAWholeOne.
func TestEndedWriteHelper(t *testing.T) {
	if os.Getenv(endedHelperEnv) == "" {
		t.Skip("child process of TestAKillDuringTheEndWriteLeavesNoneOrAWholeOne")
	}
	// Mode 0700. The helper is killed, so t.TempDir would never clean up;
	// the parent removes it.
	dir, err := os.MkdirTemp("", "ended-kill-") //nolint:usetesting // see above
	if err != nil {
		t.Fatal(err)
	}
	recs := []ended{
		{Version: endedVersion, End: EndRevoked, MachineID: "01J0000000000000000000KILL", Peer: "hub.example:7443"},
		{Version: endedVersion, End: EndRevoked, MachineID: "01J0000000000000000000KILL", Peer: "hub" + strings.Repeat("x", maxEndedPeer-3)},
	}
	if _, err := os.Stdout.WriteString("writing " + dir + "\n"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(30 * time.Second) // the parent kills it long before
	for i := 0; time.Now().Before(deadline); i++ {
		rec := recs[i%2]
		rec.Time = time.Now().UTC()
		if err := saveEnded(dir, rec); err != nil {
			t.Fatal(err)
		}
	}
}
