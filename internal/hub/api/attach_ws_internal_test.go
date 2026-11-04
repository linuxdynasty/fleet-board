package api

import (
	"io"
	"net"
	"testing"
)

// TestWSPeerCodeNeverAbsent: a close with no status code reads as a normal
// 1000 closure. 1005 is reserved and must never go back on the wire.
func TestWSPeerCodeNeverAbsent(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		payload []byte
		want    int
	}{
		{"absent", nil, 1000},
		{"short", []byte{0x03}, 1000},
		{"normal", []byte{0x03, 0xe8}, 1000},
		{"going away", []byte{0x03, 0xe9}, 1001},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := wsPeerCode(tc.payload); got != tc.want {
				t.Fatalf("wsPeerCode(%x) = %d, want %d", tc.payload, got, tc.want)
			}
		})
	}
}

// TestCloseSocketNeverEmits1005: even a 1005 close lands on the wire as a
// normal 1000 closure; 1005 is reserved and must never be sent.
func TestCloseSocketNeverEmits1005(t *testing.T) {
	t.Parallel()
	a, b := net.Pipe()
	defer func() { _ = a.Close() }()
	defer func() { _ = b.Close() }()
	c := newWSConn(a)
	type res struct {
		code int
		err  error
	}
	ch := make(chan res, 1)
	go func() {
		hdr := make([]byte, 2)
		if _, err := io.ReadFull(b, hdr); err != nil {
			ch <- res{err: err}
			return
		}
		n := int(hdr[1] & 0x7F)
		payload := make([]byte, n)
		if _, err := io.ReadFull(b, payload); err != nil {
			ch <- res{err: err}
			return
		}
		code := 1005
		if len(payload) >= 2 {
			code = int(payload[0])<<8 | int(payload[1])
		}
		ch <- res{code: code}
	}()
	c.closeSocket(1005)
	r := <-ch
	if r.err != nil {
		t.Fatalf("read close frame: %v", r.err)
	}
	if r.code == 1005 {
		t.Fatal("closeSocket(1005) put 1005 on the wire, want a real code")
	}
	if r.code != 1000 {
		t.Fatalf("closeSocket(1005) put %d on the wire, want 1000", r.code)
	}
}
