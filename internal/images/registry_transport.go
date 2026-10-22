package images

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// errBlockedAddress marks a registry request refused by the network guard.
var errBlockedAddress = errors.New("registry address not allowed")

// maxRegistryResponseBytes bounds every response body read from a registry
// for a config lookup: manifests, indexes, token responses and the config
// blob. Real ones are kilobytes; this stops a hostile or broken registry from
// streaming an unbounded body into memory.
const maxRegistryResponseBytes = 4 << 20

type registryHostKey struct{}

// namedEndpoint is the registry a lookup is for, as the Pod named it. port is
// empty when the reference had none (the scheme's default port).
type namedEndpoint struct{ host, port string }

// withRegistryHost records the registry a lookup is for. The guard lets that
// endpoint, and only that endpoint, sit on a private or loopback address:
// internal registries are normal, but a redirect or token realm pointing
// somewhere else internal is not.
func withRegistryHost(ctx context.Context, registry string) context.Context {
	host, port := splitHostPort(registry)
	return context.WithValue(ctx, registryHostKey{}, namedEndpoint{host: host, port: port})
}

// isNamed reports whether host:port is the endpoint the lookup was for.
func isNamed(ctx context.Context, host, port string) bool {
	n, _ := ctx.Value(registryHostKey{}).(namedEndpoint)
	if n.host == "" || !strings.EqualFold(n.host, host) {
		return false
	}
	if n.port == "" {
		return port == "443" || port == "80"
	}
	return n.port == port
}

// guardedTransport is the transport runtime-version lookups use. Image
// references come from Pod specs, which anyone who can create a Pod
// controls, and Radar's network position differs from a node's. Every
// connection, including redirects and token endpoints, is checked against
// the resolved address it actually dials:
//   - link-local (cloud metadata), unspecified and multicast: never;
//   - loopback: never in-cluster (it is Radar's own Pod), and locally only
//     for the registry endpoint the Pod named (a local dev registry);
//   - private ranges: only for the registry endpoint (host and port) the Pod
//     named.
//
// Behind an HTTP proxy the proxy dials the destination, so the destination is
// resolved here first and refused if it resolves somewhere forbidden. A name
// that doesn't resolve here is refused in-cluster and left to the proxy
// locally.
type guardedTransport struct {
	inCluster bool
	proxy     func(*http.Request) (*url.URL, error)
	resolver  *net.Resolver
	base      *http.Transport
	dialer    *net.Dialer
}

// forLookup returns a transport with its own connection pool, for one
// lookup. Pools aren't shared across lookups: a connection one lookup opened
// to the registry it named (maybe on a private address) must never carry
// another lookup's request, even after that name resolves elsewhere.
func (g *guardedTransport) forLookup() *guardedTransport {
	return newGuardedTransportWith(g.inCluster, g.proxy, g.resolver)
}

func newGuardedTransport(inCluster bool) *guardedTransport {
	return newGuardedTransportWith(inCluster, http.ProxyFromEnvironment, net.DefaultResolver)
}

func newGuardedTransportWith(inCluster bool, proxy func(*http.Request) (*url.URL, error), resolver *net.Resolver) *guardedTransport {
	g := &guardedTransport{
		inCluster: inCluster,
		proxy:     proxy,
		resolver:  resolver,
		dialer:    &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second},
	}
	g.base = &http.Transport{
		Proxy:                 func(r *http.Request) (*url.URL, error) { return g.proxy(r) },
		DialContext:           g.dialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          50,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 15 * time.Second,
		ExpectContinueTimeout: time.Second,
	}
	return g
}

type proxyHostKey struct{}

