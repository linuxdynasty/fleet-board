//go:build linux

package api

import (
	"fmt"
	"net"

	"golang.org/x/sys/unix"
)

// KernelPeerUID is the peer's user id from SO_PEERCRED (taken when the peer
// connected).
func KernelPeerUID(c *net.UnixConn) (uint32, error) {
	raw, err := c.SyscallConn()
	if err != nil {
		return 0, fmt.Errorf("api: peer credentials: %w", err)
	}
	var cred *unix.Ucred
	var cerr error
	if err := raw.Control(func(fd uintptr) {
		cred, cerr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	}); err != nil {
		return 0, fmt.Errorf("api: peer credentials: %w", err)
	}
	if cerr != nil {
		return 0, fmt.Errorf("api: SO_PEERCRED: %w", cerr)
	}
	return cred.Uid, nil
}
