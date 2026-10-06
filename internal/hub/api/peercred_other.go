//go:build !darwin && !linux

package api

import (
	"errors"
	"net"
)

// KernelPeerUID cannot read peer credentials on this OS, so the admin
// socket serves nobody here; fleetd supports darwin and Linux.
func KernelPeerUID(*net.UnixConn) (uint32, error) {
	return 0, errors.New("api: peer credentials are not supported on this OS")
}
