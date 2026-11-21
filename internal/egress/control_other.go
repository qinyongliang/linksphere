//go:build !linux

package egress

import (
	"errors"
	"syscall"
)

// socketControl reports that source-address and interface pinning need Linux on
// the exit machine. Plain dialing keeps working everywhere.
func socketControl(iface string, freebind bool) (func(network, address string, c syscall.RawConn) error, error) {
	if iface == "" && !freebind {
		return nil, nil
	}
	return nil, errors.New("interface binding and IPv6 source pools require a Linux exit Agent")
}
