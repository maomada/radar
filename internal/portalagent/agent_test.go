package portalagent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/portal"
)

const agentToken = "another-token-with-more-than-thirty-two-characters"

func testAgentConfig(portalURL string) Config {
	return Config{
		PortalURL: portalURL, Token: agentToken, ClusterID: "prod-east", ClusterName: "Production East",
		RadarURL: "https://radar.example.com/radar", MCPBaseURL: "https://radar.example.com/radar",
		AllowHTTP: true,
	}
}

func TestReporterUsesEffectiveMCPModeAndBasePath(t *testing.T) {
	tests := []struct {
		name     string
		snapshot Snapshot
		mode     string
		url      string
		readOnly string
	}{
		{name: "full", snapshot: Snapshot{RadarVersion: "1.2.3", ConnectionState: "connected", AuthMode: "oidc", RuntimeMode: "in-cluster", KubeContext: "prod-east", ClusterName: "prod-east-cluster", MCPEnabled: true}, mode: "full", url: "https://radar.example.com/radar/mcp", readOnly: "https://radar.example.com/radar/mcp-readonly"},
		{name: "read only", snapshot: Snapshot{RadarVersion: "1.2.3", ConnectionState: "connecting", AuthMode: "proxy", RuntimeMode: "local", KubeContext: "dev-context", ClusterName: "dev-cluster", MCPEnabled: true, MCPReadOnly: true}, mode: "read-only", readOnly: "https://radar.example.com/radar/mcp-readonly"},
		{name: "disabled", snapshot: Snapshot{RadarVersion: "1.2.3", ConnectionState: "disconnected", AuthMode: "none", RuntimeMode: "local"}, mode: "disabled"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reporter, err := New(testAgentConfig("https://portal.example.com"), func() Snapshot { return tt.snapshot })
			if err != nil {
				t.Fatal(err)
			}
			registration := reporter.registration(tt.snapshot)
			if registration.MCP.Mode != tt.mode || registration.MCP.URL != tt.url || registration.MCP.ReadOnlyURL != tt.readOnly {
				t.Fatalf("MCP registration = %+v", registration.MCP)
			}
			if registration.RadarVersion != tt.snapshot.RadarVersion || registration.ConnectionState != tt.snapshot.ConnectionState || registration.AuthMode != tt.snapshot.AuthMode {
				t.Fatalf("snapshot metadata = %+v", registration)
			}
			if registration.RuntimeMode != tt.snapshot.RuntimeMode ||
				registration.KubeContext != tt.snapshot.KubeContext ||
				registration.ClusterName != tt.snapshot.ClusterName {
				t.Fatalf("runtime metadata = %+v", registration)
			}
			if err := portal.ValidateRegistration(registration); err != nil {
				t.Fatalf("server rejected generated payload: %v", err)
			}
		})
	}
}

func TestReporterAllowsSeparateMCPBaseURL(t *testing.T) {
	config := testAgentConfig("https://portal.example.com")
	config.MCPBaseURL = "https://mcp.example.com/radar"
	reporter, err := New(config, func() Snapshot {
		return Snapshot{RadarVersion: "1.2.3", ConnectionState: "connected", AuthMode: "none", RuntimeMode: "local", MCPEnabled: true}
	})
	if err != nil {
		t.Fatal(err)
	}
	registration := reporter.registration(reporter.snapshot())
	if registration.MCP.URL != "https://mcp.example.com/radar/mcp" {
		t.Fatalf("MCP URL = %q", registration.MCP.URL)
	}
	if err := portal.ValidateRegistration(registration); err != nil {
		t.Fatalf("server rejected configured MCP base URL: %v", err)
	}
}

func TestPortalURLMustUseRootPath(t *testing.T) {
	if _, err := New(testAgentConfig("https://portal.example.com/portal"), func() Snapshot { return Snapshot{} }); err == nil {
		t.Fatal("Portal URL with a subpath was accepted")
	}
	if _, err := New(testAgentConfig("https://portal.example.com/"), func() Snapshot { return Snapshot{} }); err != nil {
		t.Fatalf("root Portal URL was rejected: %v", err)
	}
}

