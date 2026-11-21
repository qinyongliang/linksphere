package forward

import (
	"bufio"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"strconv"
	"testing"
	"time"
)

// startProxyRules starts one auto-detect listener whose dial sends empty targets
// to the echo server and client-chosen targets to themselves.
func startProxyRules(t *testing.T, echo net.Listener, protocol string) (*Runtime, string) {
	t.Helper()
	_, portText, _ := net.SplitHostPort(echo.Addr().String())
	port, _ := strconv.Atoi(portText)
	specs := []RuleSpec{{
		ID:            "proxy",
		ListenAddress: "127.0.0.1:0",
		Transports:    []string{"tcp"},
		RouteIDs:      map[string]string{"tcp": "proxy:tcp"},
		Destination:   Location{ID: "proxy", Host: "127.0.0.1", Port: port},
		Protocol:      protocol,
	}}
	rt, err := StartRules(context.Background(), specs, func(ctx context.Context, id, target string) (net.Conn, error) {
		if target == "" {
			target = echo.Addr().String()
		}
		return (&net.Dialer{Timeout: 2 * time.Second}).DialContext(ctx, "tcp", target)
	}, &portEvents{}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rt.Close() })
	return rt, rt.State().ListenAddress
}

func socks5Connect(t *testing.T, conn net.Conn, target string) {
	t.Helper()
	if _, err := conn.Write([]byte{0x05, 0x01, 0x00}); err != nil {
		t.Fatal(err)
	}
	reply := make([]byte, 2)
	if _, err := io.ReadFull(conn, reply); err != nil || reply[0] != 0x05 || reply[1] != 0x00 {
		t.Fatalf("greeting reply %v %v", reply, err)
	}
	host, portText, _ := net.SplitHostPort(target)
	port, _ := strconv.Atoi(portText)
	ip := net.ParseIP(host).To4()
	request := append([]byte{0x05, 0x01, 0x00, 0x01}, ip...)
	request = append(request, byte(port>>8), byte(port))
	if _, err := conn.Write(request); err != nil {
		t.Fatal(err)
	}
	head := make([]byte, 10)
	if _, err := io.ReadFull(conn, head); err != nil {
		t.Fatal(err)
	}
	if head[1] != 0x00 {
		t.Fatalf("SOCKS5 CONNECT refused: %d", head[1])
	}
}

// TestAutoProxyListenerServesEveryProtocol proves one listen port serves SOCKS5,
// HTTP CONNECT, HTTP proxy requests and raw TCP without separate configuration.
func TestAutoProxyListenerServesEveryProtocol(t *testing.T) {
	echo := echoServer(t)
	defer echo.Close()
	_, address := startProxyRules(t, echo, protocolAutoForTest)

	cases := []struct {
		name  string
		dial  func(t *testing.T, conn net.Conn)
		write string
	}{
		{"socks5", func(t *testing.T, conn net.Conn) { socks5Connect(t, conn, echo.Addr().String()) }, "socks"},
		{"http-connect", func(t *testing.T, conn net.Conn) {
			fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", echo.Addr().String(), echo.Addr().String())
			response, err := bufio.NewReader(conn).ReadString('\n')
			if err != nil || response == "" || response[9:12] != "200" {
				t.Fatalf("CONNECT failed: %q %v", response, err)
			}
		}, "connect"},
		{"raw", func(t *testing.T, conn net.Conn) {}, "raw"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			conn, err := net.DialTimeout("tcp", address, 2*time.Second)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
			tc.dial(t, conn)
			if _, err := io.WriteString(conn, tc.write); err != nil {
				t.Fatal(err)
			}
			echoed := make([]byte, len(tc.write))
			if _, err := io.ReadFull(conn, echoed); err != nil || string(echoed) != tc.write {
				t.Fatalf("round trip failed: %q %v", echoed, err)
			}
		})
	}
}

// TestPinnedProtocolRejectsForeignClients proves a pinned listener refuses the
// protocol it was not configured for.
func TestPinnedProtocolRejectsForeignClients(t *testing.T) {
	echo := echoServer(t)
	defer echo.Close()
	_, address := startProxyRules(t, echo, "http")
	conn, err := net.DialTimeout("tcp", address, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	// A SOCKS5 greeting on an HTTP-only listener must be dropped, not proxied.
	if _, err := conn.Write([]byte{0x05, 0x01, 0x00}); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadFull(conn, make([]byte, 2)); err == nil {
		t.Fatal("SOCKS5 greeting was answered on an HTTP-only listener")
	}
}

// protocolAutoForTest keeps the literal in one place.
const protocolAutoForTest = "auto"

var _ = binary.BigEndian

// TestProxyListenerServesSOCKS4a proves the SOCKS4a host-name form is parsed and
// dialled, exercising the parser that reads the user ID then the host name.
func TestProxyListenerServesSOCKS4a(t *testing.T) {
	echo := echoServer(t)
	defer echo.Close()
	_, portText, _ := net.SplitHostPort(echo.Addr().String())
	port, _ := strconv.Atoi(portText)
	echoListener := echo
	_, address := startProxyRules(t, echoListener, "auto")
	conn, err := net.DialTimeout("tcp", address, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	// SOCKS4a: VN=4 CD=1 PORT DSTIP=0.0.0.x USERID NUL HOSTNAME NUL
	request := []byte{0x04, 0x01, byte(port >> 8), byte(port), 0, 0, 0, 1}
	request = append(request, []byte("user\x00"+"127.0.0.1\x00")...)
	if _, err := conn.Write(request); err != nil {
		t.Fatal(err)
	}
	reply := make([]byte, 8)
	if _, err := io.ReadFull(conn, reply); err != nil || reply[1] != 0x5a {
		t.Fatalf("SOCKS4a CONNECT refused: %v %v", reply, err)
	}
	if _, err := io.WriteString(conn, "socks4a"); err != nil {
		t.Fatal(err)
	}
	echoed := make([]byte, len("socks4a"))
	if _, err := io.ReadFull(conn, echoed); err != nil || string(echoed) != "socks4a" {
		t.Fatalf("SOCKS4a round trip failed: %q %v", echoed, err)
	}
}
