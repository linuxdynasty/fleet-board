package main

import (
	"flag"
	"fmt"
	"time"
)

func runFlags(lg *logger, args []string) error {
	fs := flag.NewFlagSet("flags", flag.ContinueOnError)
	bin := fs.String("tmux", "tmux", "tmux binary to test")
	socket := fs.String("L", "fleetprobe-flags", "throwaway server name (must start with fleetprobe-)")
	stall := fs.Duration("stall", 3*time.Second, "how long to stop reading the control client in the pause cycle")
	if err := fs.Parse(args); err != nil {
		return err
	}
	srv, err := newServer(*bin, *socket)
	if err != nil {
		return err
	}
	lg.emit("env", map[string]any{"tmux": srv.version(), "bin": *bin, "socket": *socket})
	if err := srv.start(120, 40, "work"); err != nil {
		return err
	}
	defer func() {
		rc, gone := srv.kill()
		lg.emit("cleanup", map[string]any{"kill_rc": rc, "server_gone": gone})
	}()

	// 1. Flags given at attach time (attach-session -f).
	lg.emit("attach_flags", attachCheck(srv, "ignore-size,pause-after=1"))

	// 2. Flags set later with refresh-client -f (new spelling) and -F (old).
	ctl, _, err := startControl(srv, "attach-session", "-t", "work")
	if err != nil {
		return err
	}
	for _, c := range []string{
		"refresh-client -f ignore-size", "refresh-client -F ignore-size",
		"refresh-client -f pause-after=1", "refresh-client -F pause-after=1",
	} {
		r, _, err := ctl.run(c)
		if err != nil {
			return err
		}
		lg.emit("refresh_flag", map[string]any{"command": c, "reply": r, "client_flags": clientFlags(ctl)})
	}
	ctl.close()

	// 3. Does ignore-size keep the control client from shrinking the window?
	if err := sizeChecks(lg, srv); err != nil {
		return err
	}

	// 4. A real %pause / %continue cycle with pause-after=1.
	return pauseCycle(lg, srv, *stall)
}

func attachCheck(srv *server, flags string) map[string]any {
	rec := map[string]any{"args": "attach-session -t work -f " + flags}
	ctl, r, err := startControl(srv, "attach-session", "-t", "work", "-f", flags)
	if err != nil {
		exited, stderr := ctl.exited(2 * time.Second)
		rec["accepted"], rec["error"], rec["exited"], rec["stderr"] = false, err.Error(), exited, stderr
		if ctl.exitErr != nil {
			rec["exit"] = ctl.exitErr.Error()
		}
		return rec
	}
	rec["accepted"], rec["reply"], rec["client_flags"] = !r.Error, r, clientFlags(ctl)
	ctl.close()
	return rec
}

// clientFlags returns client_flags of every control client (tmux 3.2 added
// the format; older releases print an empty value).
func clientFlags(ctl *control) []string {
	r, _, err := ctl.run("list-clients -F " + quote("#{client_control_mode} #{client_flags}"))
	if err != nil || r.Error {
		return nil
	}
	return r.Body
}

// sizeChecks attaches a 100x30 person client, then a control client that asks
// for 60x20 with window-size smallest, with and without ignore-size.
func sizeChecks(lg *logger, srv *server) error {
	if _, errs, rc := srv.run("set-option", "-g", "window-size", "smallest"); rc != 0 {
		return fmt.Errorf("window-size smallest: rc=%d %s", rc, errs)
	}
	tp, err := startTyper(srv, "work", 100, 30)
	if err != nil {
		return err
	}
	defer tp.close()
	time.Sleep(500 * time.Millisecond)

	cases := []struct {
		name   string
		attach []string
		after  string
	}{
		{"no flag", []string{"attach-session", "-t", "work"}, ""},
		{"refresh-client -f ignore-size", []string{"attach-session", "-t", "work"}, "refresh-client -f ignore-size"},
		{"attach -f ignore-size", []string{"attach-session", "-t", "work", "-f", "ignore-size"}, ""},
	}
	for _, tc := range cases {
		rec := map[string]any{"case": tc.name}
		ctl, r, err := startControl(srv, tc.attach...)
		if err != nil {
			_, stderr := ctl.exited(2 * time.Second)
			rec["attach_error"], rec["stderr"] = err.Error(), stderr
			lg.emit("size_check", rec)
			continue
		}
		rec["attach_reply"] = r
		rec["size_reply"] = setControlSize(ctl, 60, 20)
		if tc.after != "" {
			fr, _, _ := ctl.run(tc.after)
			rec["flag_reply"] = fr
		}
		time.Sleep(500 * time.Millisecond)
		rec["window_size"], rec["client_flags"] = windowSize(ctl), clientFlags(ctl)
		if tc.after != "" {
			// A flag change alone may not trigger a size recalculation;
			// sending the client size again does.
			rec["size_reply_again"] = setControlSize(ctl, 60, 20)
			time.Sleep(500 * time.Millisecond)
			rec["window_size_after_resize"] = windowSize(ctl)
		}
		ctl.close()
		time.Sleep(300 * time.Millisecond)
		lg.emit("size_check", rec)
	}
	return nil
}

