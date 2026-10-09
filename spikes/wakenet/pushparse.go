package main

import (
	"encoding/binary"
	"fmt"
)

// PushEvent is one routing-socket (macOS) or netlink (Linux) message, method
// (c). Relevant marks the kinds a reconnect would act on: link state, address
// add or remove, and default-route changes.
type PushEvent struct {
	Type     string
	Index    int
	Relevant bool
	Detail   string // "up"/"down" for link messages, "default" for default routes
}

// The parsers live in one untagged file so their tests run on any OS; the
// numbers are the kernel ABI values (checked against x/sys/unix in the
// per-OS constant tests).

const flagsUpRunning = 0x41 // IFF_UP|IFF_RUNNING, same bits on darwin and Linux

func linkDetail(flags uint32) string {
	if flags&flagsUpRunning == flagsUpRunning {
		return "up"
	}
	return "down"
}

var darwinRTMNames = map[byte]string{
	0x1: "RTM_ADD", 0x2: "RTM_DELETE", 0x3: "RTM_CHANGE", 0x4: "RTM_GET",
	0x5: "RTM_LOSING", 0x6: "RTM_REDIRECT", 0x7: "RTM_MISS", 0x8: "RTM_LOCK",
	0xb: "RTM_RESOLVE", 0xc: "RTM_NEWADDR", 0xd: "RTM_DELADDR", 0xe: "RTM_IFINFO",
	0xf: "RTM_NEWMADDR", 0x10: "RTM_DELMADDR", 0x12: "RTM_IFINFO2",
	0x13: "RTM_NEWMADDR2", 0x14: "RTM_GET2",
}

const darwinSizeofRtMsghdr = 92

// parseDarwinRouteMsgs splits one PF_ROUTE read into messages. Layouts:
// rt_msghdr has the index at offset 4 and rtm_addrs at 12, then sockaddrs at 92;
// if_msghdr and ifa_msghdr have flags at 8 and the index at 12.
func parseDarwinRouteMsgs(b []byte) []PushEvent {
	var out []PushEvent
	for len(b) >= 4 {
		n := int(binary.LittleEndian.Uint16(b[0:]))
		if n < 4 || n > len(b) {
			break
		}
		m := b[:n]
		b = b[n:]
		typ := m[3]
		name, ok := darwinRTMNames[typ]
		if !ok {
			name = fmt.Sprintf("RTM_0x%x", typ)
		}
		ev := PushEvent{Type: name}
		switch typ {
		case 0xe, 0xc, 0xd, 0x12: // IFINFO, NEWADDR, DELADDR, IFINFO2
			if n >= 14 {
				ev.Index = int(binary.LittleEndian.Uint16(m[12:]))
			}
			ev.Relevant = true
			if (typ == 0xe || typ == 0x12) && n >= 12 {
				ev.Detail = linkDetail(binary.LittleEndian.Uint32(m[8:]))
			}
		case 0x1, 0x2, 0x3: // ADD, DELETE, CHANGE
			if n >= 6 {
				ev.Index = int(binary.LittleEndian.Uint16(m[4:]))
			}
			if darwinDstIsDefault(m) {
				ev.Relevant, ev.Detail = true, "default"
			}
		case 0xf, 0x10, 0x13: // multicast address changes
			if n >= 14 {
				ev.Index = int(binary.LittleEndian.Uint16(m[12:]))
			}
		}
		out = append(out, ev)
	}
	return out
}

// darwinDstIsDefault reports whether a route message's destination is 0.0.0.0
// or ::, which on macOS means the default route.
func darwinDstIsDefault(m []byte) bool {
	if len(m) < darwinSizeofRtMsghdr+2 || binary.LittleEndian.Uint32(m[12:])&0x1 == 0 {
		return false
	}
	sa := m[darwinSizeofRtMsghdr:]
	var addr []byte
	switch sa[1] {
	case 2: // AF_INET: len, family, port(2), addr(4)
		if len(sa) >= 8 {
			addr = sa[4:8]
		}
	case 30: // AF_INET6: len, family, port(2), flowinfo(4), addr(16)
		if len(sa) >= 24 {
			addr = sa[8:24]
		}
	}
	if addr == nil {
		return false
	}
	for _, x := range addr {
		if x != 0 {
			return false
		}
	}
	return true
}

var netlinkRTMNames = map[uint16]string{
	16: "RTM_NEWLINK", 17: "RTM_DELLINK", 20: "RTM_NEWADDR", 21: "RTM_DELADDR",
	24: "RTM_NEWROUTE", 25: "RTM_DELROUTE", 28: "RTM_NEWNEIGH", 29: "RTM_DELNEIGH",
}

const nlmsgHdrLen = 16

// parseNetlinkMsgs splits one NETLINK_ROUTE datagram into messages. Bodies:
// ifinfomsg (index at 4, flags at 8), ifaddrmsg (index at 4), rtmsg (dst_len
// at 1, table at 4; dst_len 0 in table 254 is the main default route).
func parseNetlinkMsgs(b []byte) []PushEvent {
	var out []PushEvent
	for len(b) >= nlmsgHdrLen {
		n := int(binary.LittleEndian.Uint32(b[0:]))
		if n < nlmsgHdrLen || n > len(b) {
			break
		}
		typ := binary.LittleEndian.Uint16(b[4:])
		body := b[nlmsgHdrLen:n]
		if adv := (n + 3) &^ 3; adv <= len(b) {
			b = b[adv:]
		} else {
			b = nil
		}
		name, ok := netlinkRTMNames[typ]
		if !ok {
			name = fmt.Sprintf("NLMSG_%d", typ)
		}
		ev := PushEvent{Type: name}
		switch typ {
		case 16, 17:
			if len(body) >= 12 {
				ev.Index = int(int32(binary.LittleEndian.Uint32(body[4:])))
				ev.Detail = linkDetail(binary.LittleEndian.Uint32(body[8:]))
			}
			ev.Relevant = true
		case 20, 21:
			if len(body) >= 8 {
				ev.Index = int(int32(binary.LittleEndian.Uint32(body[4:])))
			}
			ev.Relevant = true
		case 24, 25:
			if len(body) >= 5 && body[1] == 0 && body[4] == 254 {
				ev.Relevant, ev.Detail = true, "default"
			}
		}
		out = append(out, ev)
	}
	return out
}
