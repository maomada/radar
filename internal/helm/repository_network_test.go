package helm

import (
	"bufio"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"helm.sh/helm/v3/pkg/repo"
)

type helmResolverFunc func(context.Context, string) ([]net.IPAddr, error)

func (f helmResolverFunc) LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error) {
	return f(ctx, host)
}

type helmDialFunc func(context.Context, string, string) (net.Conn, error)

func (f helmDialFunc) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	return f(ctx, network, address)
}

func testHelmRepositoryNetwork() *helmRepositoryNetwork {
	return &helmRepositoryNetwork{
		resolver: net.DefaultResolver,
		dialer:   &net.Dialer{Timeout: 2 * time.Second},
		proxy:    func(*http.Request) (*url.URL, error) { return nil, nil },
		allowAddress: func(ip net.IP) bool {
			return !blockedHelmAddress(ip) || ip.IsLoopback()
		},
	}
}

func testRepositoryGetter(entry repo.Entry) repositoryHTTPGetter {
	return repositoryHTTPGetter{entry: entry, network: testHelmRepositoryNetwork()}
}

func withTestHelmHTTPClient(t *testing.T) {
	t.Helper()
	original := httpClient
	client := *original
	client.Transport = newHelmGuardedTransport(&http.Transport{
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 15 * time.Second,
	}, testHelmRepositoryNetwork())
	httpClient = &client
	t.Cleanup(func() { httpClient = original })
}

func TestHelmRepositoryNetworkBlocksRestrictedAddressesBeforeDial(t *testing.T) {
	blocked := []string{
		"127.0.0.1", "::1", "0.0.0.0", "::", "224.0.0.1", "ff02::1", "ff0e::1",
		"169.254.1.1", "fe80::1", "fe80::1%eth0", "100.100.100.100",
		"fd00:ec2::254", "::ffff:127.0.0.1", "::ffff:169.254.169.254",
	}
	for _, address := range blocked {
		t.Run(address, func(t *testing.T) {
			var dials int
			n := &helmRepositoryNetwork{
				resolver: helmResolverFunc(func(context.Context, string) ([]net.IPAddr, error) {
					t.Fatal("literal address unexpectedly resolved")
					return nil, nil
				}),
				dialer: helmDialFunc(func(context.Context, string, string) (net.Conn, error) {
					dials++
					return nil, errors.New("should not dial")
				}),
				proxy: func(*http.Request) (*url.URL, error) { return nil, nil },
			}
			if _, err := n.dialContext(context.Background(), "tcp", net.JoinHostPort(address, "80")); err == nil {
				t.Fatal("restricted destination unexpectedly accepted")
			}
			if dials != 0 {
				t.Fatalf("restricted destination dialed %d times", dials)
			}
		})
	}
}

func TestHelmRepositoryDefaultTransportBlocksLocalhostWithoutRequest(t *testing.T) {
	var hits int
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		hits++
	}))
	defer server.Close()
	client := &http.Client{Transport: newHelmGuardedTransport(&http.Transport{}, defaultHelmRepositoryNetwork)}
	if _, err := client.Get(server.URL); err == nil {
		t.Fatal("localhost repository request unexpectedly succeeded")
	}
	if hits != 0 {
		t.Fatalf("blocked localhost received %d requests", hits)
	}
}

func TestHelmRepositoryNetworkFailsClosedOnMixedDNSAnswers(t *testing.T) {
	var dials int
	n := &helmRepositoryNetwork{
		resolver: helmResolverFunc(func(context.Context, string) ([]net.IPAddr, error) {
			return []net.IPAddr{{IP: net.ParseIP("10.0.0.10")}, {IP: net.ParseIP("127.0.0.1")}}, nil
		}),
		dialer: helmDialFunc(func(context.Context, string, string) (net.Conn, error) {
			dials++
			return nil, errors.New("should not dial")
		}),
		proxy: func(*http.Request) (*url.URL, error) { return nil, nil },
	}
	if _, err := n.dialContext(context.Background(), "tcp", "mixed.test:80"); err == nil {
		t.Fatal("mixed DNS answer unexpectedly accepted")
	}
	if dials != 0 {
		t.Fatalf("mixed DNS answer dialed %d times", dials)
	}
}

