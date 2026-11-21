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
}

// pool allocates source addresses from a contiguous IPv6 range that starts at
// pool_start and holds pool_size addresses.
type pool struct {
	mu       sync.Mutex
	base     uint64
	size     int
	strategy string
	last     int
	rng      *rand.Rand
}

func newPool(spec protocoltypes.EgressSpec) *pool {
	var base uint64
	if ip := net.ParseIP(spec.PoolStart); ip != nil {
		base = binary.BigEndian.Uint64(ip.To16()[8:])
	}
	size := spec.PoolSize
	if size < 1 {
		size = 1
	}
	return &pool{base: base, size: size, strategy: spec.PoolMode, rng: rand.New(rand.NewSource(int64(base) ^ int64(size)<<17 ^ 0x2545F4914F6CDD1D))}
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
	ip := make(net.IP, 16)
	binary.BigEndian.PutUint64(ip[8:], p.base+uint64(index))
	return Allocation{IP: ip, Index: index, Rotated: rotated}
}

// Size exposes the configured member count.
func (p *pool) Size() int { return p.size }

// Last returns the most recently allocated index.
func (p *pool) Last() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.last
}
