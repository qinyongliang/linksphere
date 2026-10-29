package agent

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

func validateRouterRefresh(iface string) error {
	if iface == "" {
		return nil
	}
	if os.Geteuid() != 0 {
		return errors.New("IPv6 router refresh requires rooted Android")
	}
	if len(iface) > 15 || strings.ContainsAny(iface, " /\t\r\n\x00") {
		return errors.New("invalid IPv6 router refresh interface")
	}
	return nil
}

func readIPv6Routes(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, "/system/bin/ip", "-6", "route", "show", "table", "all").Output()
	return string(output), err
}

func solicitIPv6Router(name string) error {
	iface, err := net.InterfaceByName(name)
	if err != nil {
		return err
	}
	if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
		return errors.New("router refresh interface is not active")
	}
	addrs, err := iface.Addrs()
	if err != nil {
		return err
	}
	var source [16]byte
	found := false
	for _, addr := range addrs {
		ip, _, parseErr := net.ParseCIDR(addr.String())
		if parseErr == nil && ip.To4() == nil && ip.IsLinkLocalUnicast() {
			copy(source[:], ip.To16())
			found = true
			break
		}
	}
	if !found {
		return errors.New("router refresh interface has no IPv6 link-local address")
	}
	fd, err := syscall.Socket(syscall.AF_INET6, syscall.SOCK_RAW|syscall.SOCK_CLOEXEC|syscall.SOCK_NONBLOCK, syscall.IPPROTO_ICMPV6)
	if err != nil {
		return err
	}
	defer syscall.Close(fd)
	for _, option := range []struct{ level, name, value int }{
		{syscall.IPPROTO_IPV6, syscall.IPV6_MULTICAST_IF, iface.Index},
		{syscall.IPPROTO_IPV6, syscall.IPV6_MULTICAST_HOPS, 255},
	} {
		if err := syscall.SetsockoptInt(fd, option.level, option.name, option.value); err != nil {
			return err
		}
	}
	if err := syscall.SetsockoptString(fd, syscall.SOL_SOCKET, syscall.SO_BINDTODEVICE, iface.Name); err != nil {
		return err
	}
	if err := syscall.Bind(fd, &syscall.SockaddrInet6{Addr: source, ZoneId: uint32(iface.Index)}); err != nil {
		return err
	}
	packet := make([]byte, 8)
	packet[0] = 133 // ICMPv6 Router Solicitation; the kernel supplies the checksum.
	if len(iface.HardwareAddr) == 6 {
		packet = append(packet, 1, 1) // Source Link-Layer Address option, one 8-byte unit.
		packet = append(packet, iface.HardwareAddr...)
	}
	var destination [16]byte
	copy(destination[:], net.ParseIP("ff02::2").To16())
	if err := syscall.Sendto(fd, packet, 0, &syscall.SockaddrInet6{Addr: destination, ZoneId: uint32(iface.Index)}); err != nil {
		return fmt.Errorf("router solicitation: %w", err)
	}
	return nil
}
