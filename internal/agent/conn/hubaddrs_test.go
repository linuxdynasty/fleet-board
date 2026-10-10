package conn_test

import (
	"slices"
	"testing"
	"time"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
)

// HelloAck.hub_addresses replaces the configured addresses for the rest of
// the run: invalid entries are dropped and at most 16 are kept, in order.
// After the hub goes away the next cycle tries the cached address, then
// exactly those.
func TestHelloAckAddressesAreCappedAt16(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		invalid []string
		valid   int
	}{
		{"twenty valid", nil, 20},
		{"four invalid among twenty", []string{"", "host:port:extra", "http://hub.example", "hub.example:99999"}, 16},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := startConnectHub(t)
			a := enrolled(t, h)
			var list, valid []string
			for i := range tc.valid {
				addr := closedAddr(t)
				valid = append(valid, addr)
				list = append(list, addr)
				if i < len(tc.invalid) {
					list = append(list, tc.invalid[i])
				}
			}
			h.set(func(h *connectHub) {
				h.tamperAck = func(_ int, ack *agentv1.HelloAck) { ack.HubAddresses = list }
			})
			r := startLoop(t, a, nil)
			h.connected(t, 1)
			r.connected(t)
			before := len(a.events(t, "connect.attempt"))
			h.set(func(h *connectHub) { _ = h.srv.Close() })

			// One cycle: the cached address, then the configured ones,
			// until the next cycle starts with the cached one again.
			var cycle []string
			waitFor(t, 20*time.Second, "a full cycle after the hub went away", func() bool {
				cycle = cycle[:0]
				cached := 0
				for _, at := range a.events(t, "connect.attempt")[before:] {
					if at["source"] == "cached" {
						cached++
						continue
					}
					if cached == 1 {
						peer, _ := at["peer"].(string)
						cycle = append(cycle, peer)
					}
				}
				return cached >= 2
			})
			if want := valid[:16]; !slices.Equal(cycle, want) {
				t.Errorf("a cycle tried %d configured addresses %v, want the first 16 valid ones %v", len(cycle), cycle, want)
			}
		})
	}
}