func (g *guardedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	ctx := req.Context()
	host, port := req.URL.Hostname(), req.URL.Port()
	if port == "" {
		port = map[string]string{"https": "443", "http": "80"}[req.URL.Scheme]
	}
	named := isNamed(ctx, host, port)
	proxyURL, err := g.proxy(req)
	proxied := err == nil && proxyURL != nil

	// Checked per request, not only when dialing: a pooled connection to an
	// endpoint one lookup named must not serve another lookup's redirect.
	ips, err := g.lookup(ctx, host)
	switch {
	case err != nil && proxied && !g.inCluster:
		// Locally the proxy is the user's own; a name only it can resolve
		// is left to it.
	case err != nil && proxied:
		return nil, fmt.Errorf("%w: %s can't be checked before the proxy resolves it", errBlockedAddress, host)
	case err != nil:
		return nil, err
	}
	for _, ip := range ips {
		if !registryIPAllowed(ip, g.inCluster, named) {
			return nil, fmt.Errorf("%w: %s", errBlockedAddress, host)
		}
	}
	if proxied {
		// The proxy resolves the name again; that second answer can't be
		// pinned, which is the residual risk of running behind a proxy.
		req = req.WithContext(context.WithValue(ctx, proxyHostKey{}, hostOnly(proxyURL.Host)))
	}
	resp, err := g.base.RoundTrip(req)
	if err != nil || resp.Body == nil {
		return resp, err
	}
	resp.Body = &cappedBody{ReadCloser: resp.Body, left: maxRegistryResponseBytes}
	return resp, nil
}

func (g *guardedTransport) dialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	// The proxy itself is configuration, not tenant input.
	if proxyHost, _ := ctx.Value(proxyHostKey{}).(string); proxyHost != "" && strings.EqualFold(proxyHost, host) {
		return g.dialer.DialContext(ctx, network, addr)
	}
	ips, err := g.lookup(ctx, host)
	if err != nil {
		return nil, err
	}
	named := isNamed(ctx, host, port)
	// Dial the address that was checked, not the name, so a second DNS
	// answer can't swap in a forbidden address.
	lastErr := fmt.Errorf("%w: %s", errBlockedAddress, host)
	for _, ip := range ips {
		if !registryIPAllowed(ip, g.inCluster, named) {
			continue
		}
		conn, err := g.dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
		if err == nil {
			return conn, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

func (g *guardedTransport) lookup(ctx context.Context, host string) ([]net.IP, error) {
	if ip := net.ParseIP(host); ip != nil {
		return []net.IP{ip}, nil
	}
	addrs, err := g.resolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	ips := make([]net.IP, 0, len(addrs))
	for _, a := range addrs {
		ips = append(ips, a.IP)
	}
	return ips, nil
}

// registryIPAllowed is the address policy; see guardedTransport.
func registryIPAllowed(ip net.IP, inCluster, namedRegistry bool) bool {
	switch {
	case ip == nil, ip.IsLinkLocalUnicast(), ip.IsLinkLocalMulticast(), ip.IsUnspecified(), ip.IsMulticast():
		return false
	case ip.IsLoopback():
		return !inCluster && namedRegistry
	case ip.IsPrivate(), isSharedAddressSpace(ip):
		return namedRegistry
	}
	return true
}

// isSharedAddressSpace reports RFC 6598 carrier-grade NAT space (100.64/10),
// which net.IP.IsPrivate leaves out and some clusters use for Pods.
func isSharedAddressSpace(ip net.IP) bool {
	v4 := ip.To4()
	return v4 != nil && v4[0] == 100 && v4[1]&0xc0 == 64
}

func hostOnly(hostport string) string {
	host, _ := splitHostPort(hostport)
	return host
}

// splitHostPort splits "host", "host:port", "[v6]" or "[v6]:port".
func splitHostPort(hostport string) (host, port string) {
	if h, p, err := net.SplitHostPort(hostport); err == nil {
		return h, p
	}
	return strings.TrimSuffix(strings.TrimPrefix(hostport, "["), "]"), ""
}

type cappedBody struct {
	io.ReadCloser
	left int64
}

func (b *cappedBody) Read(p []byte) (int, error) {
	if b.left <= 0 {
		// At the cap: a body that ends exactly here is fine.
		var one [1]byte
		if n, err := b.ReadCloser.Read(one[:]); n == 0 && err == io.EOF {
			return 0, io.EOF
		}
		return 0, fmt.Errorf("registry response exceeds %d bytes", maxRegistryResponseBytes)
	}
	if int64(len(p)) > b.left {
		p = p[:b.left]
	}
	n, err := b.ReadCloser.Read(p)
	b.left -= int64(n)
	return n, err
}

// isBlocked reports whether err came from the network guard.
func isBlocked(err error) bool {
	return errors.Is(err, errBlockedAddress)
}
