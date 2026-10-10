package conn

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"

	"connectrpc.com/connect"

	"github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1/agentv1connect"
)

// Transport keepalive of the agent's stream (ADR 0004, defaults.md
// "Transport keepalive (agent)").
const (
	DefaultKeepalivePing    = 10 * time.Second
	DefaultKeepaliveTimeout = 20 * time.Second
)

// Keepalive is the HTTP/2 ping on the agent's connection: a ping when
// nothing arrived for Ping, and the connection closed when no answer comes
// within Timeout, so a silent hub is caught within Ping + Timeout. Zero
// fields take the defaults.
type Keepalive struct {
	Ping, Timeout time.Duration
}

func (k Keepalive) withDefaults() Keepalive {
	if k.Ping <= 0 {
		k.Ping = DefaultKeepalivePing
	}
	if k.Timeout <= 0 {
		k.Timeout = DefaultKeepaliveTimeout
	}
	return k
}

// link is one connection to a hub candidate: TLS 1.3 pinned to the
// enrolled CA (the same check as enroll), this machine's client
// certificate, and the keepalive.
type link struct {
	tr     *http.Transport
	client agentv1connect.AgentServiceClient
	// untrusted is set when the peer failed the handshake (the pin, the
	// name, the key proof, an alert or a non-TLS answer).
	untrusted atomic.Bool
	// handshook is set once a handshake passed: the candidate is accepted.
	handshook atomic.Bool

	mu    sync.Mutex
	local netip.Addr
	conns []net.Conn
}

func newLink(addr string, fp [32]byte, cert tls.Certificate, ka Keepalive, hs time.Duration) (*link, error) {
	return newLinkVia(addr, "", fp, cert, ka, hs, nil)
}

// newLinkVia is newLink dialing dial (an mDNS answer's address) when it is
// set; addr still names the hub the certificate must hold. onHandshake,
// when set, hears each handshake's outcome (nil when it passed).
func newLinkVia(addr, dial string, fp [32]byte, cert tls.Certificate, ka Keepalive, hs time.Duration,
	onHandshake func(err error),
) (*link, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("%w: %q", ErrHubAddress, addr)
	}
	cfg := pinnedTLS(fp, host)
	cfg.Certificates = []tls.Certificate{cert}
	k := &link{}
	d := &net.Dialer{Timeout: hs}
	k.tr = &http.Transport{
		ForceAttemptHTTP2:      true,
		MaxResponseHeaderBytes: 64 << 10,
		HTTP2:                  &http.HTTP2Config{SendPingTimeout: ka.Ping, PingTimeout: ka.Timeout},
		DialTLSContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			if dial != "" {
				address = dial
			}
			heard := func(err error) {
				if onHandshake != nil {
					onHandshake(err)
				}
			}
			raw, err := d.DialContext(ctx, network, address)
			if err != nil {
				heard(err)
				return nil, err
			}
			c := tls.Client(raw, cfg)
			hctx, cancel := context.WithTimeout(ctx, hs)
			defer cancel()
			if err := c.HandshakeContext(hctx); err != nil {
				_ = raw.Close()
				if !transportError(err) {
					k.untrusted.Store(true)
				}
				heard(err)
				return nil, err
			}
			heard(nil)
			k.mu.Lock()
			if ap, err := netip.ParseAddrPort(raw.LocalAddr().String()); err == nil {
				k.local = ap.Addr().Unmap()
			}
			k.conns = append(k.conns, c)
			k.mu.Unlock()
			k.handshook.Store(true)
			return c, nil
		},
	}
	k.client = agentv1connect.NewAgentServiceClient(&http.Client{Transport: k.tr}, "https://"+addr,
		connect.WithGRPC(), connect.WithReadMaxBytes(maxMessageBytes), connect.WithSendMaxBytes(maxMessageBytes))
	return k, nil
}

// close closes every connection the link dialed, so none outlives its
// attempt, whatever state the transport left it in.
func (k *link) close() {
	k.tr.CloseIdleConnections()
	k.mu.Lock()
	defer k.mu.Unlock()
	for _, c := range k.conns {
		_ = c.Close()
	}
	k.conns = nil
}

// localAddr is the connection's own address, for the network-change
// decision; zero before the handshake.
func (k *link) localAddr() netip.Addr {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.local
}
