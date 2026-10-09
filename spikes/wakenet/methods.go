package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"sync"
	"time"
)

// recorder writes one JSON line per event and, while a trigger is active,
// one "detected" line per method with its latency from the trigger.
type recorder struct {
	log *slog.Logger

	mu       sync.Mutex
	counts   map[string]int
	relevant map[string]int
	trig     *activeTrigger
}

type activeTrigger struct {
	name       string
	start, end time.Time
	seen       map[string]bool
}

func newRecorder(log *slog.Logger) *recorder {
	return &recorder{log: log, counts: map[string]int{}, relevant: map[string]int{}}
}

// event logs one observation. Relevant events also carry since_wake_ms when the
// kernel records a wake in the last ten minutes, and count toward trigger latency.
func (r *recorder) event(method, event string, relevant, logIt bool, attrs ...any) {
	now := time.Now()
	r.mu.Lock()
	r.counts[method]++
	var detected []any
	if relevant {
		r.relevant[method]++
		if t := r.trig; t != nil && !t.seen[method] {
			t.seen[method] = true
			detected = []any{
				"method", method, "trigger", t.name, "first_event", event,
				"latency_from_start_ms", ms(now.Sub(t.start)),
			}
			if !t.end.IsZero() {
				detected = append(detected, "latency_from_end_ms", ms(now.Sub(t.end)))
			}
		}
	}
	r.mu.Unlock()
	if relevant {
		if w, ok := kernelWakeTime(); ok && now.Sub(w) < 10*time.Minute {
			attrs = append(attrs, "since_wake_ms", ms(now.Sub(w)))
		}
	}
	if logIt || relevant {
		r.log.Info(event, append([]any{"method", method, "relevant", relevant}, attrs...)...)
	}
	if detected != nil {
		r.log.Info("detected", detected...)
	}
}

func (r *recorder) triggerStart(name string, at time.Time) {
	r.mu.Lock()
	r.trig = &activeTrigger{name: name, start: at, seen: map[string]bool{}}
	r.mu.Unlock()
}

func (r *recorder) triggerEnd(at time.Time) {
	r.mu.Lock()
	if r.trig != nil {
		r.trig.end = at
	}
	r.mu.Unlock()
}

func ms(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }

// runClock is method (a): wall clock versus Go's monotonic clock each tick.
func runClock(ctx context.Context, r *recorder, interval, threshold time.Duration) {
	start := time.Now()
	d := JumpDetector{Threshold: threshold}
	d.Observe(start.Round(0), 0)
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		now := time.Now()
		j, jumped := d.Observe(now.Round(0), now.Sub(start))
		if !jumped {
			continue
		}
		event := "wake_suspected"
		if j.Gap < 0 {
			event = "wall_step_back"
		}
		attrs := []any{"gap_ms", ms(j.Gap), "wall_delta_ms", ms(j.WallDelta), "mono_delta_ms", ms(j.MonoDelta)}
		if w, ok := kernelWakeTime(); ok {
			attrs = append(attrs, "kernel_wake", w, "latency_ms", ms(now.Sub(w)))
		}
		r.event("clock", event, true, true, attrs...)
	}
}

// runSleepMark is method (s): poll the kernel's own sleep record.
func runSleepMark(ctx context.Context, r *recorder, interval time.Duration) {
	d := SleepMarkDetector{Tolerance: 500 * time.Millisecond}
	m, err := readSleepMark()
	if err != nil {
		r.event("sleepmark", "error", false, true, "err", err.Error())
		return
	}
	d.Observe(m)
	r.event("sleepmark", "baseline", false, true, "wake_time", m.WakeTime, "suspended_ms", ms(m.Suspended))
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		m, err := readSleepMark()
		if err != nil {
			r.event("sleepmark", "error", false, true, "err", err.Error())
			continue
		}
		w, woke := d.Observe(m)
		if !woke {
			continue
		}
		attrs := []any{"slept_ms", ms(w.Slept)}
		if !w.At.IsZero() {
			attrs = append(attrs, "kernel_wake", w.At, "latency_ms", ms(time.Since(w.At)))
		}
		r.event("sleepmark", "wake", true, true, attrs...)
	}
}

// runIfPoll is method (b): diff interfaces and addresses, plus the default route.
func runIfPoll(ctx context.Context, r *recorder, interval time.Duration, routeExec bool) {
	prev, err := Capture()
	if err != nil {
		r.event("ifpoll", "error", false, true, "err", err.Error())
		return
	}
	prevRoute, routeOK := readDefaultRoute(routeExec)
	r.event("ifpoll", "baseline", false, true, "interfaces", len(prev),
		"default_iface", prevRoute.Iface, "default_gw", prevRoute.Gateway, "route_checked", routeOK)
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		cur, err := Capture()
		if err != nil {
			r.event("ifpoll", "error", false, true, "err", err.Error())
			continue
		}
		for _, c := range Diff(prev, cur) {
			r.event("ifpoll", "net_change", true, true, "iface", c.Iface, "kind", c.Kind, "detail", c.Detail)
		}
		prev = cur
		if route, ok := readDefaultRoute(routeExec); ok && route != prevRoute {
			r.event("ifpoll", "default_route_change", true, true,
				"from", prevRoute.Iface+" via "+prevRoute.Gateway, "to", route.Iface+" via "+route.Gateway)
			prevRoute = route
		}
	}
}

// runPush is method (c): read the routing socket (macOS) or netlink (Linux).
// extraRelevant names message types that also count as relevant, for
// measuring delivery latency with a trigger that needs no root.
func runPush(ctx context.Context, r *recorder, logAll bool, extraRelevant map[string]bool) {
	f, parse, err := openPush()
	if err != nil {
		r.event("push", "error", false, true, "err", err.Error())
		return
	}
	r.event("push", "listening", false, true, "socket", f.Name())
	go func() {
		<-ctx.Done()
		_ = f.Close()
	}()
	buf := make([]byte, 64<<10)
	for {
		n, err := f.Read(buf)
		if err != nil {
			if !errors.Is(err, os.ErrClosed) && ctx.Err() == nil {
				r.event("push", "error", false, true, "err", err.Error())
			}
			return
		}
		for _, ev := range parse(buf[:n]) {
			r.event("push", ev.Type, ev.Relevant || extraRelevant[ev.Type], logAll, "index", ev.Index, "detail", ev.Detail)
		}
	}
}
