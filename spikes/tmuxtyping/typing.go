package main

import (
	"flag"
	"fmt"
	"math/rand/v2"
	"strconv"
	"strings"
	"time"
)

// subscription is one refresh-client -B name:what:format line.
type subscription struct{ name, what, format string }

// subscriptions covers every push source a typing person could move:
// the control client's own session, every session through the S: loop, the
// control client's own client_activity, every window (output activity), and
// every client through the L: loop (only tmux 3.4 and later have L:).
var subscriptions = []subscription{
	{"sess_act", "", "#{session_activity}"},
	{"all_sess_act", "", "#{S:#{session_name}=#{session_activity} }"},
	{"ctl_client_act", "", "#{client_activity}"},
	{"win_act", "@*", "#{window_activity}"},
	{"clients_act", "", "#{L:#{client_pid}=#{client_activity} }"},
}

var trialKinds = []string{"type-work", "type-other", "agent-sendkeys", "idle"}

func runTyping(lg *logger, args []string) error {
	fs := flag.NewFlagSet("typing", flag.ContinueOnError)
	bin := fs.String("tmux", "tmux", "tmux binary to test")
	socket := fs.String("L", "fleetprobe-typing", "throwaway server name (must start with fleetprobe-)")
	per := fs.Int("n", 20, "trials per kind")
	window := fs.Duration("window", 2500*time.Millisecond, "how long to watch after each action")
	if err := fs.Parse(args); err != nil {
		return err
	}
	srv, err := newServer(*bin, *socket)
	if err != nil {
		return err
	}
	ver := srv.version()
	lg.emit("env", map[string]any{"tmux": ver, "bin": *bin, "socket": *socket, "n": *per, "window_ms": window.Milliseconds()})
	if err := srv.start(120, 40, "work", "other"); err != nil {
		return err
	}
	defer func() {
		rc, gone := srv.kill()
		lg.emit("cleanup", map[string]any{"kill_rc": rc, "server_gone": gone})
	}()

	ctl, att, err := startControl(srv, "attach-session", "-t", "work")
	if err != nil {
		return fmt.Errorf("control attach: %w", err)
	}
	defer ctl.close()
	lg.emit("control_attached", map[string]any{"reply": att})
	for _, s := range subscriptions {
		r, _, err := ctl.run("refresh-client -B " + quote(s.name+":"+s.what+":"+s.format))
		if err != nil {
			return err
		}
		lg.emit("subscribe", map[string]any{"name": s.name, "what": s.what, "format": s.format, "reply": r})
	}

	work, err := startTyper(srv, "work", 100, 30)
	if err != nil {
		return err
	}
	defer work.close()
	other, err := startTyper(srv, "other", 100, 30)
	if err != nil {
		return err
	}
	defer other.close()

	panes := map[string]string{}
	for _, sess := range []string{"work", "other"} {
		r, _, err := ctl.run("display-message -p -t " + quote(sess+":") + " " + quote("#{pane_id}"))
		if err != nil || r.Error || len(r.Body) == 0 {
			return fmt.Errorf("pane id of %s: %v %+v", sess, err, r)
		}
		panes[sess] = r.Body[0]
	}
	initial := ctl.collect(time.Now().Add(2500 * time.Millisecond))
	lg.emit("setup", map[string]any{
		"panes": panes, "typer_pids": map[string]int{"work": work.pid, "other": other.pid},
		"initial_events": lines(initial), "clients": listClients(ctl),
	})

	for i := 0; i < *per; i++ {
		for _, kind := range trialKinds {
			rec, err := trial(ctl, kind, panes, map[string]*typer{"work": work, "other": other}, *window)
			if err != nil {
				return fmt.Errorf("trial %s %d: %w", kind, i, err)
			}
			rec["i"], rec["tmux"] = i, ver
			lg.emit("trial", rec)
		}
	}
	return nil
}