func windowSize(ctl *control) string {
	r, _, err := ctl.run("display-message -p -t work: " + quote("#{window_width}x#{window_height}"))
	if err != nil || r.Error || len(r.Body) == 0 {
		return ""
	}
	return r.Body[0]
}

// setControlSize tries the 3.2 "WxH" form, then the older "W,H" form.
func setControlSize(ctl *control, w, h int) []*Reply {
	var out []*Reply
	for _, arg := range []string{fmt.Sprintf("%dx%d", w, h), fmt.Sprintf("%d,%d", w, h)} {
		r, _, err := ctl.run("refresh-client -C " + arg)
		out = append(out, r)
		if err == nil && !r.Error {
			break
		}
	}
	return out
}

// pauseCycle stops reading the control client for stall while a pane floods
// output, then counts %pause, %extended-output, %output and %continue.
func pauseCycle(lg *logger, srv *server, stall time.Duration) error {
	rec := map[string]any{"stall_ms": stall.Milliseconds()}
	defer func() { lg.emit("pause_cycle", rec) }()
	ctl, _, err := startControl(srv, "attach-session", "-t", "work")
	if err != nil {
		return err
	}
	defer ctl.close()
	r, _, err := ctl.run("refresh-client -f pause-after=1")
	if err != nil {
		return err
	}
	rec["pause_after_reply"] = r
	if r.Error {
		rec["skipped"] = "refresh-client -f pause-after=1 was refused"
		return nil
	}
	nw, _, err := ctl.run("new-window -d -n flood -P -F " + quote("#{pane_id}") + " " + quote("yes fleetprobe-flood"))
	if err != nil || nw.Error || len(nw.Body) == 0 {
		return fmt.Errorf("flood window: %v %+v", err, nw)
	}
	pane := nw.Body[0]
	rec["flood_pane"] = pane

	ctl.paused.Store(true)
	time.Sleep(stall)
	ctl.paused.Store(false)
	resumed := time.Now()
	rec["after_stall"] = countKinds(ctl, pane, resumed, 3*time.Second)

	if err := ctl.send("refresh-client -A " + quote(pane+":continue")); err != nil {
		return err
	}
	rec["after_continue"] = countKinds(ctl, pane, time.Now(), 2*time.Second)
	_, _, rc := srv.run("kill-window", "-t", "work:flood")
	rec["kill_flood_rc"] = rc
	return nil
}

// countKinds counts notifications for the pane without keeping output.
func countKinds(ctl *control, pane string, from time.Time, d time.Duration) map[string]any {
	counts := map[string]int{}
	first := map[string]float64{}
	var replies []*Reply
	deadline := time.After(time.Until(from.Add(d)))
	for {
		select {
		case ev, ok := <-ctl.events:
			if !ok {
				return map[string]any{"counts": counts, "first_ms": first, "replies": replies, "exited": true}
			}
			if ev.Reply != nil {
				replies = append(replies, ev.Reply)
				continue
			}
			k := ev.Line.Kind
			if ev.Line.Pane != "" && ev.Line.Pane != pane {
				k += ":other-pane"
			}
			counts[k]++
			if _, ok := first[k]; !ok {
				first[k] = float64(ev.T.Sub(from).Microseconds()) / 1000
			}
		case <-deadline:
			return map[string]any{"counts": counts, "first_ms": first, "replies": replies}
		}
	}
}
