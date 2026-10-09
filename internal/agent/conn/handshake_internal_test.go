package conn

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"os"
	"syscall"
	"testing"
)

// A handshake the peer fails is untrusted; a connection that drops, resets
// or times out during it is a lost connection (03 W1). The shapes are the
// ones crypto/tls and net return; the live cases are in handshake_test.go.
func TestHandshakeFailuresAreSortedByShape(t *testing.T) {
	t.Parallel()
	opErr := func(op string, err error) error {
		return &net.OpError{Op: op, Net: "tcp", Err: os.NewSyscallError(op, err)}
	}
	pinErr := verifyPinned(nil, [32]byte{1}, "127.0.0.1")
	if pinErr == nil {
		t.Fatal("verifyPinned accepted an empty chain")
	}
	cases := []struct {
		name string
		err  error
		lost bool
	}{
		{"host unreachable", opErr("read", syscall.EHOSTUNREACH), true},
		{"network unreachable", opErr("write", syscall.ENETUNREACH), true},
		{"network down", opErr("read", syscall.ENETDOWN), true},
		{"connection aborted", opErr("read", syscall.ECONNABORTED), true},
		{"connection reset", opErr("read", syscall.ECONNRESET), true},
		{"broken pipe", opErr("write", syscall.EPIPE), true},
		{"closed connection", &net.OpError{Op: "read", Net: "tcp", Err: net.ErrClosed}, true},
		{"end of stream", io.EOF, true},
		{"cut mid-record", io.ErrUnexpectedEOF, true},
		{"handshake deadline", context.DeadlineExceeded, true},
		{"cancelled", context.Canceled, true},
		{"the pin or the name", pinErr, false},
		{"a non-TLS answer", tls.RecordHeaderError{Msg: "first record does not look like a TLS handshake"}, false},
		{"the peer's alert", &net.OpError{Op: "remote error", Err: errors.New("tls: bad certificate")}, false},
		{"a chain that does not verify", &tls.CertificateVerificationError{Err: x509.UnknownAuthorityError{}}, false},
		{"a failed key proof", errors.New("tls: invalid signature by the server certificate: ed25519: verification failure"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := transportError(tc.err); got != tc.lost {
				t.Errorf("transportError(%v) = %v, want %v", tc.err, got, tc.lost)
			}
		})
	}
}
