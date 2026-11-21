package forward

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	protocoltypes "github.com/qinyongliang/linksphere/internal/protocol/types"
)

// ProxySpec configures one listen port that talks to clients instead of blindly
// streaming bytes. Mode "auto" sniffs the first bytes and serves SOCKS5, SOCKS4,
// HTTP proxy (including CONNECT for HTTPS) or a raw stream to Destination.
type ProxySpec struct {
	RuleID      string
	RouteID     string
	Mode        string
	Destination Location
}

func isProxyMode(mode string) bool {
	switch mode {
	case protocoltypes.ListenProtocolAuto, protocoltypes.ListenProtocolHTTP, protocoltypes.ListenProtocolSOCKS:
		return true
	}
	return false
}

// startProxy runs the accept loop of one multi-protocol listen port.
func startProxy(parent context.Context, listener net.Listener, spec ProxySpec, dial TargetDialFunc, logs EventRecorder, direct func(string) bool, counters *runtimeCounters) (*Runtime, error) {
	ctx, cancel := context.WithCancel(parent)
	r := &Runtime{listeners: []net.Listener{listener}, logs: logs, ctx: ctx, cancel: cancel, done: make(chan struct{}), conns: map[net.Conn]bool{}, runtimeCounters: counters}
	if direct == nil {
		direct = func(string) bool { return false }
	}
	go func() {
		defer close(r.done)
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			if !r.track(conn) {
				continue
			}
			r.wg.Add(1)
			go func(conn net.Conn) {
				defer r.wg.Done()
				defer r.untrack(conn)
				defer conn.Close()
				r.serveProxy(conn, spec, dial, direct)
			}(conn)
		}
	}()
	go func() { <-ctx.Done(); r.closeSockets() }()
	return r, nil
}

// serveProxy classifies one client and bridges its stream to the exit socket.
func (r *Runtime) serveProxy(client net.Conn, spec ProxySpec, dial TargetDialFunc, direct func(string) bool) {
	started := time.Now()
	reader := bufio.NewReaderSize(client, 4096)
	event := Event{Time: started.UnixMilli(), SourceIP: SourceIP(client.RemoteAddr().String()), RouteID: spec.RuleID}
	event.SourceConcurrent = r.openSource(event.SourceIP)
	defer r.closeSource(event.SourceIP)
	concurrent := r.open()
	defer r.active.Add(-1)
	event.Concurrent = concurrent
	routeIDs := spec.RouteID
	counts := &byteCounts{inflightUp: &r.inflightUp, inflightDown: &r.inflightDown, inflightDirectUp: &r.inflightDirectUp, inflightDirectDown: &r.inflightDirectDown, direct: func() bool { return direct(routeIDs) }}
	defer func() {
		event.DurationMS = time.Since(started).Milliseconds()
		event.Up = counts.up.Load()
		event.Down = counts.down.Load()
		event.DirectUp = counts.directUp.Load()
		event.DirectDown = counts.directDown.Load()
		counts.finish()
		r.logs.Record(event)
	}()

	head, err := reader.Peek(1)
	if err != nil {
		event.Error = err.Error()
		return
	}
	// A pinned protocol rejects foreign clients instead of silently proxying.
	mode := spec.Mode
	if mode != protocoltypes.ListenProtocolHTTP && mode != protocoltypes.ListenProtocolSOCKS {
		mode = protocoltypes.ListenProtocolAuto
	}
	switch {
	case head[0] == 0x05:
		if mode == protocoltypes.ListenProtocolHTTP {
			event.Error = "SOCKS5 client on an HTTP-only listener"
			return
		}
		event.Method = "SOCKS5"
		r.serveSOCKS5(client, reader, spec, dial, counts, &event)
	case head[0] == 0x04:
		if mode == protocoltypes.ListenProtocolHTTP {
			event.Error = "SOCKS4 client on an HTTP-only listener"
			return
		}
		event.Method = "SOCKS4"
		r.serveSOCKS4(client, reader, spec, dial, counts, &event)
	case looksLikeHTTP(head[0]):
		if mode == protocoltypes.ListenProtocolSOCKS {
			event.Error = "HTTP client on a SOCKS-only listener"
			return
		}
		event.Method = "HTTP"
		r.serveHTTPProxy(client, reader, spec, dial, counts, &event)
	default:
		if mode != protocoltypes.ListenProtocolAuto {
			event.Error = "unrecognized client on a protocol-pinned listener"
			return
		}
		// A TLS handshake, SSH banner or any other byte stream simply passes
		// through to the rule destination.
		event.Method = "TCP"
		r.tunnelRaw(client, reader, spec, dial, counts, &event)
	}
}

