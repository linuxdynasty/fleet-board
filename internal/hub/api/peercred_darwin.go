//go:build darwin

package api

import (
	"errors"
	"fmt"
	"net"

	"golang.org/x/sys/unix"
)

// xucredVersion is XUCRED_VERSION of <sys/ucred.h>; golang.org/x/sys has no
// constant for it.
const xucredVersion = 0

// KernelPeerUID is the peer's effective user id from LOCAL_PEERCRED.
func KernelPeerUID(c *net.UnixConn) (uint32, error) {
	raw, err := c.SyscallConn()
	if err != nil {
		return 0, fmt.Errorf("api: peer credentials: %w", err)
	}
	var cred *unix.Xucred
	var cerr error
	if err := raw.Control(func(fd uintptr) {
		cred, cerr = unix.GetsockoptXucred(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
	}); err != nil {
		return 0, fmt.Errorf("api: peer credentials: %w", err)
	}
	if cerr != nil {
		return 0, fmt.Errorf("api: LOCAL_PEERCRED: %w", cerr)
	}
	if cred.Version != xucredVersion {
		return 0, errors.New("api: LOCAL_PEERCRED: unknown xucred version")
	}
	return cred.Uid, nil
}
