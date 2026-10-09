package main

import (
	"encoding/binary"
	"reflect"
	"testing"
)

// darwinRtMsg builds an rt_msghdr (92 bytes) followed by a sockaddr_in dst.
func darwinRtMsg(typ byte, index uint16, dst [4]byte) []byte {
	b := make([]byte, 92+16)
	binary.LittleEndian.PutUint16(b[0:], uint16(len(b)))
	b[2], b[3] = 5, typ
	binary.LittleEndian.PutUint16(b[4:], index)
	binary.LittleEndian.PutUint32(b[8:], 0x2)  // RTF_GATEWAY
	binary.LittleEndian.PutUint32(b[12:], 0x7) // RTA_DST|RTA_GATEWAY|RTA_NETMASK
	b[92], b[93] = 16, 2                       // sa_len, AF_INET
	copy(b[96:], dst[:])
	return b
}

// darwinIfMsg builds an if_msghdr or ifa_msghdr head (index at offset 12).
func darwinIfMsg(typ byte, flags uint32, index uint16) []byte {
	b := make([]byte, 20)
	binary.LittleEndian.PutUint16(b[0:], uint16(len(b)))
	b[2], b[3] = 5, typ
	binary.LittleEndian.PutUint32(b[8:], flags)
	binary.LittleEndian.PutUint16(b[12:], index)
	return b
}

func TestParseDarwinRouteMsgs(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		buf  []byte
		want []PushEvent
	}{
		{
			name: "link down on index 4",
			buf:  darwinIfMsg(0xe, 0x8862, 4),
			want: []PushEvent{{Type: "RTM_IFINFO", Index: 4, Relevant: true, Detail: "down"}},
		},
		{
			name: "address added",
			buf:  darwinIfMsg(0xc, 0, 6),
			want: []PushEvent{{Type: "RTM_NEWADDR", Index: 6, Relevant: true}},
		},
		{
			name: "default route deleted is relevant",
			buf:  darwinRtMsg(0x2, 6, [4]byte{}),
			want: []PushEvent{{Type: "RTM_DELETE", Index: 6, Relevant: true, Detail: "default"}},
		},
		{
			name: "host route added by ARP is not relevant",
			buf:  darwinRtMsg(0x1, 6, [4]byte{192, 168, 1, 9}),
			want: []PushEvent{{Type: "RTM_ADD", Index: 6}},
		},
		{
			name: "two messages in one read",
			buf:  append(darwinIfMsg(0xd, 0, 6), darwinIfMsg(0xe, 0x8863, 6)...),
			want: []PushEvent{
				{Type: "RTM_DELADDR", Index: 6, Relevant: true},
				{Type: "RTM_IFINFO", Index: 6, Relevant: true, Detail: "up"},
			},
		},
		{
			name: "truncated message stops parsing",
			buf:  []byte{200, 0, 5, 0xe},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := parseDarwinRouteMsgs(tc.buf); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

// nlMsg builds a netlink message: nlmsghdr (16 bytes) plus body, padded to 4.
func nlMsg(typ uint16, body []byte) []byte {
	n := 16 + len(body)
	b := make([]byte, (n+3)&^3)
	binary.LittleEndian.PutUint32(b[0:], uint32(n))
	binary.LittleEndian.PutUint16(b[4:], typ)
	copy(b[16:], body)
	return b
}

func ifinfomsg(index int32, flags uint32) []byte {
	b := make([]byte, 16)
	binary.LittleEndian.PutUint32(b[4:], uint32(index))
	binary.LittleEndian.PutUint32(b[8:], flags)
	return b
}

func ifaddrmsg(index int32) []byte {
	b := make([]byte, 8)
	b[0], b[1] = 2, 32
	binary.LittleEndian.PutUint32(b[4:], uint32(index))
	return b
}

func rtmsg(dstLen, table byte) []byte {
	b := make([]byte, 12)
	b[0], b[1], b[4] = 2, dstLen, table
	return b
}

func TestParseNetlinkMsgs(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		buf  []byte
		want []PushEvent
	}{
		{
			name: "link up",
			buf:  nlMsg(16, ifinfomsg(2, 0x11043)),
			want: []PushEvent{{Type: "RTM_NEWLINK", Index: 2, Relevant: true, Detail: "up"}},
		},
		{
			name: "address removed",
			buf:  nlMsg(21, ifaddrmsg(1)),
			want: []PushEvent{{Type: "RTM_DELADDR", Index: 1, Relevant: true}},
		},
		{
			name: "default route in main table",
			buf:  nlMsg(24, rtmsg(0, 254)),
			want: []PushEvent{{Type: "RTM_NEWROUTE", Relevant: true, Detail: "default"}},
		},
		{
			name: "local-table host route is not relevant",
			buf:  nlMsg(24, rtmsg(32, 255)),
			want: []PushEvent{{Type: "RTM_NEWROUTE"}},
		},
		{
			name: "two messages in one datagram",
			buf:  append(nlMsg(20, ifaddrmsg(3)), nlMsg(17, ifinfomsg(3, 0))...),
			want: []PushEvent{
				{Type: "RTM_NEWADDR", Index: 3, Relevant: true},
				{Type: "RTM_DELLINK", Index: 3, Relevant: true, Detail: "down"},
			},
		},
		{
			name: "truncated header",
			buf:  []byte{1, 2, 3},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := parseNetlinkMsgs(tc.buf); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}
