//go:build !linux

package egress

import (
	"errors"
	"net"
)

// provisionAddress reports that address allocation needs Linux.
func provisionAddress(ip net.IP) error {
	return errors.New("IPv6 pool address allocation requires a Linux exit machine")
}
