package helm

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/net/proxy"
)

type helmIPResolver interface {
	LookupIPAddr(context.Context, string) ([]net.IPAddr, error)
}

type helmDialer interface {
	DialContext(context.Context, string, string) (net.Conn, error)
}

type helmRepositoryNetwork struct {
	resolver     helmIPResolver
	dialer       helmDialer
	proxy        func(*http.Request) (*url.URL, error)
	allowAddress func(net.IP) bool
}

var defaultHelmRepositoryNetwork = &helmRepositoryNetwork{
	resolver: net.DefaultResolver,
	dialer:   &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second},
	proxy:    http.ProxyFromEnvironment,
}

type helmProxyRouteKey struct{}

type helmGuardedTransport struct {
	base    *http.Transport
	network *helmRepositoryNetwork
}

func (t *helmGuardedTransport) CloseIdleConnections() {
	t.base.CloseIdleConnections()
}

func newHelmGuardedTransport(base *http.Transport, network *helmRepositoryNetwork) *helmGuardedTransport {
	if network == nil {
		network = defaultHelmRepositoryNetwork
	}
	base.Proxy = nil
	base.DisableKeepAlives = true
	base.DialContext = func(ctx context.Context, networkName, address string) (net.Conn, error) {
		return network.dialContext(ctx, networkName, address)
	}
	return &helmGuardedTransport{base: base, network: network}
}

func (t *helmGuardedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL == nil || (strings.ToLower(req.URL.Scheme) != "http" && strings.ToLower(req.URL.Scheme) != "https") {
		return nil, fmt.Errorf("unsupported Helm repository URL scheme")
	}
	if req.URL.Hostname() == "" {
		return nil, fmt.Errorf("Helm repository URL has no host")
	}
	var route *url.URL
	var err error
	if t.network.proxy != nil {
		route, err = t.network.proxy(req)
	}
	if err != nil {
		return nil, fmt.Errorf("resolve Helm proxy: %w", err)
	}
	ctx := context.WithValue(req.Context(), helmProxyRouteKey{}, route)
	return t.base.RoundTrip(req.WithContext(ctx))
}

func (n *helmRepositoryNetwork) lookup(ctx context.Context, host string) ([]net.IP, error) {
	if strings.Contains(host, "%") {
		return nil, fmt.Errorf("host %q contains an IPv6 zone", host)
	}
	if ip := net.ParseIP(host); ip != nil {
		if v4 := ip.To4(); v4 != nil {
			ip = v4
		}
		if n.blocked(ip) {
			return nil, fmt.Errorf("host %q resolved to blocked address %s", host, ip)
		}
		return []net.IP{ip}, nil
	}
	answers, err := n.resolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	if len(answers) == 0 {
		return nil, fmt.Errorf("host %q resolved to no addresses", host)
	}
	ips := make([]net.IP, 0, len(answers))
	for _, answer := range answers {
		if answer.Zone != "" || answer.IP == nil || answer.IP.To16() == nil {
			return nil, fmt.Errorf("host %q returned an invalid address", host)
		}
		ip := answer.IP
		if v4 := ip.To4(); v4 != nil {
			ip = v4
		}
		if n.blocked(ip) {
			return nil, fmt.Errorf("host %q resolved to blocked address %s", host, ip)
		}
		ips = append(ips, ip)
	}
	return ips, nil
}

func (n *helmRepositoryNetwork) blocked(ip net.IP) bool {
	if n.allowAddress != nil {
		return !n.allowAddress(ip)
	}
	return blockedHelmAddress(ip)
}

func blockedHelmAddress(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsUnspecified() || ip.IsMulticast() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.Equal(net.ParseIP("100.100.100.100")) || ip.Equal(net.ParseIP("fd00:ec2::254"))
}