func TestHelmRepositoryNetworkFailsClosedOnEmptyOrInvalidDNSAnswers(t *testing.T) {
	for _, tc := range []struct {
		name    string
		answers []net.IPAddr
	}{
		{name: "empty"},
		{name: "invalid", answers: []net.IPAddr{{IP: net.IP{}}}},
		{name: "zoned", answers: []net.IPAddr{{IP: net.ParseIP("fe80::1"), Zone: "eth0"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var dials int
			n := &helmRepositoryNetwork{
				resolver: helmResolverFunc(func(context.Context, string) ([]net.IPAddr, error) {
					return tc.answers, nil
				}),
				dialer: helmDialFunc(func(context.Context, string, string) (net.Conn, error) {
					dials++
					return nil, errors.New("should not dial")
				}),
				proxy: func(*http.Request) (*url.URL, error) { return nil, nil },
			}
			if _, err := n.dialContext(context.Background(), "tcp", "empty.test:80"); err == nil {
				t.Fatal("invalid DNS answers unexpectedly accepted")
			}
			if dials != 0 {
				t.Fatalf("invalid DNS answers dialed %d times", dials)
			}
		})
	}
}

func TestHelmRepositoryNetworkDialsCheckedPrivateLiteral(t *testing.T) {
	for _, privateIP := range []string{"10.20.30.40", "100.64.0.1"} {
		t.Run(privateIP, func(t *testing.T) {
			var got string
			n := &helmRepositoryNetwork{
				resolver: helmResolverFunc(func(context.Context, string) ([]net.IPAddr, error) {
					return []net.IPAddr{{IP: net.ParseIP(privateIP)}}, nil
				}),
				dialer: helmDialFunc(func(_ context.Context, _, address string) (net.Conn, error) {
					got = address
					return nil, errors.New("stop after observing checked address")
				}),
				proxy: func(*http.Request) (*url.URL, error) { return nil, nil },
			}
			_, _ = n.dialContext(context.Background(), "tcp", "repo.test:8080")
			if got != privateIP+":8080" {
				t.Fatalf("dial target = %q, want checked literal", got)
			}
		})
	}
}

func TestHelmRepositoryNetworkRedirectToBlockedHostDoesNotDial(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://blocked.test:80/private", http.StatusFound)
	}))
	defer server.Close()
	_, port, _ := net.SplitHostPort(strings.TrimPrefix(server.URL, "http://"))
	var mu sync.Mutex
	var dials []string
	n := &helmRepositoryNetwork{
		resolver: helmResolverFunc(func(_ context.Context, host string) ([]net.IPAddr, error) {
			if host == "safe.test" {
				return []net.IPAddr{{IP: net.ParseIP("192.0.2.10")}}, nil
			}
			return []net.IPAddr{{IP: net.ParseIP("169.254.169.254")}}, nil
		}),
		dialer: helmDialFunc(func(ctx context.Context, _, address string) (net.Conn, error) {
			mu.Lock()
			dials = append(dials, address)
			mu.Unlock()
			if address != "192.0.2.10:"+port {
				return nil, fmt.Errorf("unexpected dial %s", address)
			}
			return (&net.Dialer{}).DialContext(ctx, "tcp", "127.0.0.1:"+port)
		}),
		proxy: func(*http.Request) (*url.URL, error) { return nil, nil },
	}
	client := &http.Client{Transport: newHelmGuardedTransport(&http.Transport{}, n)}
	resp, err := client.Get("http://safe.test:" + port + "/")
	if resp != nil {
		_ = resp.Body.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "blocked address") {
		t.Fatalf("redirect to blocked destination error = %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(dials) != 1 || dials[0] != "192.0.2.10:"+port {
		t.Fatalf("dial attempts = %v, want only checked initial destination", dials)
	}
}

func TestHelmRepositoryHTTPProxyCONNECTPinsTargetAndSeparatesCredentials(t *testing.T) {
	clientConn, proxyConn := net.Pipe()
	defer clientConn.Close()
	defer proxyConn.Close()
	proxyURL, _ := url.Parse("http://proxy-user:proxy-pass@proxy.test:8080")
	n := &helmRepositoryNetwork{
		resolver: helmResolverFunc(func(context.Context, string) ([]net.IPAddr, error) {
			return []net.IPAddr{{IP: net.ParseIP("10.20.30.40")}}, nil
		}),
		dialer: helmDialFunc(func(context.Context, string, string) (net.Conn, error) {
			return clientConn, nil
		}),
		proxy: func(*http.Request) (*url.URL, error) { return proxyURL, nil },
	}
	wantProxyAuth := "Basic " + base64.StdEncoding.EncodeToString([]byte("proxy-user:proxy-pass"))
	done := make(chan error, 1)
	go func() {
		reader := bufio.NewReader(proxyConn)
		connect, err := http.ReadRequest(reader)
		if err != nil {
			done <- err
			return
		}
		if connect.Method != http.MethodConnect || connect.URL.Host != "10.20.30.40:80" {
			done <- fmt.Errorf("CONNECT target = %s %s", connect.Method, connect.URL.Host)
			return
		}
		if connect.Header.Get("Proxy-Authorization") != wantProxyAuth || connect.Header.Get("Authorization") != "" {
			done <- fmt.Errorf("unexpected CONNECT credentials: %#v", connect.Header)
			return
		}
		_, _ = io.WriteString(proxyConn, "HTTP/1.1 200 Connection Established\r\n\r\n")
		origin, err := http.ReadRequest(reader)
		if err != nil {
			done <- err
			return
		}
		if origin.Header.Get("Authorization") != "Bearer origin-secret" || origin.Header.Get("Proxy-Authorization") != "" {
			done <- fmt.Errorf("unexpected tunneled origin credentials: %#v", origin.Header)
			return
		}
		_, _ = io.WriteString(proxyConn, "HTTP/1.1 200 OK\r\nContent-Length: 2\r\nConnection: close\r\n\r\nok")
		done <- nil
	}()
	req, _ := http.NewRequest(http.MethodGet, "http://origin.test/resource", nil)
	req.Header.Set("Authorization", "Bearer origin-secret")
	resp, err := (&http.Client{Transport: newHelmGuardedTransport(&http.Transport{}, n)}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
