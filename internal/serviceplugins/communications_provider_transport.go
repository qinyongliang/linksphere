package serviceplugins

import (
	"context"
	"errors"
	"net"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"golang.org/x/crypto/ssh"
)

var ErrProviderTransportDenied = errors.New("provider transport denied")

// ProviderTransportConfig describes a dedicated, locally configured service
// identity. It neither enrolls credentials nor authenticates a human actor.
type ProviderTransportConfig struct {
	Enabled          bool
	ProviderID       string
	ServerKey        ssh.PublicKey
	ServiceSigner    ssh.Signer
	HandshakeTimeout time.Duration
}

// ProviderTransport exposes lifecycle only. There is deliberately no channel,
// command or dispatcher API, and no incoming channel is accepted in this slice.
type ProviderTransport struct {
	raw        net.Conn
	lifetime   context.Context
	done       chan struct{}
	cancel     context.CancelFunc
	providerID string
	verified   bool
	drained    chan struct{}
	closeOnce  sync.Once
}

// NewProviderTransport does not dial or listen. Disabled leaves raw untouched;
// enabled takes ownership of this dedicated connection, including on failure.
// The caller must bound concurrent constructors. A custom signer must cooperate:
// a network deadline cannot forcibly interrupt arbitrary Sign implementations.
func NewProviderTransport(ctx context.Context, raw net.Conn, config ProviderTransportConfig) (*ProviderTransport, error) {
	return newProviderTransport(func() Environment { return Environment{goos: runtime.GOOS, uid: os.Geteuid()} }, ctx, raw, config)
}

func newProviderTransport(environment func() Environment, ctx context.Context, raw net.Conn, config ProviderTransportConfig) (*ProviderTransport, error) {
	if !config.Enabled {
		return nil, nil
	}
	if nilInterface(raw) {
		return nil, ErrProviderTransportDenied
	}
	var transport *ProviderTransport
	accepted := false
	defer func() {
		if !accepted {
			if transport != nil {
				_ = transport.Close()
			} else {
				_ = raw.Close()
			}
		}
	}()
	if environment == nil || ctx == nil || ctx.Err() != nil || config.HandshakeTimeout <= 0 || config.HandshakeTimeout > time.Minute {
		return nil, ErrProviderTransportDenied
	}
	env := environment()
	if env.GOOS() != "android" || env.UID() != 0 {
		return nil, ErrProviderTransportDenied
	}
	if config.ProviderID == "" || len(config.ProviderID) > 256 || strings.TrimSpace(config.ProviderID) != config.ProviderID || strings.ContainsAny(config.ProviderID, "\x00\r\n\t*?") || !utf8.ValidString(config.ProviderID) || nilInterface(config.ServerKey) || nilInterface(config.ServiceSigner) {
		return nil, ErrProviderTransportDenied
	}
	if _, certificate := config.ServerKey.(*ssh.Certificate); certificate {
		return nil, ErrProviderTransportDenied
	}
	pin, err := ssh.ParsePublicKey(append([]byte(nil), config.ServerKey.Marshal()...))
	if err != nil {
		return nil, ErrProviderTransportDenied
	}
	if _, certificate := pin.(*ssh.Certificate); certificate {
		return nil, ErrProviderTransportDenied
	}
	public := config.ServiceSigner.PublicKey()
	if nilInterface(public) {
		return nil, ErrProviderTransportDenied
	}
	if _, certificate := public.(*ssh.Certificate); certificate {
		return nil, ErrProviderTransportDenied
	}
	publicSnapshot, err := ssh.ParsePublicKey(append([]byte(nil), public.Marshal()...))
	if err != nil {
		return nil, ErrProviderTransportDenied
	}
	if _, certificate := publicSnapshot.(*ssh.Certificate); certificate {
		return nil, ErrProviderTransportDenied
	}
	handshake, cancel := context.WithTimeout(ctx, config.HandshakeTimeout)
	stop := context.AfterFunc(handshake, func() { _ = raw.Close() })
	defer func() { stop(); cancel() }()
	deadline, _ := handshake.Deadline()
	if raw.SetDeadline(deadline) != nil {
		return nil, ErrProviderTransportDenied
	}
	conn, channels, requests, err := ssh.NewClientConn(raw, "dedicated-provider", &ssh.ClientConfig{
		User: strings.Clone(config.ProviderID), Auth: []ssh.AuthMethod{ssh.PublicKeys(config.ServiceSigner)}, HostKeyCallback: ssh.FixedHostKey(pin),
	})
	if conn != nil {
		transport = trackProviderTransport(raw, conn, channels, requests)
	}
	if err != nil || conn == nil || handshake.Err() != nil {
		return nil, ErrProviderTransportDenied
	}
	if raw.SetDeadline(time.Time{}) != nil || !stop() || handshake.Err() != nil {
		return nil, ErrProviderTransportDenied
	}
	select {
	case <-transport.Done():
		return nil, ErrProviderTransportDenied
	default:
	}
	transport.providerID = strings.Clone(config.ProviderID)
	transport.verified = true
	accepted = true
	return transport, nil
}

func trackProviderTransport(raw net.Conn, conn ssh.Conn, channels <-chan ssh.NewChannel, requests <-chan *ssh.Request) *ProviderTransport {
	lifetime, cancel := context.WithCancel(context.Background())
	transport := &ProviderTransport{raw: raw, lifetime: lifetime, cancel: cancel, done: make(chan struct{}), drained: make(chan struct{})}
	rejected := make(chan struct{})
	go func() {
		defer close(rejected)
		for channels != nil || requests != nil {
			select {
			case channel, ok := <-channels:
				if !ok {
					channels = nil
				} else {
					_ = channel.Reject(ssh.Prohibited, "provider dispatch disabled")
				}
			case request, ok := <-requests:
				if !ok {
					requests = nil
				} else if request.WantReply {
					_ = request.Reply(false, nil)
				}
			}
		}
	}()
	go func() { _ = conn.Wait(); _ = transport.Close(); <-rejected; close(transport.drained) }()
	return transport
}

// Done closes after synchronous dispatcher-session cancellation, before raw Close.
// It is not proof every provider operation or drain goroutine has exited.
// A handshake context is not retained as the successful transport's lifetime.
func (t *ProviderTransport) Done() <-chan struct{} {
	if t == nil {
		return nil
	}
	return t.done
}
func (t *ProviderTransport) Close() error {
	if t != nil {
		t.closeOnce.Do(func() {
			if t.cancel != nil {
				t.cancel()
			}
			if t.done != nil {
				close(t.done)
			}
			if !nilInterface(t.raw) {
				_ = t.raw.Close()
			}
		})
	}
	return nil
}
