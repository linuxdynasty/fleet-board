// Command wakenet is spike 0.6: compare ways to detect machine wake and
// network change on macOS and Linux without cgo. It logs one JSON line per
// event to stdout. Throwaway code; not part of the product.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
)

func main() {
	methods := flag.String("methods", "clock,sleepmark,ifpoll,push", "comma list of clock, sleepmark, ifpoll, push; or none (runtime baseline)")
	interval := flag.Duration("interval", 5*time.Second, "poll interval for clock, sleepmark and ifpoll")
	duration := flag.Duration("duration", 0, "stop after this long (0 = until Ctrl-C)")
	threshold := flag.Duration("jump-threshold", 2*time.Second, "clock: wall minus monotonic gap that counts as a wake")
	routeExec := flag.Bool("route-exec", false, "macOS ifpoll: also exec `route -n get default` each tick (Linux always reads /proc/net/route)")
	pushAll := flag.Bool("push-all", false, "push: log every message, not only link, address and default-route ones")
	pushRelevant := flag.String("push-relevant", "", "push: comma list of extra message types to count as relevant (e.g. RTM_GET for a no-root delivery test)")
	trigger := flag.String("trigger", "", "shell command that causes a network change (run by /bin/sh -c)")
	untrigger := flag.String("untrigger", "", "shell command that reverts -trigger")
	triggerAfter := flag.Duration("trigger-after", 10*time.Second, "wait before each -trigger")
	untriggerAfter := flag.Duration("untrigger-after", 10*time.Second, "wait after -trigger before -untrigger")
	repeat := flag.Int("repeat", 1, "trigger/untrigger cycles")
	flag.Parse()

	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			switch {
			case len(groups) == 0 && a.Key == slog.LevelKey:
				return slog.Attr{}
			case len(groups) == 0 && a.Key == slog.MessageKey:
				a.Key = "event"
			}
			return a
		},
	}))
	host, _ := os.Hostname()
	log.Info("run_start", "host", host, "goos", runtime.GOOS, "goarch", runtime.GOARCH, "go", runtime.Version(),
		"methods", *methods, "interval", interval.String(), "duration", duration.String(),
		"jump_threshold", threshold.String(), "route_exec", *routeExec)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if *duration > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, *duration)
		defer cancel()
	}

	r := newRecorder(log)
	var wg sync.WaitGroup
	for _, m := range strings.Split(*methods, ",") {
		var run func()
		switch strings.TrimSpace(m) {
		case "clock":
			run = func() { runClock(ctx, r, *interval, *threshold) }
		case "sleepmark":
			run = func() { runSleepMark(ctx, r, *interval) }
		case "ifpoll":
			run = func() { runIfPoll(ctx, r, *interval, *routeExec) }
		case "push":
			extra := map[string]bool{}
			for _, t := range strings.Split(*pushRelevant, ",") {
				if t = strings.TrimSpace(t); t != "" {
					extra[t] = true
				}
			}
			run = func() { runPush(ctx, r, *pushAll, extra) }
		case "none", "":
			continue
		default:
			fmt.Fprintf(os.Stderr, "unknown method %q\n", m)
			os.Exit(2)
		}
		wg.Go(run)
	}
	if *trigger != "" {
		wg.Go(func() { runTriggers(ctx, r, log, *trigger, *untrigger, *triggerAfter, *untriggerAfter, *repeat) })
	}
	<-ctx.Done()
	wg.Wait()
	summary(log, r)
}

// runTriggers runs the owner-supplied change and revert commands and marks
// their start and end so each method's first event gets a latency.
func runTriggers(ctx context.Context, r *recorder, log *slog.Logger, trig, untrig string, after, untrigAfter time.Duration, repeat int) {
	run := func(name, cmd string) bool {
		start := time.Now()
		r.triggerStart(name, start)
		out, err := exec.CommandContext(ctx, "/bin/sh", "-c", cmd).CombinedOutput()
		end := time.Now()
		r.triggerEnd(end)
		attrs := []any{"name", name, "cmd", cmd, "took_ms", ms(end.Sub(start)), "output", strings.TrimSpace(string(out))}
		if err != nil {
			attrs = append(attrs, "err", err.Error())
		}
		log.Info("trigger_ran", attrs...)
		return err == nil
	}
	for i := 0; i < repeat; i++ {
		if !sleepCtx(ctx, after) || !run(fmt.Sprintf("trigger_%d", i+1), trig) {
			return
		}
		if untrig == "" {
			continue
		}
		if !sleepCtx(ctx, untrigAfter) || !run(fmt.Sprintf("untrigger_%d", i+1), untrig) {
			return
		}
	}
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// summary logs per-method event counts and this process's and its children's
// CPU time and context switches (getrusage; maxrss is bytes on macOS, KiB on Linux).
func summary(log *slog.Logger, r *recorder) {
	var self, kids syscall.Rusage
	_ = syscall.Getrusage(syscall.RUSAGE_SELF, &self)
	_ = syscall.Getrusage(syscall.RUSAGE_CHILDREN, &kids)
	tv := func(t syscall.Timeval) float64 { return float64(t.Sec)*1000 + float64(t.Usec)/1000 }
	r.mu.Lock()
	defer r.mu.Unlock()
	log.Info("summary", "events", r.counts, "relevant", r.relevant,
		"user_ms", tv(self.Utime), "sys_ms", tv(self.Stime), "maxrss", self.Maxrss,
		"vol_ctx_switches", self.Nvcsw, "invol_ctx_switches", self.Nivcsw,
		"children_user_ms", tv(kids.Utime), "children_sys_ms", tv(kids.Stime))
}
