// Command gosshd-egress is a small forwarding proxy for hosts that cannot run
// the full Agent (for example a 32-bit MIPS router). It binds every outgoing
// connection to one network device, so a multi-WAN host picks the WAN with a
// socket option instead of policy routing, and nothing on the host has to be
// configured by hand.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// version is stamped by the release build.
var version = "dev"

type config struct {
	listen string
	device string
	allow  string
	family string
	quiet  bool
}

func main() {
	var cfg config
	flag.StringVar(&cfg.listen, "listen", "127.0.0.1:1080", "listen address, host:port")
	flag.StringVar(&cfg.device, "device", "", "network device every outgoing connection is bound to (optional)")
	flag.StringVar(&cfg.allow, "allow", "", "comma separated client CIDRs; empty allows every client")
	flag.StringVar(&cfg.family, "family", "auto", "address family for outgoing connections: auto, ipv4 or ipv6")
	flag.BoolVar(&cfg.quiet, "quiet", false, "disable per-connection logging")
	flag.Parse()

	var allow []*net.IPNet
	for _, entry := range strings.Split(cfg.allow, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		if !strings.Contains(entry, "/") {
			if ip := net.ParseIP(entry); ip != nil {
				bits := 32
				if ip.To4() == nil {
					bits = 128
				}
				entry = fmt.Sprintf("%s/%d", ip.String(), bits)
			}
		}
		if _, network, err := net.ParseCIDR(entry); err == nil {
			allow = append(allow, network)
		}
	}

	control := controlFor(cfg.device)
	if cfg.device != "" {
		if _, err := net.InterfaceByName(cfg.device); err != nil {
			log.Fatalf("device %q: %v", cfg.device, err)
		}
	}
	dialer := &net.Dialer{Timeout: 15 * time.Second, Control: control}
	network := dialNetwork(cfg.family)

	ln, err := net.Listen("tcp", cfg.listen)
	if err != nil {
		log.Fatalf("listen %s: %v", cfg.listen, err)
	}
	log.Printf("egress %s listening on %s (device %q, family %s)", version, cfg.listen, cfg.device, cfg.family)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		ln.Close()
	}()
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		if !allowed(conn.RemoteAddr(), allow) {
			conn.Close()
			continue
		}
		go serve(conn, dialer, network, cfg.quiet)
	}
}

func allowed(addr net.Addr, allow []*net.IPNet) bool {
	if len(allow) == 0 {
		return true
	}
	host, _, err := net.SplitHostPort(addr.String())
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	for _, network := range allow {
		if network.Contains(ip) {
			return true
		}
	}
	return false
}

// serve sniffs the first byte so one port speaks SOCKS5 and HTTP CONNECT.
func serve(client net.Conn, dialer *net.Dialer, network string, quiet bool) {
	defer client.Close()
	_ = client.SetDeadline(time.Now().Add(30 * time.Second))
	head := make([]byte, 1)
	if _, err := io.ReadFull(client, head); err != nil {
		return
	}
	_ = client.SetDeadline(time.Time{})
	if head[0] == 0x05 {
		serveSOCKS5(client, dialer, network, quiet)
		return
	}
	serveHTTP(client, dialer, network, head[0], quiet)
}

func dialNetwork(family string) string {
	switch strings.ToLower(strings.TrimSpace(family)) {
	case "ipv4", "4":
		return "tcp4"
	case "ipv6", "6":
		return "tcp6"
	}
	return "tcp"
}

func dial(dialer *net.Dialer, network, target string) (net.Conn, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return dialer.DialContext(ctx, network, target)
}

func bridge(a, b net.Conn) {
	var once sync.Once
	closeBoth := func() { a.Close(); b.Close() }
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(a, b); once.Do(func() { a.(*net.TCPConn).CloseWrite() }); done <- struct{}{} }()
	go func() { _, _ = io.Copy(b, a); once.Do(func() { b.(*net.TCPConn).CloseWrite() }); done <- struct{}{} }()
	<-done
	<-done
	closeBoth()
}

func serveSOCKS5(client net.Conn, dialer *net.Dialer, network string, quiet bool) {
	// Greeting: version, method count, then that many methods.
	count := make([]byte, 1)
	if _, err := io.ReadFull(client, count); err != nil {
		return
	}
	if _, err := io.ReadFull(client, make([]byte, int(count[0]))); err != nil {
		return
	}
	if _, err := client.Write([]byte{0x05, 0x00}); err != nil {
		return
	}
	req := make([]byte, 4)
	if _, err := io.ReadFull(client, req); err != nil {
		return
	}
	if req[0] != 0x05 || req[1] != 0x01 {
		client.Write([]byte{0x05, 0x07, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		return
	}
	host, err := readAddress(client, req[3])
	if err != nil {
		return
	}
	port := make([]byte, 2)
	if _, err := io.ReadFull(client, port); err != nil {
		return
	}
	target := net.JoinHostPort(host, strconv.Itoa(int(port[0])<<8|int(port[1])))
	upstream, err := dial(dialer, network, target)
	if err != nil {
		client.Write([]byte{0x05, 0x05, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		if !quiet {
			log.Printf("socks %s: %v", target, err)
		}
		return
	}
	defer upstream.Close()
	if _, err := client.Write([]byte{0x05, 0x00, 0x00, 0x01, 0, 0, 0, 0, 0, 0}); err != nil {
		return
	}
	if !quiet {
		log.Printf("socks %s open", target)
	}
	bridge(client, upstream)
}

func readAddress(conn net.Conn, atyp byte) (string, error) {
	switch atyp {
	case 0x01:
		b := make([]byte, 4)
		if _, err := io.ReadFull(conn, b); err != nil {
			return "", err
		}
		return net.IP(b).String(), nil
	case 0x03:
		l := make([]byte, 1)
		if _, err := io.ReadFull(conn, l); err != nil {
			return "", err
		}
		b := make([]byte, int(l[0]))
		if _, err := io.ReadFull(conn, b); err != nil {
			return "", err
		}
		return string(b), nil
	case 0x04:
		b := make([]byte, 16)
		if _, err := io.ReadFull(conn, b); err != nil {
			return "", err
		}
		return net.IP(b).String(), nil
	}
	return "", errors.New("unsupported address type")
}

func serveHTTP(client net.Conn, dialer *net.Dialer, network string, first byte, quiet bool) {
	buf := []byte{first}
	tmp := make([]byte, 1)
	for {
		if _, err := io.ReadFull(client, tmp); err != nil {
			return
		}
		buf = append(buf, tmp[0])
		if len(buf) > 8192 {
			return
		}
		if len(buf) >= 4 && string(buf[len(buf)-4:]) == "\r\n\r\n" {
			break
		}
	}
	lines := strings.Split(string(buf), "\r\n")
	parts := strings.Fields(lines[0])
	if len(parts) < 2 || !strings.EqualFold(parts[0], "CONNECT") {
		client.Write([]byte("HTTP/1.1 405 Method Not Allowed\r\nContent-Length: 0\r\n\r\n"))
		return
	}
	upstream, err := dial(dialer, network, parts[1])
	if err != nil {
		client.Write([]byte("HTTP/1.1 502 Bad Gateway\r\nContent-Length: 0\r\n\r\n"))
		if !quiet {
			log.Printf("connect %s: %v", parts[1], err)
		}
		return
	}
	defer upstream.Close()
	if _, err := client.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n")); err != nil {
		return
	}
	if !quiet {
		log.Printf("connect %s open", parts[1])
	}
	bridge(client, upstream)
}
