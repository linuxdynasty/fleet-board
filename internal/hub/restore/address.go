package restore

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"
)

// DefaultAgentPort is the port the agent adds to a hub address without one
// (conn.DefaultPort; hub code does not import the agent's packages, so a
// test keeps the two equal).
const DefaultAgentPort = "7443"

// ErrAddress means a --address the hub cannot hand to agents.
var ErrAddress = errors.New("restore: not a hub address (want host or host:port, letters, digits, dots and hyphens, or an IP)")

// Address checks a hub address the owner typed and spells it the way the
// agent keeps one (conn.HubAddress): lower case, no trailing dot, an IP in
// its canonical form, and a port without leading zeros; defaultPort when it
// has none. An IPv6 zone, or more than 261 bytes, is refused. The agent
// drops an entry of HelloAck.hub_addresses it cannot parse, so the hub only
// stores ones it would keep.
func Address(s, defaultPort string) (string, error) {
	if s == "" {
		return "", fmt.Errorf("%w: empty", ErrAddress)
	}
	if len(s) > maxAddressLen {
		return "", fmt.Errorf("%w: longer than %d bytes", ErrAddress, maxAddressLen)
	}
	host, port, err := net.SplitHostPort(s)
	if err != nil {
		if net.ParseIP(s) == nil && strings.Contains(s, ":") {
			return "", fmt.Errorf("%w: %q", ErrAddress, s)
		}
		host, port = s, defaultPort
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return "", fmt.Errorf("%w: %q has no valid port", ErrAddress, s)
	}
	host = strings.ToLower(strings.TrimRight(host, "."))
	if ip, err := netip.ParseAddr(host); err == nil && ip.Zone() == "" {
		host = ip.Unmap().String()
	} else if err == nil || !hostName(host) { // an IPv6 zone can carry any byte and means nothing off-link
		return "", fmt.Errorf("%w: %q", ErrAddress, s)
	}
	return net.JoinHostPort(host, strconv.Itoa(n)), nil
}

// maxAddressLen is the longest host name (253), a colon and a port, in
// brackets: the agent's limit too.
const maxAddressLen = 253 + 2 + 1 + 5

// hostName accepts letters, digits, dots and hyphens, at most 253 bytes.
func hostName(h string) bool {
	if h == "" || len(h) > 253 {
		return false
	}
	for i := range len(h) {
		c := h[i]
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '.' && c != '-' {
			return false
		}
	}
	return true
}

// Host is the host part of an address [Address] spelled, for the server
// certificate's names.
func Host(addr string) (string, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return "", fmt.Errorf("%w: %q", ErrAddress, addr)
	}
	return host, nil
}
