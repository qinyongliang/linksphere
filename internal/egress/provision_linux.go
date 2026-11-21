//go:build linux

package egress

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
	"sync"
)

// poolDevice is the dummy interface that carries allocated IPv6 addresses. A
// dummy device keeps the host routing and existing connectivity untouched: the
// addresses exist only so the kernel accepts replies for them.
const poolDevice = "lsv6pool"

var provisionMu sync.Mutex
var provisioned = map[string]bool{}

// provisionAddress assigns one pool address to the dummy interface. It is a
// no-op for an address that already exists or when the process is not root; the
// dialer then falls back to IPV6_FREEBIND, which still sends but cannot receive.
func provisionAddress(ip net.IP) error {
	if os.Geteuid() != 0 {
		return fmt.Errorf("address allocation requires root")
	}
	key := ip.String()
	provisionMu.Lock()
	defer provisionMu.Unlock()
	if provisioned[key] {
		return nil
	}
	if err := ensurePoolDevice(); err != nil {
		return err
	}
	cmd := exec.Command("ip", "-6", "addr", "add", key+"/128", "dev", poolDevice, "nodad")
	output, err := cmd.CombinedOutput()
	if err != nil && !strings.Contains(string(output), "File exists") {
		return fmt.Errorf("assign %s: %v: %s", key, err, strings.TrimSpace(string(output)))
	}
	provisioned[key] = true
	return nil
}

// ensurePoolDevice creates the pool dummy interface once.
func ensurePoolDevice() error {
	if _, err := net.InterfaceByName(poolDevice); err == nil {
		return nil
	}
	if output, err := exec.Command("ip", "link", "add", poolDevice, "type", "dummy").CombinedOutput(); err != nil {
		return fmt.Errorf("create %s: %v: %s", poolDevice, err, strings.TrimSpace(string(output)))
	}
	if output, err := exec.Command("ip", "link", "set", poolDevice, "up").CombinedOutput(); err != nil {
		return fmt.Errorf("bring up %s: %v: %s", poolDevice, err, strings.TrimSpace(string(output)))
	}
	return nil
}
