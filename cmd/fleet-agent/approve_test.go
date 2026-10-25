package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// runDecide runs `fleet-agent approve|deny` against a throwaway state dir
// and returns what it printed and what it returned.
func runDecide(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	var out bytes.Buffer
	dir := t.TempDir()
	resolve := func() (string, error) { return dir, nil }
	root := &cobra.Command{Use: "fleet-agent", SilenceUsage: true, SilenceErrors: true}
	root.AddCommand(approveCommands(resolve, &out)...)
	root.SetArgs(args)
	err := root.ExecuteContext(t.Context())
	return out.String(), dir, err
}

func TestApproveWritesADecisionFile(t *testing.T) {
	t.Parallel()
	out, dir, err := runDecide(t, "approve", "01kcliapprove1")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "01kcliapprove1 approved") {
		t.Fatalf("approve printed %q, want the id and the verb", out)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "approvals", "01kcliapprove1"))
	if err != nil {
		t.Fatalf("approve wrote no decision file: %v", err)
	}
	if string(raw) != "approved" {
		t.Fatalf("approve wrote %q, want the approved verdict", raw)
	}
}

func TestDenyWritesADecisionFile(t *testing.T) {
	t.Parallel()
	out, dir, err := runDecide(t, "deny", "01kclideny0001")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "01kclideny0001 denied") {
		t.Fatalf("deny printed %q, want the id and the verb", out)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "approvals", "01kclideny0001"))
	if err != nil {
		t.Fatalf("deny wrote no decision file: %v", err)
	}
	if string(raw) != "denied" {
		t.Fatalf("deny wrote %q, want the denied verdict", raw)
	}
}

func TestDecideRefusesShapes(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{
		{"approve"},
		{"approve", "01kone000000000000000001", "extra"},
		{"approve", "../escape"},
		{"deny", "NOT-LOWER"},
	} {
		t.Run(strings.Join(args, "/"), func(t *testing.T) {
			t.Parallel()
			if _, _, err := runDecide(t, args...); err == nil {
				t.Fatalf("decide %q succeeded, want a usage or decision error", args)
			}
		})
	}
}

func TestDecideTwiceFails(t *testing.T) {
	t.Parallel()
	// runDecide uses a fresh state dir per call, so drive both answers
	// through one dir: approve, then deny must fail already-decided.
	var out bytes.Buffer
	dir := t.TempDir()
	resolve := func() (string, error) { return dir, nil }
	decide := func(args ...string) error {
		root := &cobra.Command{Use: "fleet-agent", SilenceUsage: true, SilenceErrors: true}
		root.AddCommand(approveCommands(resolve, &out)...)
		root.SetArgs(args)
		return root.ExecuteContext(t.Context())
	}
	if err := decide("approve", "01kclitwice02"); err != nil {
		t.Fatal(err)
	}
	if err := decide("deny", "01kclitwice02"); err == nil {
		t.Fatal("deny after approve succeeded, want already-decided")
	}
}
