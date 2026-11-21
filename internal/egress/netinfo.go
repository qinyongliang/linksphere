package egress

import (
	"net"
	"os"
	"runtime"
	"sort"

	protocoltypes "github.com/qinyongliang/linksphere/internal/protocol/types"
)

// Probe reports the interfaces and addresses available for egress selection on
// this machine. It never changes network state.
func Probe() protocoltypes.NetInfo {
	info := protocoltypes.NetInfo{Platform: runtime.GOOS, Interfaces: []protocoltypes.NetInterface{}, PoolSource: poolSource()}
	info.Hostname, _ = os.Hostname()
	interfaces, err := net.Interfaces()
	if err != nil {
		return info
	}
	for _, iface := range interfaces {
		entry := protocoltypes.NetInterface{Name: iface.Name, Up: iface.Flags&net.FlagUp != 0, Loopback: iface.Flags&net.FlagLoopback != 0, MTU: iface.MTU}
		addresses, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addresses {
			ip, network, err := net.ParseCIDR(addr.String())
			if err != nil {
				continue
			}
			prefix, _ := network.Mask.Size()
			family := protocoltypes.EgressFamilyIPv6
			if ip.To4() != nil {
				family = protocoltypes.EgressFamilyIPv4
			}
			entry.Addresses = append(entry.Addresses, protocoltypes.NetAddress{Address: ip.String(), Prefix: prefix, Family: family, Scope: scopeOf(ip)})
			if !ip.IsLoopback() && !ip.IsLinkLocalUnicast() {
				if ip.To4() != nil {
					entry.Default4 = true
				} else {
					entry.Default6 = true
				}
			}
		}
		sort.Slice(entry.Addresses, func(i, j int) bool { return entry.Addresses[i].Address < entry.Addresses[j].Address })
		info.Interfaces = append(info.Interfaces, entry)
	}
	sort.Slice(info.Interfaces, func(i, j int) bool {
		if info.Interfaces[i].Loopback != info.Interfaces[j].Loopback {
			return !info.Interfaces[i].Loopback
		}
		return info.Interfaces[i].Name < info.Interfaces[j].Name
	})
	info.Default4 = defaultSource("udp4", "192.0.2.1:9")
	info.Default6 = defaultSource("udp6", "[2001:db8::1]:9")
	return info
}

// defaultSource asks the routing table which source address would be used. The
// destination is documentation-range and no packet is sent by a UDP connect.
func defaultSource(network, destination string) string {
	conn, err := net.Dial(network, destination)
	if err != nil {
		return ""
	}
	defer conn.Close()
	host, _, err := net.SplitHostPort(conn.LocalAddr().String())
	if err != nil {
		return ""
	}
	return host
}

func scopeOf(ip net.IP) string {
	switch {
	case ip.IsLoopback():
		return "loopback"
	case ip.IsLinkLocalUnicast():
		return "link-local"
	case ip.IsPrivate():
		return "private"
	}
	return "global"
}

// poolSource reports whether extra IPv6 addresses can be carried on this host.
func poolSource() string {
	switch runtime.GOOS {
	case "linux":
		return "linux-freebind"
	case "windows":
		return "windows-unsupported"
	case "darwin":
		return "darwin-unsupported"
	}
	return "unsupported"
}
