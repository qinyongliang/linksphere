//go:build !linux

package main

import "syscall"

// controlFor is a no-op off Linux, where SO_BINDTODEVICE is unavailable.
func controlFor(device string) func(network, address string, c syscall.RawConn) error {
	return nil
}
