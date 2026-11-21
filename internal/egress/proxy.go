package egress

import (
	"bufio"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	protocoltypes "github.com/qinyongliang/linksphere/internal/protocol/types"
)

type proxyDialer struct {
	spec  protocoltypes.EgressSpec
	proxy proxyEndpoint
}

func (d *proxyDialer) Spec() protocoltypes.EgressSpec { return d.spec }
func (d *proxyDialer) Close() error                   { return nil }

// DialContext first connects to the upstream proxy, then asks it for the
// destination. UDP is not tunnelled through the proxy: SOCKS5 UDP ASSOCIATE and
// HTTP CONNECT are TCP-only contracts.
func (d *proxyDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	if network != "tcp" {
		return nil, errors.New("proxy egress supports TCP only")
	}
	dialer := net.Dialer{Timeout: 10 * time.Second}
	conn, err := dialer.DialContext(ctx, "tcp", d.proxy.address)
	if err != nil {
		return nil, fmt.Errorf("connect upstream proxy %s: %w", d.proxy.address, err)
	}
	_ = conn.SetDeadline(time.Now().Add(15 * time.Second))
	switch d.proxy.scheme {
	case "socks5", "socks5h":
		err = d.handshakeSOCKS5(conn, address)
	default:
		err = d.handshakeHTTP(conn, address)
	}
	if err != nil {
		conn.Close()
		return nil, err
	}
	_ = conn.SetDeadline(time.Time{})
	return conn, nil
}

// handshakeSOCKS5 performs RFC 1928 CONNECT with optional RFC 1929 credentials.
func (d *proxyDialer) handshakeSOCKS5(conn net.Conn, address string) error {
	methods := []byte{0x00}
	if d.proxy.username != "" {
		methods = append(methods, 0x02)
	}
	greeting := append([]byte{0x05, byte(len(methods))}, methods...)
	if _, err := conn.Write(greeting); err != nil {
		return err
	}
	reply := make([]byte, 2)
	if _, err := io.ReadFull(conn, reply); err != nil {
		return err
	}
	if reply[0] != 0x05 {
		return errors.New("upstream proxy is not SOCKS5")
	}
	switch reply[1] {
	case 0x00:
	case 0x02:
		if d.proxy.username == "" {
			return errors.New("upstream proxy requires authentication")
		}
		user, pass := []byte(d.proxy.username), []byte(d.proxy.password)
		auth := append([]byte{0x01, byte(len(user))}, user...)
		auth = append(auth, byte(len(pass)))
		auth = append(auth, pass...)
		if _, err := conn.Write(auth); err != nil {
			return err
		}
		if _, err := io.ReadFull(conn, reply); err != nil {
			return err
		}
		if reply[1] != 0x00 {
			return errors.New("upstream proxy rejected the credentials")
		}
	case 0xff:
		return errors.New("upstream proxy accepted no offered authentication method")
	default:
		return errors.New("upstream proxy returned an unsupported authentication method")
	}
	host, port, err := splitHostPort(address)
	if err != nil {
		return err
	}
	request := []byte{0x05, 0x01, 0x00}
	if ip := net.ParseIP(host); ip != nil {
		if v4 := ip.To4(); v4 != nil {
			request = append(request, 0x01)
			request = append(request, v4...)
		} else {
			request = append(request, 0x04)
			request = append(request, ip.To16()...)
		}
	} else {
		if len(host) > 255 {
			return errors.New("destination host name too long")
		}
		request = append(request, 0x03, byte(len(host)))
		request = append(request, host...)
	}
	request = append(request, byte(port>>8), byte(port))
	if _, err := conn.Write(request); err != nil {
		return err
	}
	head := make([]byte, 4)
	if _, err := io.ReadFull(conn, head); err != nil {
		return err
	}
	if head[0] != 0x05 {
		return errors.New("invalid SOCKS5 reply")
	}
	if head[1] != 0x00 {
		return fmt.Errorf("upstream proxy refused CONNECT: %s", socksError(head[1]))
	}
	switch head[3] {
	case 0x01:
		_, err = io.CopyN(io.Discard, conn, 4+2)
	case 0x04:
		_, err = io.CopyN(io.Discard, conn, 16+2)
	case 0x03:
		length := make([]byte, 1)
		if _, err = io.ReadFull(conn, length); err == nil {
			_, err = io.CopyN(io.Discard, conn, int64(length[0])+2)
		}
	default:
		return errors.New("invalid SOCKS5 address type in reply")
	}
	return err
}

// handshakeHTTP issues CONNECT through the proxy.
func (d *proxyDialer) handshakeHTTP(conn net.Conn, address string) error {
	target := address
	if _, _, err := net.SplitHostPort(address); err != nil {
		return err
	}
	var builder strings.Builder
	builder.WriteString("CONNECT " + target + " HTTP/1.1\r\nHost: " + target + "\r\n")
	if d.proxy.username != "" {
		token := base64.StdEncoding.EncodeToString([]byte(d.proxy.username + ":" + d.proxy.password))
		builder.WriteString("Proxy-Authorization: Basic " + token + "\r\n")
	}
	builder.WriteString("User-Agent: LinkSphere\r\n\r\n")
	if _, err := io.WriteString(conn, builder.String()); err != nil {
		return err
	}
	reader := bufio.NewReader(conn)
	response, err := http.ReadResponse(reader, &http.Request{Method: http.MethodConnect})
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("upstream proxy refused CONNECT: %s", response.Status)
	}
	return nil
}

func splitHostPort(address string) (string, int, error) {
	host, rawPort, err := net.SplitHostPort(address)
	if err != nil {
		return "", 0, err
	}
	port, err := strconv.Atoi(rawPort)
	if err != nil || port < 1 || port > 65535 {
		return "", 0, errors.New("invalid destination port")
	}
	return host, port, nil
}

func socksError(code byte) string {
	switch code {
	case 0x01:
		return "general failure"
	case 0x02:
		return "connection not allowed"
	case 0x03:
		return "network unreachable"
	case 0x04:
		return "host unreachable"
	case 0x05:
		return "connection refused"
	case 0x06:
		return "TTL expired"
	case 0x07:
		return "command not supported"
	case 0x08:
		return "address type not supported"
	}
	return "error " + strconv.Itoa(int(code))
}
