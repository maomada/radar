package traffic

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestCarettaRejectsCrossOriginRedirects(t *testing.T) {
	var received atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(target.Close)
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	t.Cleanup(source.Close)
	c := NewCarettaSource(nil)
	c.prometheusAddr = source.URL
	c.headers = map[string]string{"Authorization": "Bearer secret", "X-API-Key": "api-secret", "X-Scope-OrgID": "tenant"}
	c.mu.Lock()
	connected := c.tryMetricsEndpointLocked(context.Background(), source.URL)
	hasSeries := c.hasSeriesLocked(context.Background(), source.URL, "up")
	c.mu.Unlock()
	if connected || hasSeries {
		t.Fatal("cross-origin redirect was accepted as a valid metrics source")
	}
	if _, err := c.queryPrometheusRaw(context.Background(), "up"); err == nil || !strings.Contains(err.Error(), "cross-origin redirect refused") {
		t.Fatalf("query error = %v, want refused redirect", err)
	}
	if received.Load() != 0 {
		t.Fatal("traffic consumer sent a request to the redirect destination")
	}
}

// Guards the parallel applyHeaders implementation in caretta.go against
// silent drift from internal/prometheus/client.go — a future contributor
// adding a 4th Prometheus call site here won't have a failing test to
// remind them if this one is missing.
func TestCarettaAppliesHeaders(t *testing.T) {
	var gotAuth, gotOrg atomic.Value
	gotAuth.Store("")
	gotOrg.Store("")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth.Store(r.Header.Get("Authorization"))
		gotOrg.Store(r.Header.Get("X-Scope-OrgID"))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[]}}`))
	}))
	defer srv.Close()

	c := &CarettaSource{
		httpClient:     &http.Client{Timeout: 5 * time.Second},
		prometheusAddr: srv.URL,
		headers: map[string]string{
			"Authorization": "Bearer caretta-token",
			"X-Scope-OrgID": "tenant-42",
		},
	}

	if _, err := c.queryPrometheusRaw(context.Background(), "up"); err != nil {
		t.Fatalf("queryPrometheusRaw failed: %v", err)
	}
	if got := gotAuth.Load().(string); got != "Bearer caretta-token" {
		t.Errorf("Authorization = %q, want %q", got, "Bearer caretta-token")
	}
	if got := gotOrg.Load().(string); got != "tenant-42" {
		t.Errorf("X-Scope-OrgID = %q, want %q", got, "tenant-42")
	}

	// tryMetricsEndpointLocked must carry the same headers, otherwise
	// discovery would 401 against an auth-protected endpoint and the user
	// would see "no metrics source found". Acquire the write lock first —
	// that's what every production caller does, and the function name's
	// "Locked" suffix is the Go convention saying so. A previous version
	// of applyHeaders took c.mu.RLock and deadlocked here.
	gotAuth.Store("")
	c.mu.Lock()
	ok := c.tryMetricsEndpointLocked(context.Background(), srv.URL)
	c.mu.Unlock()
	if !ok {
		t.Fatal("tryMetricsEndpointLocked returned false for healthy server")
	}
	if got := gotAuth.Load().(string); got != "Bearer caretta-token" {
		t.Errorf("probe Authorization = %q, want %q", got, "Bearer caretta-token")
	}
}

func TestCarettaNamespaceRemainsEncodedQueryParameter(t *testing.T) {
	var gotPath string
	var gotValues url.Values
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotValues = r.URL.Query()
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[]}}`))
	}))
	defer server.Close()
	c := &CarettaSource{httpClient: server.Client()}
	if _, err := c.queryPrometheusForFlows(context.Background(), server.URL, "/metrics/base", FlowOptions{Namespace: "tenant&query=override"}); err != nil {
		t.Fatalf("queryPrometheusForFlows: %v", err)
	}
	if gotPath != "/metrics/base/api/v1/query" {
		t.Fatalf("request path = %q", gotPath)
	}
	if len(gotValues) != 1 || gotValues.Get("query") != `caretta_links_observed{client_namespace="tenant&query=override"} or caretta_links_observed{server_namespace="tenant&query=override"}` {
		t.Fatalf("query parameters = %#v", gotValues)
	}
}
