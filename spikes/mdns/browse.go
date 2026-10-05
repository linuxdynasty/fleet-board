package main

import (
	"context"
	"flag"
	"net"
	"runtime"
	"strings"
	"time"

	"github.com/hashicorp/mdns"
)

type browseOpts struct {
	service, domain, instance string
	timeout                   time.Duration
	iface                     *net.Interface
	unicast, noV6             bool
}

func msSince(t time.Time) float64 { return float64(time.Since(t).Microseconds()) / 1000 }

// browseOnce runs one QueryContext and records when each entry arrives.
func browseOnce(ctx context.Context, lg *logger, o browseOpts, n int) Run {
	entries := make(chan *mdns.ServiceEntry, 64)
	qp := &mdns.QueryParam{
		Service:             o.service,
		Domain:              o.domain,
		Timeout:             o.timeout,
		Interface:           o.iface,
		Entries:             entries,
		WantUnicastResponse: o.unicast,
		DisableIPv6:         o.noV6,
		Logger:              lg.lib(),
	}
	r := Run{Event: "browse_run", Mode: "browse", Run: n, TimeoutMS: o.timeout.Milliseconds(), Instance: o.instance}
	start := time.Now()
	done := make(chan error, 1)
	go func() { done <- mdns.QueryContext(ctx, qp) }()

	take := func(e *mdns.ServiceEntry) {
		at := msSince(start)
		ent := Entry{AtMS: at, Name: e.Name, Host: e.Host, Port: e.Port, TXT: append([]string(nil), e.InfoFields...)}
		if e.AddrV4 != nil {
			ent.AddrV4 = e.AddrV4.String()
		}
		if e.AddrV6IPAddr != nil {
			ent.AddrV6 = e.AddrV6IPAddr.String()
		}
		r.Entries = append(r.Entries, ent)
		if r.FirstAnyMS == nil {
			r.FirstAnyMS = &at
		}
		if !r.Found && matchInstance(o.instance, e.Name) {
			r.Found = true
			r.FirstMatchMS = &at
		}
	}
	for {
		select {
		case e := <-entries:
			take(e)
			continue
		case err := <-done:
			for drained := false; !drained; {
				select {
				case e := <-entries:
					take(e)
				default:
					drained = true
				}
			}
			if err != nil {
				r.Error = err.Error()
			}
		}
		break
	}
	r.ElapsedMS = msSince(start)
	r.EntryCount = len(r.Entries)
	r.TS = time.Now().UTC().Format(time.RFC3339Nano)
	return r
}

// crossCheck runs `dns-sd -B` (and `-L` for a named instance) so the OS
// responder's view can be compared with the library's. Diagnostic only.
func crossCheck(ctx context.Context, lg *logger, d time.Duration, o browseOpts) {
	if runtime.GOOS != "darwin" {
		lg.emit("dnssd_skipped", map[string]any{"reason": "dns-sd exists on macOS only"})
		return
	}
	svc := strings.Trim(o.service, ".")
	dom := strings.Trim(o.domain, ".") + "."
	lines, err := runDNSSD(ctx, d, "-B", svc, dom)
	f := map[string]any{"args": []string{"-B", svc, dom}, "events": parseBrowse(lines), "raw": capLines(lines)}
	if err != nil {
		f["error"] = err.Error()
	}
	f["instance_seen"] = false
	for _, ev := range parseBrowse(lines) {
		if ev.Action == "Add" && (o.instance == "" || ev.Instance == o.instance) {
			f["instance_seen"] = true
		}
	}
	lg.emit("dnssd_browse", f)
	if o.instance == "" {
		return
	}
	lines, err = runDNSSD(ctx, d, "-L", o.instance, svc, dom)
	f = map[string]any{"args": []string{"-L", o.instance, svc, dom}, "reached": parseLookup(lines), "raw": capLines(lines)}
	if err != nil {
		f["error"] = err.Error()
	}
	lg.emit("dnssd_lookup", f)
}

func runBrowse(ctx context.Context, lg *logger, args []string) error {
	fs := flag.NewFlagSet("browse", flag.ContinueOnError)
	var o browseOpts
	fs.StringVar(&o.service, "service", "_fleetd._tcp", "service type")
	fs.StringVar(&o.domain, "domain", "local", "domain")
	fs.StringVar(&o.instance, "instance", "", `instance that counts as "found" (empty = any entry)`)
	fs.DurationVar(&o.timeout, "timeout", 2*time.Second, "browse timeout (one QueryContext)")
	interval := fs.Duration("interval", 3*time.Second, "start-to-start interval between runs (a run never overlaps the next)")
	count := fs.Int("count", 10, "number of runs (0 = until interrupted)")
	ifaceName := fs.String("iface", "", "multicast interface, e.g. en0 (default: system default)")
	fs.BoolVar(&o.unicast, "unicast", false, "set the unicast-response (QU) bit")
	fs.BoolVar(&o.noV6, "no-v6", false, "query over IPv4 only")
	dnssd := fs.Duration("dnssd", 0, "also run macOS dns-sd -B (and -L for -instance) for this long before and after the loop (0 = off)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	ifi, err := lookupIface(*ifaceName)
	if err != nil {
		return err
	}
	o.iface = ifi

	start := hostInfo()
	start["service"], start["instance"], start["timeout_ms"] = o.service, o.instance, o.timeout.Milliseconds()
	start["interval_ms"], start["count"], start["iface"] = interval.Milliseconds(), *count, *ifaceName
	start["unicast"], start["no_v6"] = o.unicast, o.noV6
	lg.emit("start", start)

	if *dnssd > 0 {
		crossCheck(ctx, lg, *dnssd, o)
	}
	var runs []Run
	for i := 1; (*count == 0 || i <= *count) && ctx.Err() == nil; i++ {
		begin := time.Now()
		r := browseOnce(ctx, lg, o, i)
		lg.write(r)
		runs = append(runs, r)
		select {
		case <-ctx.Done():
		case <-time.After(time.Until(begin.Add(*interval))):
		}
	}
	for _, s := range Summarize(runs) {
		lg.emit("summary", map[string]any{"summary": s})
	}
	if *dnssd > 0 && ctx.Err() == nil {
		crossCheck(ctx, lg, *dnssd, o)
	}
	return nil
}
