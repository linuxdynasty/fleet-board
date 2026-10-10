package conn

import (
	"context"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"time"
)

// DefaultBrowseTimeout bounds one mDNS browse (defaults.md "mDNS browse
// timeout", ADR 0028).
const DefaultBrowseTimeout = 2 * time.Second

// fleetdSuffix ends the name of every answer discovery keeps (ADR 0028:
// the library returns unrelated services too).
const (
	fleetdSuffix   = "._fleetd._tcp.local."
	maxServiceName = 255
)

// Service is one mDNS answer. It is untrusted data from the network: only
// a candidate until TLS verifies it against the pinned CA (02g).
type Service struct {
	// Name is the instance name, such as "fleetd-mac._fleetd._tcp.local.".
	Name string
	// Host is the SRV target, such as "mac.local.": the name the hub's
	// certificate must hold.
	Host   string
	Port   int
	AddrV4 netip.Addr
	// AddrV6 carries its zone when it is link-local.
	AddrV6 netip.Addr
}

// Discover finds hubs on the local network (03 A2, ADR 0023 and 0028).
type Discover interface {
	// Browse sends one query and calls found, from one goroutine, for
	// each answer until timeout passes or ctx ends.
	Browse(ctx context.Context, timeout time.Duration, found func(Service)) error
}

// BrowseStatus is the last mDNS browse, kept in [Status].
type BrowseStatus struct {
	Time    time.Time
	Elapsed time.Duration
	// Kept answers became candidates; Dropped ones did not (another
	// service, no valid port, host or unicast address, or past the cap of
	// 16). An answer repeated within the browse is neither.
	Kept, Dropped int
	// ErrKind is set when the browse failed (no usable multicast
	// interface): the 05b err_kind.
	ErrKind string
}

// candidateOf turns an mDNS answer into a candidate: the SRV target and
// port name the hub (and its certificate must hold that name), and the
// answer's address is dialed. Anything else is dropped.
func candidateOf(s Service) (candidate, bool) {
	name := strings.ToLower(s.Name)
	if len(name) > maxServiceName || len(name) <= len(fleetdSuffix) || !strings.HasSuffix(name, fleetdSuffix) {
		return candidate{}, false
	}
	if s.Port < 1 || s.Port > 65535 {
		return candidate{}, false
	}
	host := strings.TrimSuffix(s.Host, ".")
	if host == "" {
		return candidate{}, false
	}
	addr, err := HubAddress(net.JoinHostPort(host, strconv.Itoa(s.Port)))
	if err != nil {
		return candidate{}, false
	}
	ip := s.AddrV4
	if !unicast(ip) {
		ip = s.AddrV6
	}
	if !unicast(ip) {
		return candidate{}, false
	}
	return candidate{addr: addr, dial: netip.AddrPortFrom(ip, uint16(s.Port)).String(), source: "mdns"}, true
}

func unicast(a netip.Addr) bool {
	return a.IsValid() && !a.IsUnspecified() && !a.IsMulticast() && a != netip.AddrFrom4([4]byte{255, 255, 255, 255})
}

// browse is one mDNS browse running on its own goroutine: its kept
// answers arrive on cands as they come, and cands closes when it ends.
type browse struct {
	cands  chan candidate
	done   chan struct{}
	cancel context.CancelFunc
}

// startBrowse starts one browse. report runs on the browse's goroutine
// when it ends, before cands closes, with the time to the first kept
// answer (negative when none).
func startBrowse(ctx context.Context, d Discover, timeout time.Duration,
	report func(st BrowseStatus, first time.Duration),
) *browse {
	if timeout <= 0 {
		timeout = DefaultBrowseTimeout
	}
	bctx, cancel := context.WithCancel(ctx)
	b := &browse{cands: make(chan candidate, maxHubAddresses), done: make(chan struct{}), cancel: cancel}
	go func() {
		defer close(b.done)
		start := time.Now()
		st := BrowseStatus{Time: start}
		first := time.Duration(-1)
		seen := map[string]bool{}
		err := d.Browse(bctx, timeout, func(s Service) {
			c, ok := candidateOf(s)
			switch {
			case !ok:
				st.Dropped++
			case seen[c.key()]:
			case st.Kept == maxHubAddresses:
				st.Dropped++
			default:
				seen[c.key()] = true
				st.Kept++
				if first < 0 {
					first = time.Since(start)
				}
				// Never blocks: at most maxHubAddresses are kept.
				b.cands <- c
			}
		})
		st.Elapsed = time.Since(start)
		if err != nil {
			st.ErrKind = errKind(err)
		}
		report(st, first)
		close(b.cands)
	}()
	return b
}

// stop ends the browse and waits for its goroutine.
func (b *browse) stop() {
	b.cancel()
	for range b.cands {
	}
	<-b.done
}
