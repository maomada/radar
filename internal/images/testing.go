package images

import "net/http"

// testRegistryTransport replaces the network guard for authenticated
// inspection, so tests in other packages can serve a fake registry.
var testRegistryTransport http.RoundTripper

// SetTestRegistryTransport makes authenticated image inspection send every
// registry request to rt and returns the previous value. Tests only.
func SetTestRegistryTransport(rt http.RoundTripper) http.RoundTripper {
	previous := testRegistryTransport
	testRegistryTransport = rt
	return previous
}
