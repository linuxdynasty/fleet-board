// Command tmuxtyping is the throwaway probe for v1 prep item 0.5 (#50). It
// answers two questions against a real tmux on a throwaway server: can a
// control-mode client be told (pushed) when a person types in another
// attached client, and do the control-mode flags pause-after and ignore-size
// work on a given tmux release.
package main

import (
	"encoding/json"
	"io"
	"log"
	"os"
	"sync"
	"time"
)

const usage = `usage:
  tmuxtyping typing [flags]    keystroke-to-notification trials, one JSON line per trial
  tmuxtyping flags [flags]     check attach -f / refresh-client -f|-F flags and a %pause cycle
  tmuxtyping summarize file... markdown tables from typing trial lines
Run "tmuxtyping <mode> -h" for the flags of a mode.`

func main() {
	if len(os.Args) < 2 {
		_, _ = io.WriteString(os.Stderr, usage+"\n")
		os.Exit(2)
	}
	mode, args := os.Args[1], os.Args[2:]
	lg := newLogger(os.Stdout, mode)
	var err error
	switch mode {
	case "typing":
		err = runTyping(lg, args)
	case "flags":
		err = runFlags(lg, args)
	case "summarize":
		err = runSummarize(os.Stdout, args)
	default:
		_, _ = io.WriteString(os.Stderr, usage+"\n")
		os.Exit(2)
	}
	if err != nil {
		lg.emit("fatal", map[string]any{"error": err.Error()})
		os.Exit(1)
	}
}

type logger struct {
	mu   sync.Mutex
	w    io.Writer
	mode string
}

func newLogger(w io.Writer, mode string) *logger { return &logger{w: w, mode: mode} }

// emit writes one JSON line with ts, event and mode set.
func (l *logger) emit(event string, fields map[string]any) {
	rec := map[string]any{
		"ts":    time.Now().UTC().Format(time.RFC3339Nano),
		"event": event,
		"mode":  l.mode,
	}
	for k, v := range fields {
		rec[k] = v
	}
	b, err := json.Marshal(rec)
	if err != nil {
		log.Printf("marshal: %v", err)
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	_, _ = l.w.Write(append(b, '\n'))
}
