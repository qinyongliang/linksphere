package egress

import (
	"encoding/binary"
	"math/rand"
	"net"
	"sync"

	protocoltypes "github.com/qinyongliang/linksphere/internal/protocol/types"
)

// Allocation is one IPv6 address chosen for an outbound connection.
type Allocation struct {
	IP      net.IP
	Index   int
	Rotated bool
	// ProvisionError explains why the address could not be assigned locally; the
	// connection still uses it as a source address via IPV6_FREEBIND.
	ProvisionError string
}

// pool allocates source addresses from a contiguous IPv6 range that starts at
// pool_start and holds pool_size addresses.
type pool struct {
	mu       sync.Mutex
	base     net.IP
	size     int
	strategy string
	last     int
	rng      *rand.Rand
}

func newPool(spec protocoltypes.EgressSpec) *pool {
	var base net.IP
	if ip := net.ParseIP(spec.PoolStart); ip != nil {
		// Keep the full 128 bits: the pool lives inside a routable prefix, so the
		// upper half must survive allocation.
		base = append(net.IP(nil), ip.To16()...)
	}
	if base == nil {
		base = make(net.IP, 16)
	}
	size := spec.PoolSize
	if size < 1 {
		size = 1
	}
	seed := int64(binary.BigEndian.Uint64(base[8:])) ^ int64(size)<<17 ^ 0x2545F4914F6CDD1D
	return &pool{base: base, size: size, strategy: spec.PoolMode, rng: rand.New(rand.NewSource(seed))}
}

// Next returns the next allocation. Rotate chooses a random member each time;
// sticky advances from the previous address by a random increment.
func (p *pool) Next() Allocation {
	p.mu.Lock()
	defer p.mu.Unlock()
	index := p.rng.Intn(p.size)
	rotated := true
	if p.strategy == protocoltypes.EgressPoolSticky {
		step := 1
		if p.size > 1 {
			step += p.rng.Intn(p.size)
		}
		index = (p.last + step) % p.size
		rotated = false
	}
	p.last = index
	ip := append(net.IP(nil), p.base...)
	low := binary.BigEndian.Uint64(ip[8:])
	next := low + uint64(index)
	binary.BigEndian.PutUint64(ip[8:], next)
	if next < low {
		// Carry into the upper half when the pool crosses a /64 boundary.
		high := binary.BigEndian.Uint64(ip[0:8]) + 1
		binary.BigEndian.PutUint64(ip[0:8], high)
	}
	// Assign the address to the pool device so replies are delivered to this
	// machine. When that is impossible (not root, unsupported platform) the
	// dialer keeps IPV6_FREEBIND, which sends but cannot receive.
	allocation := Allocation{IP: ip, Index: index, Rotated: rotated}
	if err := provisionAddress(ip); err != nil {
		allocation.ProvisionError = err.Error()
	}
	return allocation
}

// Size exposes the configured member count.
func (p *pool) Size() int { return p.size }

// Last returns the most recently allocated index.
func (p *pool) Last() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.last
}
