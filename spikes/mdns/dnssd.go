package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"time"
)

// BrowseEvent is one Add or Rmv line of `dns-sd -B`.
type BrowseEvent struct {
	Action   string `json:"action"`
	Flags    string `json:"flags"`
	IfIndex  string `json:"if"`
	Domain   string `json:"domain"`
	Type     string `json:"type"`
	Instance string `json:"instance"`
}

// parseBrowse reads `dns-sd -B` output: "time A/R flags if domain type name...".
func parseBrowse(lines []string) []BrowseEvent {
	var out []BrowseEvent
	for _, line := range lines {
		f := strings.Fields(line)
		if len(f) < 7 || (f[1] != "Add" && f[1] != "Rmv") {
			continue
		}
		out = append(out, BrowseEvent{
			Action:   f[1],
			Flags:    f[2],
			IfIndex:  f[3],
			Domain:   f[4],
			Type:     f[5],
			Instance: strings.Join(f[6:], " "),
		})
	}
	return out
}

// parseLookup keeps the "<instance> can be reached at host:port" lines of `dns-sd -L`.
func parseLookup(lines []string) []string {
	var out []string
	for _, line := range lines {
		_, rest, ok := strings.Cut(strings.TrimSpace(line), "  ")
		if !ok || !strings.Contains(rest, " can be reached at ") {
			continue
		}
		out = append(out, strings.TrimSpace(rest))
	}
	return out
}

// matchInstance reports whether a wire name such as `my\ box._fleetd._tcp.local.`
// belongs to the instance; an empty instance matches anything.
func matchInstance(instance, name string) bool {
	if instance == "" {
		return true
	}
	unescaped := strings.ReplaceAll(name, `\ `, " ")
	return strings.HasPrefix(unescaped, instance+".")
}

const maxRawLines = 60

// runDNSSD runs dns-sd for d, stops it with SIGINT and returns its output lines.
// dns-sd block-buffers stdout when it is not a terminal and loses that buffer
// when stopped, so it runs under script(1) for a pseudo-terminal.
// It is a diagnostic only: the product never depends on it.
func runDNSSD(ctx context.Context, d time.Duration, args ...string) ([]string, error) {
	cctx, cancel := context.WithTimeout(ctx, d)
	defer cancel()
	cmd := exec.CommandContext(cctx, "script", append([]string{"-q", "/dev/null", "dns-sd"}, args...)...)
	cmd.Cancel = func() error { return cmd.Process.Signal(os.Interrupt) }
	cmd.WaitDelay = 2 * time.Second
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	if errors.Is(cctx.Err(), context.DeadlineExceeded) && ctx.Err() == nil {
		err = nil
	}
	lines := strings.Split(strings.TrimRight(strings.ReplaceAll(out.String(), "\r", ""), "\n"), "\n")
	if len(lines) == 1 && lines[0] == "" {
		lines = nil
	}
	return lines, err
}

func capLines(lines []string) []string {
	if len(lines) > maxRawLines {
		return append(lines[:maxRawLines:maxRawLines], "...truncated")
	}
	return lines
}