func (n *helmRepositoryNetwork) dialContext(ctx context.Context, networkName, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, fmt.Errorf("invalid repository target %q: %w", address, err)
	}
	ips, err := n.lookup(ctx, host)
	if err != nil {
		return nil, err
	}
	route, _ := ctx.Value(helmProxyRouteKey{}).(*url.URL)
	var last error
	for _, ip := range ips {
		target := net.JoinHostPort(ip.String(), port)
		var conn net.Conn
		if route == nil {
			conn, err = n.dialer.DialContext(ctx, networkName, target)
		} else {
			conn, err = n.proxyDial(ctx, route, target)
		}
		if err == nil {
			return conn, nil
		}
		last = err
	}
	return nil, last
}

func (n *helmRepositoryNetwork) proxyDial(ctx context.Context, route *url.URL, target string) (net.Conn, error) {
	switch strings.ToLower(route.Scheme) {
	case "http", "https":
		host := route.Hostname()
		port := route.Port()
		scheme := strings.ToLower(route.Scheme)
		if port == "" {
			port = "80"
			if scheme == "https" {
				port = "443"
			}
		}
		if host == "" {
			return nil, fmt.Errorf("Helm proxy URL has no host")
		}
		conn, err := n.dialer.DialContext(ctx, "tcp", net.JoinHostPort(host, port))
		if err != nil {
			return nil, err
		}
		if scheme == "https" {
			cfg := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: host}
			tlsConn := tls.Client(conn, cfg)
			handshakeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			if err := tlsConn.HandshakeContext(handshakeCtx); err != nil {
				_ = conn.Close()
				return nil, err
			}
			conn = tlsConn
		}
		return connectHelmProxy(ctx, conn, route, target)
	case "socks5", "socks5h":
		var forward proxy.Dialer = contextDialAdapter{n.dialer}
		d, err := proxy.FromURL(route, forward)
		if err != nil {
			return nil, fmt.Errorf("unsupported Helm proxy %q: %w", route.Scheme, err)
		}
		cd, ok := d.(proxy.ContextDialer)
		if !ok {
			return nil, fmt.Errorf("Helm SOCKS5 proxy lacks context cancellation")
		}
		timeoutCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		return cd.DialContext(timeoutCtx, "tcp", target)
	default:
		return nil, fmt.Errorf("unsupported Helm proxy scheme %q", route.Scheme)
	}
}

type contextDialAdapter struct{ d helmDialer }

func (d contextDialAdapter) Dial(network, address string) (net.Conn, error) {
	return d.d.DialContext(context.Background(), network, address)
}
func (d contextDialAdapter) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	return d.d.DialContext(ctx, network, address)
}

func connectHelmProxy(ctx context.Context, conn net.Conn, route *url.URL, target string) (net.Conn, error) {
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-done:
		}
	}()
	defer close(done)
	deadline := time.Now().Add(10 * time.Second)
	if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
		deadline = ctxDeadline
	}
	_ = conn.SetDeadline(deadline)
	var b strings.Builder
	b.WriteString("CONNECT ")
	b.WriteString(target)
	b.WriteString(" HTTP/1.1\r\nHost: ")
	b.WriteString(target)
	b.WriteString("\r\n")
	if route.User != nil {
		password, _ := route.User.Password()
		auth := base64.StdEncoding.EncodeToString([]byte(route.User.Username() + ":" + password))
		b.WriteString("Proxy-Authorization: Basic ")
		b.WriteString(auth)
		b.WriteString("\r\n")
	}
	b.WriteString("\r\n")
	if _, err := io.WriteString(conn, b.String()); err != nil {
		_ = conn.Close()
		return nil, err
	}
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, &http.Request{Method: http.MethodConnect})
	if err != nil || resp.StatusCode != http.StatusOK {
		_ = conn.Close()
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("proxy refused CONNECT: %s", resp.Status)
	}
	_ = conn.SetDeadline(time.Time{})
	if br.Buffered() > 0 {
		return &helmBufferedConn{Conn: conn, reader: br}, nil
	}
	return conn, nil
}

type helmBufferedConn struct {
	net.Conn
	reader *bufio.Reader
}

func (c *helmBufferedConn) Read(p []byte) (int, error) { return c.reader.Read(p) }