func TestReporterTracksCurrentContextWithStableInstanceIdentity(t *testing.T) {
	config := testAgentConfig("https://portal.example.com")
	contexts := []Snapshot{
		{RadarVersion: "1.2.3", ConnectionState: "connected", AuthMode: "none", RuntimeMode: "local", KubeContext: "dev", ClusterName: "dev-cluster"},
		{RadarVersion: "1.2.3", ConnectionState: "connecting", AuthMode: "none", RuntimeMode: "local", KubeContext: "prod", ClusterName: "prod-cluster"},
	}
	current := 0
	reporter, err := New(config, func() Snapshot { return contexts[current] })
	if err != nil {
		t.Fatal(err)
	}
	first := reporter.registration(reporter.snapshot())
	current = 1
	second := reporter.registration(reporter.snapshot())
	if first.ID != second.ID || first.Name != second.Name || first.ID != config.ClusterID || first.Name != config.ClusterName {
		t.Fatalf("context change altered Portal instance identity: first=%+v second=%+v", first, second)
	}
	if first.KubeContext != "dev" || first.ClusterName != "dev-cluster" ||
		second.KubeContext != "prod" || second.ClusterName != "prod-cluster" {
		t.Fatalf("registrations did not track current context: first=%+v second=%+v", first, second)
	}
}

func TestReporterRetriesUntilCanceled(t *testing.T) {
	var calls atomic.Int32
	received := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer "+agentToken {
			t.Errorf("authorization header did not contain configured token")
		}
		var registration portal.Registration
		if err := json.NewDecoder(r.Body).Decode(&registration); err != nil {
			t.Errorf("decode registration: %v", err)
		}
		if registration.ID != "prod-east" {
			t.Errorf("registration ID = %q", registration.ID)
		}
		if calls.Add(1) == 1 {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		select {
		case received <- struct{}{}:
		default:
		}
	}))
	defer server.Close()

	reporter, err := New(testAgentConfig(server.URL), func() Snapshot {
		return Snapshot{RadarVersion: "1.2.3", ConnectionState: "connected", AuthMode: "none", RuntimeMode: "local", MCPEnabled: true}
	})
	if err != nil {
		t.Fatal(err)
	}
	reporter.interval = 10 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	errs := make(chan error, 2)
	done := make(chan struct{})
	go func() {
		reporter.Run(ctx, func(err error) {
			errs <- err
		})
		close(done)
	}()
	select {
	case <-received:
	case <-time.After(time.Second):
		cancel()
		t.Fatal("reporter did not retry")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("reporter did not stop after cancellation")
	}
	if calls.Load() < 2 {
		t.Fatalf("registration attempts = %d, want at least 2", calls.Load())
	}
	select {
	case err := <-errs:
		if strings.Contains(err.Error(), agentToken) {
			t.Fatal("error leaked the registration token")
		}
	default:
	}
}

func TestReporterDoesNotFollowRedirects(t *testing.T) {
	var targetCalls atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		targetCalls.Add(1)
	}))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer redirect.Close()
	reporter, err := New(testAgentConfig(redirect.URL), func() Snapshot {
		return Snapshot{RadarVersion: "1.2.3", ConnectionState: "connected", AuthMode: "none", RuntimeMode: "local", MCPEnabled: true}
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := reporter.Register(context.Background()); err == nil || !strings.Contains(err.Error(), "HTTP 302") {
		t.Fatalf("Register error = %v, want HTTP 302", err)
	}
	if targetCalls.Load() != 0 {
		t.Fatal("reporter followed a redirect and disclosed credentials to another host")
	}
}

func TestConfigFromEnvDefaultsMCPBaseURL(t *testing.T) {
	t.Setenv("RADAR_PORTAL_URL", "https://portal.example.com")
	t.Setenv("RADAR_PORTAL_TOKEN", agentToken)
	t.Setenv("RADAR_PORTAL_CLUSTER_ID", "prod-east")
	t.Setenv("RADAR_PORTAL_CLUSTER_NAME", "Production East")
	t.Setenv("RADAR_PORTAL_RADAR_URL", "https://radar.example.com/prefix")
	t.Setenv("RADAR_PORTAL_MCP_BASE_URL", "")
	t.Setenv("RADAR_PORTAL_ALLOW_HTTP", "")
	config, enabled, err := ConfigFromEnv()
	if err != nil || !enabled {
		t.Fatalf("ConfigFromEnv enabled=%v err=%v", enabled, err)
	}
	if config.MCPBaseURL != config.RadarURL {
		t.Fatalf("MCP base URL = %q, want Radar URL %q", config.MCPBaseURL, config.RadarURL)
	}
}
