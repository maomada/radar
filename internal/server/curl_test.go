package server

import (
	"crypto/tls"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func testCurlClient(t *testing.T) *http.Client {
	t.Helper()
	client := curlDialClient()
	client.Transport.(*http.Transport).DialContext = (&net.Dialer{}).DialContext
	return client
}

// A curld Service must not be able to bounce the curl to a redirect target —
// don't chase a Service-controlled 3xx. curlDialClient must not follow redirects.
func TestCurlDialClientDoesNotFollowRedirects(t *testing.T) {
	var downstreamHits int32
	downstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&downstreamHits, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer downstream.Close()

	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, downstream.URL+"/elsewhere", http.StatusFound)
	}))
	defer proxy.Close()

	resp, err := testCurlClient(t).Get(proxy.URL + "/x")
	if err != nil {
		t.Fatalf("unexpected request error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusFound {
		t.Errorf("expected the 302 to be returned (not followed), got %d", resp.StatusCode)
	}
	if hits := atomic.LoadInt32(&downstreamHits); hits != 0 {
		t.Errorf("redirect was followed — downstream host hit %d time(s)", hits)
	}
}

func TestCurlDialClientBlocksLoopbackByDefault(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		hits.Add(1)
	}))
	defer server.Close()
	if _, err := curlDialClient().Get(server.URL); err == nil {
		t.Fatal("loopback request unexpectedly succeeded")
	}
	if got := hits.Load(); got != 0 {
		t.Fatalf("blocked curl reached server %d times", got)
	}
}

func TestCurlDialClientTLSVerification(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer server.Close()
	untrusted := testCurlClient(t)
	if _, err := untrusted.Get(server.URL); err == nil {
		t.Fatal("self-signed server unexpectedly trusted")
	}
	trusted := testCurlClient(t)
	trusted.Transport.(*http.Transport).TLSClientConfig = &tls.Config{
		MinVersion: tls.VersionTLS12,
		RootCAs:    server.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs,
	}
	resp, err := trusted.Get(server.URL)
	if err != nil {
		t.Fatalf("configured CA request failed: %v", err)
	}
	_ = resp.Body.Close()
}

func TestCurlNameValidation(t *testing.T) {
	valid := []string{"kube-system", "kube-dns", "my-svc", "a", "svc.with.dots", "argocd-server"}
	for _, s := range valid {
		if !curlNameRe.MatchString(s) {
			t.Errorf("expected %q to be a valid name", s)
		}
	}
	invalid := []string{"", "x/../../etc", "a:b", "UPPER", "-leading", "trailing-", "has space", "name/sub", "a%2f"}
	for _, s := range invalid {
		if curlNameRe.MatchString(s) {
			t.Errorf("expected %q to be rejected as a name", s)
		}
	}
}

func TestCurlPortValidation(t *testing.T) {
	valid := []string{"80", "9153", "443", "1", "65535"}
	for _, s := range valid {
		if !curlPortRe.MatchString(s) {
			t.Errorf("expected %q to be a valid port", s)
		}
	}
	// Direct dial needs a numeric host:port — named ports and anything that could
	// alter the dial target must be rejected.
	invalid := []string{"", "http", "web-ui", "80:proxy", "a/b", "8080a", "12.34", "123456"}
	for _, s := range invalid {
		if curlPortRe.MatchString(s) {
			t.Errorf("expected %q to be rejected as a port", s)
		}
	}
}
