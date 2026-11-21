//go:build linux

package egress

import (
	"syscall"

	"golang.org/x/sys/unix"
)

// socketControl binds the socket to an interface and lets the kernel accept a
// non-local IPv6 source address (IPV6_FREEBIND), which is how an allocated pool
// address that is routed to this host but not assigned to a device still works.
func socketControl(iface string, freebind bool) (func(network, address string, c syscall.RawConn) error, error) {
	if iface == "" && !freebind {
		return nil, nil
	}
	return func(network, address string, c syscall.RawConn) error {
		var sockErr error
		err := c.Control(func(fd uintptr) {
			if iface != "" {
				if sockErr = unix.SetsockoptString(int(fd), unix.SOL_SOCKET, unix.SO_BINDTODEVICE, iface); sockErr != nil {
					return
				}
			}
			if freebind {
				if sockErr = unix.SetsockoptInt(int(fd), unix.IPPROTO_IPV6, unix.IPV6_FREEBIND, 1); sockErr != nil {
					sockErr = unix.SetsockoptInt(int(fd), unix.IPPROTO_IP, unix.IP_FREEBIND, 1)
				}
			}
		})
		if err != nil {
			return err
		}
		return sockErr
	}, nil
}
