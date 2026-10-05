package store_test

import (
	"bufio"
	"context"
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/store"
)

const crashHelperEnv = "FLEET_STORE_CRASH_HELPER_PATH"

// TestAckedWritesSurviveAKilledProcess is the crash between commit and ack:
// a child process writes as fast as it can and prints each acknowledged row,
// the test kills it with SIGKILL mid-stream (no Close, no flush), reopens the
// file and checks that every acknowledged row is there.
func TestAckedWritesSurviveAKilledProcess(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	cmd := exec.Command(os.Args[0], "-test.run=^TestCrashHelper$", "-test.count=1")
	cmd.Env = append(os.Environ(), crashHelperEnv+"="+h.path)
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start helper: %v", err)
	}
	var acked []string
	killed := false
	sc := bufio.NewScanner(out)
	for sc.Scan() {
		v, ok := strings.CutPrefix(sc.Text(), "ack ")
		if !ok {
			continue
		}
		acked = append(acked, v)
		if len(acked) == 1000 && !killed {
			if err := cmd.Process.Kill(); err != nil {
				t.Fatalf("kill helper: %v", err)
			}
			killed = true
		}
	}
	waitErr := cmd.Wait()
	if !killed {
		t.Fatalf("helper ended before 1000 acks (%d): %v", len(acked), waitErr)
	}

	s := h.open(t, h.options(itemsV1))
	stored := map[string]bool{}
	err = s.Read(context.Background(), func(tx *sql.Tx) error {
		rows, err := tx.Query("SELECT v FROM items")
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var v string
			if err := rows.Scan(&v); err != nil {
				return err
			}
			stored[v] = true
		}
		return rows.Err()
	})
	if err != nil {
		t.Fatalf("read after reopen: %v", err)
	}
	var lost []string
	for _, v := range acked {
		if !stored[v] {
			lost = append(lost, v)
		}
	}
	if len(lost) > 0 {
		t.Fatalf("%d of %d acknowledged rows lost after the kill, first %v", len(lost), len(acked), lost[0])
	}
	t.Logf("killed after %d acks; %d rows on disk after reopen", len(acked), len(stored))
}

// TestCrashHelper is the child process of TestAckedWritesSurviveAKilledProcess.
func TestCrashHelper(t *testing.T) {
	path := os.Getenv(crashHelperEnv)
	if path == "" {
		t.Skip("child process of TestAckedWritesSurviveAKilledProcess")
	}
	ctx := context.Background()
	s, err := store.Open(ctx, store.Options{Path: path, Name: "state", Migrations: []store.Migration{itemsV1}})
	if err != nil {
		t.Fatalf("helper open: %v", err)
	}
	var mu sync.Mutex
	deadline := time.Now().Add(30 * time.Second) // the parent kills it long before
	for i := 0; time.Now().Before(deadline); i++ {
		v := fmt.Sprint(i)
		done, err := s.Submit(ctx, insert(v))
		if err != nil {
			t.Fatalf("helper submit: %v", err)
		}
		go func() {
			if <-done == nil {
				mu.Lock()
				_, _ = fmt.Fprintf(os.Stdout, "ack %s\n", v)
				mu.Unlock()
			}
		}()
	}
}
