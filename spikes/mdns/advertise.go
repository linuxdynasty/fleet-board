package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"sync/atomic"
	"time"

	"github.com/hashicorp/mdns"
	"github.com/miekg/dns"
)

// countingZone wraps the service zone to log every question it answers, so the
// advertiser shows whether queries from dns-sd or a browser reach it.
type countingZone struct {
	inner     mdns.Zone
	lg        *logger
	questions atomic.Int64
	answered  atomic.Int64
}

func (z *countingZone) Records(q dns.Question) []dns.RR {
	rrs := z.inner.Records(q)
	z.questions.Add(1)
	if len(rrs) > 0 {
		z.answered.Add(1)
		z.lg.emit("answered", map[string]any{
			"qname":       q.Name,
			"qtype":       dns.TypeToString[q.Qtype],
			"unicast_bit": q.Qclass&(1<<15) != 0,
			"records":     len(rrs),
		})
	}
	return rrs
}

// bindCheck opens the same multicast listeners NewServer opens. NewServer
// discards a per-family error and only fails when both families fail, so this
// is the only place a 5353 bind error next to mDNSResponder is visible.
func bindCheck(lg *logger, ifi *net.Interface) {
	fams := []struct {
		network string
		addr    *net.UDPAddr
	}{
		{"udp4", &net.UDPAddr{IP: net.ParseIP("224.0.0.251"), Port: 5353}},
		{"udp6", &net.UDPAddr{IP: net.ParseIP("ff02::fb"), Port: 5353}},
	}
	for _, f := range fams {
		c, err := net.ListenMulticastUDP(f.network, ifi, f.addr)
		fields := map[string]any{"network": f.network, "group": f.addr.String(), "ok": err == nil}
		if err != nil {
			fields["error"] = err.Error()
		} else {
			fields["local"] = c.LocalAddr().String()
			_ = c.Close()
		}
		lg.emit("bind_check", fields)
	}
}

// groupSourceIP returns the address the OS would send from to the IPv4 mDNS
// group; connecting a UDP socket sends nothing.
func groupSourceIP() (net.IP, error) {
	c, err := net.Dial("udp4", "224.0.0.251:5353")
	if err != nil {
		return nil, fmt.Errorf("source address for the mDNS group: %w", err)
	}
	defer c.Close()
	return c.LocalAddr().(*net.UDPAddr).IP, nil
}

func runAdvertise(ctx context.Context, lg *logger, args []string) error {
	fs := flag.NewFlagSet("advertise", flag.ContinueOnError)
	instance := fs.String("instance", defaultInstance(), "service instance name")
	service := fs.String("service", "_fleetd._tcp", "service type")
	domain := fs.String("domain", "local.", "domain (fully qualified)")
	port := fs.Int("port", 47999, "advertised port (nothing needs to listen on it)")
	ifaceName := fs.String("iface", "", "multicast interface, e.g. en0 (default: system default)")
	host := fs.String("host", "", `host name in SRV/A records (default "<instance>.local."; "os" = the library default, the machine's own name)`)
	ipList := fs.String("ip", "", `comma-separated addresses for A/AAAA (default: -iface addresses, else the source address for the mDNS group; with -host os, the library's lookup)`)
	txt := fs.String("txt", "txtvers=1,role=spike", "comma-separated TXT strings")
	duration := fs.Duration("duration", 0, "stop after this long (0 = until interrupted)")
	statsEvery := fs.Duration("stats", 30*time.Second, "interval between stats lines")
	if err := fs.Parse(args); err != nil {
		return err
	}

	ifi, err := lookupIface(*ifaceName)
	if err != nil {
		return err
	}
	var ips []net.IP
	for _, s := range splitList(*ipList) {
		ip := net.ParseIP(s)
		if ip == nil {
			return fmt.Errorf("bad -ip %q", s)
		}
		ips = append(ips, ip)
	}
	switch {
	case len(ips) > 0:
	case ifi != nil:
		if ips, err = ifaceIPs(ifi); err != nil {
			return err
		}
	case *host != "os":
		ip, err := groupSourceIP()
		if err != nil {
			return err
		}
		ips = []net.IP{ip}
	}
	hostName := *host
	switch hostName {
	case "":
		hostName = *instance + ".local."
	case "os":
		hostName = ""
	}

	start := hostInfo()
	start["instance"], start["service"], start["port"], start["iface"] = *instance, *service, *port, *ifaceName
	lg.emit("start", start)
	bindCheck(lg, ifi)

	svc, err := mdns.NewMDNSService(*instance, *service, *domain, hostName, *port, ips, splitList(*txt))
	if err != nil {
		return fmt.Errorf("new service: %w", err)
	}
	lg.emit("service", map[string]any{
		"instance": svc.Instance, "service": svc.Service, "domain": svc.Domain,
		"host": svc.HostName, "ips": ipStrings(svc.IPs), "port": svc.Port, "txt": svc.TXT,
	})

	zone := &countingZone{inner: svc, lg: lg}
	srv, err := mdns.NewServer(&mdns.Config{Zone: zone, Iface: ifi, Logger: lg.lib()})
	if err != nil {
		return fmt.Errorf("new server: %w", err)
	}
	defer func() { _ = srv.Shutdown() }()
	lg.emit("server_started", nil)

	var deadline <-chan time.Time
	if *duration > 0 {
		deadline = time.After(*duration)
	}
	tick := time.NewTicker(*statsEvery)
	defer tick.Stop()
	stats := func() map[string]any {
		return map[string]any{"questions_total": zone.questions.Load(), "answered_total": zone.answered.Load()}
	}
	for {
		select {
		case <-tick.C:
			lg.emit("stats", stats())
		case <-deadline:
			lg.emit("stopped", stats())
			return nil
		case <-ctx.Done():
			lg.emit("stopped", stats())
			return nil
		}
	}
}
