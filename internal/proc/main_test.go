//go:build unix

package proc_test

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"testing"
	"time"
)

// The fake children are this test binary run again: TestMain looks at
// FLEET_PROC_CHILD and, when it is set, plays that child instead of running
// the tests. No external tool is needed.
const (
	childEnv     = "FLEET_PROC_CHILD"
	pidFileEnv   = "FLEET_PROC_PIDFILE"
	exitCodeEnv  = "FLEET_PROC_EXIT"
	grandSetsEnv = "FLEET_PROC_GRAND_SETSID"
	splitEnv     = "FLEET_PROC_SPLIT_BYTES"
	grandModeEnv = "FLEET_PROC_GRAND_MODE" // hold (default) or late-flood
	lateMsEnv    = "FLEET_PROC_LATE_MS"    // late-flood: ms to wait after the parent is gone (default 400)
)

func TestMain(m *testing.M) {
	if mode := os.Getenv(childEnv); mode != "" {
		runChild(mode)
		return
	}
	// A race-enabled binary sleeps up to a second at exit (GORACE atexit_sleep_ms),
	// which would keep every fake child alive that long after it exited. The
	// children inherit this environment.
	_ = os.Setenv("GORACE", "atexit_sleep_ms=0")
	// A Run that handed its child this process's stdin would give it
	// /dev/zero, which never ends: the stdin test then sees bytes, not EOF.
	if zero, err := os.Open("/dev/zero"); err == nil {
		os.Stdin = zero
	}
	os.Exit(m.Run())
}

func runChild(mode string) {
	switch mode {
	case "hang":
		time.Sleep(time.Hour)
	case "flood":
		chunk := make([]byte, 32<<10)
		for i := range chunk {
			chunk[i] = 'x'
		}
		for {
			if _, err := os.Stdout.Write(chunk); err != nil {
				time.Sleep(time.Hour)
			}
		}
	case "stdin":
		// At most 1 MiB, so an inherited /dev/zero cannot run forever.
		b, err := io.ReadAll(io.LimitReader(os.Stdin, 1<<20))
		fmt.Printf("stdin bytes=%d err=%v\n", len(b), err)
	case "write-hang":
		_, _ = os.Stdout.WriteString("partial")
		time.Sleep(time.Hour)
	case "split":
		n, _ := strconv.Atoi(os.Getenv(splitEnv))
		chunk := make([]byte, n)
		_, _ = os.Stdout.Write(chunk)
		_, _ = os.Stderr.Write(chunk)
	case "echo":
		_, _ = fmt.Fprint(os.Stdout, "to-stdout")
		_, _ = fmt.Fprint(os.Stderr, "to-stderr")
		code, _ := strconv.Atoi(os.Getenv(exitCodeEnv))
		os.Exit(code)
	case "parent-exits", "parent-hangs", "parent-exits-quiet":
		// Start a grandchild that holds our stdout and stderr, wait until it
		// has written its pid, then exit or hang.
		g := exec.CommandContext(context.Background(), os.Args[0]) //nolint:gosec // G204: the test binary starts itself as a fake grandchild
		gm := os.Getenv(grandModeEnv)
		if gm == "" {
			gm = "hold"
		}
		g.Env = append(os.Environ(), childEnv+"="+gm)
		if mode != "parent-exits-quiet" {
			g.Stdout, g.Stderr = os.Stdout, os.Stderr // else the null device: nothing holds the pipes
		}
		if os.Getenv(grandSetsEnv) != "" {
			g.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		}
		if err := g.Start(); err != nil {
			fmt.Fprintln(os.Stderr, "start grandchild:", err)
			os.Exit(3)
		}
		path := os.Getenv(pidFileEnv)
		for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
			if b, err := os.ReadFile(path); err == nil && len(b) > 0 { //nolint:gosec // G304: the path comes from the parent test's own t.TempDir
				break
			}
		}
		if mode == "parent-hangs" {
			time.Sleep(time.Hour)
		}
		code, _ := strconv.Atoi(os.Getenv(exitCodeEnv))
		os.Exit(code)
	case "hold", "late-flood":
		path := os.Getenv(pidFileEnv)
		tmp := path + ".tmp"
		if err := os.WriteFile(tmp, []byte(strconv.Itoa(os.Getpid())), 0o600); err == nil { //nolint:gosec // G703: the path comes from the parent test's own t.TempDir
			_ = os.Rename(tmp, path) //nolint:gosec // G703: same path
		}
		if mode == "late-flood" {
			// After the parent has exited (this process is then adopted by
			// another parent) and, a moment later, been reaped by Run.
			for orig, end := os.Getppid(), time.Now().Add(10*time.Second); os.Getppid() == orig && time.Now().Before(end); {
				time.Sleep(5 * time.Millisecond)
			}
			late := 400
			if v, err := strconv.Atoi(os.Getenv(lateMsEnv)); err == nil {
				late = v
			}
			time.Sleep(time.Duration(late) * time.Millisecond)
			n, _ := strconv.Atoi(os.Getenv(splitEnv))
			_, _ = os.Stdout.Write(make([]byte, n))
		}
		time.Sleep(time.Hour)
	default:
		fmt.Fprintln(os.Stderr, "unknown child mode", mode)
		os.Exit(2)
	}
}