func looksLikeHTTP(b byte) bool { return b >= 'A' && b <= 'Z' }

// tunnelRaw bridges the client stream to the rule destination without parsing it.
func (r *Runtime) tunnelRaw(client net.Conn, reader *bufio.Reader, spec ProxySpec, dial TargetDialFunc, counts *byteCounts, event *Event) {
	upstream, err := dial(r.ctx, spec.RouteID, "")
	if err != nil {
		event.Error = err.Error()
		return
	}
	if !r.track(upstream) {
		return
	}
	defer r.untrack(upstream)
	defer upstream.Close()
	event.Upstream = spec.Destination.ExitTargetID + "@" + spec.Destination.Address()
	bridge(client, reader, upstream, counts)
}

// serveSOCKS5 implements RFC 1928 CONNECT. UDP ASSOCIATE and BIND are refused
// with the corresponding protocol error.
func (r *Runtime) serveSOCKS5(client net.Conn, reader *bufio.Reader, spec ProxySpec, dial TargetDialFunc, counts *byteCounts, event *Event) {
	head := make([]byte, 2)
	if _, err := io.ReadFull(reader, head); err != nil || head[0] != 0x05 {
		event.Error = "invalid SOCKS5 greeting"
		return
	}
	methods := make([]byte, int(head[1]))
	if _, err := io.ReadFull(reader, methods); err != nil {
		event.Error = err.Error()
		return
	}
	// No authentication is offered; the tunnel itself is the access boundary.
	if _, err := client.Write([]byte{0x05, 0x00}); err != nil {
		event.Error = err.Error()
		return
	}
	request := make([]byte, 4)
	if _, err := io.ReadFull(reader, request); err != nil {
		event.Error = err.Error()
		return
	}
	if request[0] != 0x05 {
		event.Error = "invalid SOCKS5 request"
		return
	}
	target, err := readSOCKS5Target(reader, request[3])
	if err != nil {
		writeSOCKS5Reply(client, 0x08)
		event.Error = err.Error()
		return
	}
	if request[1] != 0x01 {
		writeSOCKS5Reply(client, 0x07)
		event.Error = "unsupported SOCKS5 command"
		return
	}
	event.Upstream = target
	upstream, err := dial(r.ctx, spec.RouteID, target)
	if err != nil {
		writeSOCKS5Reply(client, 0x05)
		event.Error = err.Error()
		return
	}
	if !r.track(upstream) {
		return
	}
	defer r.untrack(upstream)
	defer upstream.Close()
	writeSOCKS5Reply(client, 0x00)
	bridge(client, reader, upstream, counts)
}

func readSOCKS5Target(reader io.Reader, addressType byte) (string, error) {
	var host string
	switch addressType {
	case 0x01:
		buf := make([]byte, 4)
		if _, err := io.ReadFull(reader, buf); err != nil {
			return "", err
		}
		host = net.IP(buf).String()
	case 0x03:
		length := make([]byte, 1)
		if _, err := io.ReadFull(reader, length); err != nil {
			return "", err
		}
		name := make([]byte, int(length[0]))
		if _, err := io.ReadFull(reader, name); err != nil {
			return "", err
		}
		host = string(name)
	case 0x04:
		buf := make([]byte, 16)
		if _, err := io.ReadFull(reader, buf); err != nil {
			return "", err
		}
		host = net.IP(buf).String()
	default:
		return "", errors.New("unsupported SOCKS5 address type")
	}
	port := make([]byte, 2)
	if _, err := io.ReadFull(reader, port); err != nil {
		return "", err
	}
	return net.JoinHostPort(host, strconv.Itoa(int(binary.BigEndian.Uint16(port)))), nil
}

