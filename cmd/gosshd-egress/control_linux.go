//go:build linux

package main

import (
	"syscall"
)

// controlFor binds every outgoing socket to the configured device. Linux then
// routes the connection through that interface, so the host needs no policy
// routing or NAT rules of its own.
func controlFor(device string) func(network, address string, c syscall.RawConn) error {
	if device == "" {
		return nil
	}
	return func(network, address string, c syscall.RawConn) error {
		var serr error
		if err := c.Control(func(fd uintptr) {
			serr = syscall.SetsockoptString(int(fd), syscall.SOL_SOCKET, syscall.SO_BINDTODEVICE, device)
		}); err != nil {
			return err
		}
		return serr
	}
}
