// Command mdns is the throwaway probe for v1 prep item 0.7 (#52): can
// github.com/hashicorp/mdns advertise _fleetd._tcp from a macOS machine that
// also runs mDNSResponder, and how long does a browse take to find it.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
)

const usage = `usage:
  mdns advertise [flags]   advertise _fleetd._tcp until stopped (or -duration)
  mdns browse [flags]      query in a loop, one JSON line per run
  mdns summarize [-table] [file ...]   summarize browse_run lines
Run "mdns <mode> -h" for the flags of a mode.`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	mode, args := os.Args[1], os.Args[2:]
	lg := newLogger(os.Stdout, mode)
	var err error
	switch mode {
	case "advertise":
		err = runAdvertise(ctx, lg, args)
	case "browse":
		err = runBrowse(ctx, lg, args)
	case "summarize":
		err = runSummarize(args)
	default:
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	if err != nil {
		lg.emit("fatal", map[string]any{"error": err.Error()})
		stop()
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
	l.write(rec)
}

func (l *logger) write(v any) {
	b, err := json.Marshal(v)
	if err != nil {
		b, _ = json.Marshal(map[string]string{"event": "log_error", "error": err.Error()})
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	_, _ = l.w.Write(append(b, '\n'))
}

// libWriter turns the library's log.Logger output into lib_log events, so its
// "[ERR] mdns: ..." lines are kept verbatim.
type libWriter struct{ l *logger }

func (w libWriter) Write(p []byte) (int, error) {
	w.l.emit("lib_log", map[string]any{"msg": strings.TrimSpace(string(p))})
	return len(p), nil
}

func (l *logger) lib() *log.Logger { return log.New(libWriter{l}, "", 0) }

func hostInfo() map[string]any {
	h, _ := os.Hostname()
	return map[string]any{"hostname": h, "goos": runtime.GOOS, "goarch": runtime.GOARCH, "go": runtime.Version(), "pid": os.Getpid()}
}

func lookupIface(name string) (*net.Interface, error) {
	if name == "" {
		return nil, nil
	}
	ifi, err := net.InterfaceByName(name)
	if err != nil {
		return nil, fmt.Errorf("interface %q: %w", name, err)
	}
	return ifi, nil
}

// ifaceIPs returns the IPv4 and non-link-local IPv6 addresses of an interface.
func ifaceIPs(ifi *net.Interface) ([]net.IP, error) {
	addrs, err := ifi.Addrs()
	if err != nil {
		return nil, fmt.Errorf("addresses of %s: %w", ifi.Name, err)
	}
	var ips []net.IP
	for _, a := range addrs {
		ipn, ok := a.(*net.IPNet)
		if !ok || ipn.IP.IsLinkLocalUnicast() || ipn.IP.IsLoopback() {
			continue
		}
		ips = append(ips, ipn.IP)
	}
	return ips, nil
}

func ipStrings(ips []net.IP) []string {
	out := make([]string, 0, len(ips))
	for _, ip := range ips {
		out = append(out, ip.String())
	}
	return out
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func defaultInstance() string {
	h, _ := os.Hostname()
	h, _, _ = strings.Cut(h, ".")
	return "fleet-spike-" + strings.ToLower(h)
}