func writeSOCKS5Reply(w io.Writer, code byte) {
	_, _ = w.Write([]byte{0x05, code, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
}

// serveSOCKS4 implements the original SOCKS4 CONNECT request.
func (r *Runtime) serveSOCKS4(client net.Conn, reader *bufio.Reader, spec ProxySpec, dial TargetDialFunc, counts *byteCounts, event *Event) {
	// VN CD DSTPORT(2) DSTIP(4) USERID NUL [HOSTNAME NUL]
	head := make([]byte, 8)
	if _, err := io.ReadFull(reader, head); err != nil {
		event.Error = err.Error()
		return
	}
	if head[0] != 0x04 || head[1] != 0x01 {
		_, _ = client.Write([]byte{0x00, 0x5b, 0, 0, 0, 0, 0, 0})
		event.Error = "unsupported SOCKS4 command"
		return
	}
	port := int(binary.BigEndian.Uint16(head[2:4]))
	ip := net.IP(head[4:8]).String()
	userID, userErr := reader.ReadString(0)
	if userErr != nil {
		event.Error = userErr.Error()
		return
	}
	_ = userID
	if head[4] == 0 && head[5] == 0 && head[6] == 0 && head[7] != 0 {
		// SOCKS4a sends the host name after the null-terminated user ID.
		name, err := reader.ReadString(0)
		if err != nil {
			event.Error = err.Error()
			return
		}
		ip = strings.TrimSuffix(name, "\x00")
	}
	target := net.JoinHostPort(ip, strconv.Itoa(port))
	event.Upstream = target
	upstream, err := dial(r.ctx, spec.RouteID, target)
	if err != nil {
		_, _ = client.Write([]byte{0x00, 0x5b, 0, 0, 0, 0, 0, 0})
		event.Error = err.Error()
		return
	}
	if !r.track(upstream) {
		return
	}
	defer r.untrack(upstream)
	defer upstream.Close()
	_, _ = client.Write([]byte{0x00, 0x5a, 0, 0, 0, 0, 0, 0})
	bridge(client, reader, upstream, counts)
}

// serveHTTPProxy handles CONNECT tunnels and absolute-form proxy requests.
func (r *Runtime) serveHTTPProxy(client net.Conn, reader *bufio.Reader, spec ProxySpec, dial TargetDialFunc, counts *byteCounts, event *Event) {
	request, err := http.ReadRequest(reader)
	if err != nil {
		event.Error = err.Error()
		_, _ = io.WriteString(client, "HTTP/1.1 400 Bad Request\r\n\r\n")
		return
	}
	if request.Method == http.MethodConnect {
		target := request.Host
		if _, _, err := net.SplitHostPort(target); err != nil {
			target = net.JoinHostPort(target, "443")
		}
		event.Method = "CONNECT"
		event.Upstream = target
		upstream, err := dial(r.ctx, spec.RouteID, target)
		if err != nil {
			event.Error = err.Error()
			_, _ = io.WriteString(client, "HTTP/1.1 502 Bad Gateway\r\n\r\n")
			return
		}
		if !r.track(upstream) {
			return
		}
		defer r.untrack(upstream)
		defer upstream.Close()
		if _, err := io.WriteString(client, "HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
			event.Error = err.Error()
			return
		}
		bridge(client, reader, upstream, counts)
		return
	}
	// Absolute-form request: forward it to the requested origin unchanged. The
	// origin-form fallback uses the rule destination, which keeps the listener
	// usable as a transparent HTTP gateway.
	target := request.URL.Host
	if target == "" {
		target = spec.Destination.Address()
	} else if _, _, err := net.SplitHostPort(target); err != nil {
		target = net.JoinHostPort(target, "80")
	}
	event.Upstream = target
	upstream, err := dial(r.ctx, spec.RouteID, target)
	if err != nil {
		event.Error = err.Error()
		_, _ = io.WriteString(client, "HTTP/1.1 502 Bad Gateway\r\n\r\n")
		return
	}
	if !r.track(upstream) {
		return
	}
	defer r.untrack(upstream)
	defer upstream.Close()
	request.RequestURI = ""
	if err := request.Write(upstream); err != nil {
		event.Error = err.Error()
		_, _ = io.WriteString(client, "HTTP/1.1 502 Bad Gateway\r\n\r\n")
		return
	}
	response, err := http.ReadResponse(bufio.NewReader(upstream), request)
	if err != nil {
		event.Error = err.Error()
		_, _ = io.WriteString(client, "HTTP/1.1 502 Bad Gateway\r\n\r\n")
		return
	}
	defer response.Body.Close()
	event.Status = response.StatusCode
	if err := response.Write(client); err != nil {
		event.Error = err.Error()
	}
}

// bridge copies both directions and counts bytes per direction.
func bridge(client net.Conn, reader *bufio.Reader, upstream net.Conn, counts *byteCounts) {
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = io.Copy(countWriter{w: upstream, counts: counts, up: true}, reader)
		if cw, ok := upstream.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
		} else {
			_ = upstream.Close()
		}
	}()
	go func() {
		defer wg.Done()
		_, _ = io.Copy(countWriter{w: client, counts: counts}, upstream)
		if cw, ok := client.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
		} else {
			_ = client.Close()
		}
	}()
	wg.Wait()
}