// trial reads the target screen, waits a random part of a second (so the
// keystroke lands at a random point of tmux's 1 s subscription timer), acts,
// and records the first arrival of every signal within the window.
func trial(ctl *control, kind string, panes map[string]string, typers map[string]*typer, window time.Duration) (map[string]any, error) {
	sess := "work"
	if kind == "type-other" {
		sess = "other"
	}
	screen, err := readScreen(ctl, panes[sess])
	if err != nil {
		return nil, err
	}
	time.Sleep(time.Duration(rand.IntN(1000)) * time.Millisecond)

	var t0 time.Time
	switch kind {
	case "type-work", "type-other":
		t0, err = typers[sess].key("a")
	case "agent-sendkeys":
		t0 = time.Now()
		err = ctl.send("send-keys -t " + quote(panes[sess]) + " -l b")
	case "idle":
		t0 = time.Now()
	}
	if err != nil {
		return nil, err
	}
	evs := ctl.collect(t0.Add(window))
	subLat, subVal, outLat := map[string]float64{}, map[string]string{}, map[string]float64{}
	var others, otherValues []string
	for _, ev := range evs {
		ms := float64(ev.T.Sub(t0).Microseconds()) / 1000
		switch {
		case ev.Reply != nil:
		case ev.Line.Kind == "subscription-changed":
			if _, ok := subLat[ev.Line.Name]; !ok {
				subLat[ev.Line.Name], subVal[ev.Line.Name] = ms, ev.Line.Value
			}
		case ev.Line.Kind == "output" || ev.Line.Kind == "extended-output":
			if _, ok := outLat[ev.Line.Pane]; !ok {
				outLat[ev.Line.Pane] = ms
			}
		default:
			others = append(others, ev.Line.Kind)
			otherValues = append(otherValues, fmt.Sprintf("+%.0fms %%%s %s", ms, ev.Line.Kind, ev.Line.Value))
		}
	}
	clients := listClients(ctl)
	rec := map[string]any{
		"kind": kind, "session": sess, "pane": panes[sess], "t0": t0.UTC().Format(time.RFC3339Nano),
		"t0_frac_ms": t0.Nanosecond() / 1e6, "screen": screen,
		"sub_latency_ms": subLat, "sub_value": subVal, "output_latency_ms": outLat,
		"other_notifications": others, "other_values": otherValues, "clients": clients,
	}
	if tp, ok := typers[sess]; ok {
		act := clients[strconv.Itoa(tp.pid)]
		rec["poll_client_activity"] = act
		rec["poll_saw_input"] = act >= t0.Unix()
	}
	return rec, nil
}

// readScreen is the "read every screen before any key" step: it refuses to
// act unless the pane still runs cat, and returns a short summary.
func readScreen(ctl *control, pane string) (map[string]any, error) {
	r, _, err := ctl.run("display-message -p -t " + quote(pane) + " " + quote("#{pane_current_command}"))
	if err != nil {
		return nil, err
	}
	if r.Error || len(r.Body) == 0 || r.Body[0] != "cat" {
		return nil, fmt.Errorf("pane %s is not running cat: %+v", pane, r)
	}
	c, _, err := ctl.run("capture-pane -p -t " + quote(pane))
	if err != nil {
		return nil, err
	}
	last, nonblank := "", 0
	for _, l := range c.Body {
		if strings.TrimSpace(l) != "" {
			nonblank++
			last = strings.TrimSpace(l)
		}
	}
	if len(last) > 40 {
		last = last[len(last)-40:]
	}
	return map[string]any{"command": "cat", "nonblank_lines": nonblank, "last_line_tail": last}, nil
}

// listClients returns client_pid -> client_activity (seconds) for every
// client, control clients included; this is the 2 s poll's data.
func listClients(ctl *control) map[string]int64 {
	r, _, err := ctl.run("list-clients -F " + quote("#{client_pid} #{client_activity}"))
	out := map[string]int64{}
	if err != nil || r.Error {
		return out
	}
	for _, l := range r.Body {
		pid, act, ok := strings.Cut(l, " ")
		if !ok {
			continue
		}
		if v, err := strconv.ParseInt(act, 10, 64); err == nil {
			out[pid] = v
		}
	}
	return out
}

func lines(evs []event) []Line {
	var out []Line
	for _, ev := range evs {
		if ev.Reply == nil && ev.Line.Kind != "output" {
			out = append(out, ev.Line)
		}
	}
	return out
}
