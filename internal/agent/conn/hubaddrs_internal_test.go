package conn

import (
	"bufio"
	"errors"
	"fmt"
	"io/fs"
	"math/rand/v2"
	"os"
	"os/exec"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"
)

const hubAddrsHelperEnv = "FLEET_CONN_HUB_ADDRESSES_HELPER"

// killLists are the two lists the helper alternates: one address and the
// full 16, so a torn write would show as neither.
func killLists() [2][]string {
	var full []string
	for i := range maxHubAddresses {
		full = append(full, fmt.Sprintf("hub%02d.example:7443", i))
	}
	return [2][]string{{"hub.example:7443"}, full}
}

// A kill at any point of the hub-addresses write leaves the old list or
// the new one whole, never a part: a child process rewrites it in a loop
// and is killed with SIGKILL at a random moment, twenty times.
func TestAKillDuringTheAddressListWriteLeavesAWholeList(t *testing.T) {
	t.Parallel()
	lists := killLists()
	counts := map[int]int{}
	for range 20 {
		cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestHubAddressesWriteHelper$", "-test.count=1") //nolint:gosec // G204: re-runs this test binary as the helper
		cmd.Env = append(os.Environ(), hubAddrsHelperEnv+"=1")
		out, err := cmd.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
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
		// A helper that ended on its own (a failed write) proves nothing.
		var ee *exec.ExitError
		if err := cmd.Wait(); !errors.As(err, &ee) || !killed(ee) {
			t.Fatalf("the helper ended before the kill: %v", err)
		}
		got, dropped, err := loadHubAddresses(dir)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			counts[0]++
		case err != nil:
			t.Fatalf("after a kill: %v", err)
		case dropped == 0 && slices.Equal(got, lists[0]):
			counts[1]++
		case dropped == 0 && slices.Equal(got, lists[1]):
			counts[16]++
		default:
			t.Fatalf("after a kill hub-addresses holds %d addresses (%d dropped): %v", len(got), dropped, got)
		}
	}
	t.Logf("20 kills: %d left no file, %d the short list, %d the full one", counts[0], counts[1], counts[16])
}

// TestHubAddressesWriteHelper is the child process of
// TestAKillDuringTheAddressListWriteLeavesAWholeList.
func TestHubAddressesWriteHelper(t *testing.T) {
	if os.Getenv(hubAddrsHelperEnv) == "" {
		t.Skip("child process of TestAKillDuringTheAddressListWriteLeavesAWholeList")
	}
	// The helper is killed, so t.TempDir would never clean up; the parent
	// removes it.
	dir, err := os.MkdirTemp("", "hub-addresses-kill-") //nolint:usetesting // see above
	if err != nil {
		t.Fatal(err)
	}
	lists := killLists()
	if _, err := os.Stdout.WriteString("writing " + dir + "\n"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(30 * time.Second) // the parent kills it long before
	for i := 0; time.Now().Before(deadline); i++ {
		if err := saveHubAddresses(dir, lists[i%2]); err != nil {
			t.Fatal(err)
		}
	}
}

// killed reports whether the process ended by SIGKILL.
func killed(ee *exec.ExitError) bool {
	ws, ok := ee.Sys().(syscall.WaitStatus)
	return ok && ws.Signaled() && ws.Signal() == syscall.SIGKILL
}
