package app

import (
	"context"
	"testing"

	"github.com/skyhook-io/radar/internal/portalagent"
)

func TestPortalReporterStartsForLocalAndInClusterModes(t *testing.T) {
	config := portalagent.Config{
		PortalURL: "https://portal.example.com", Token: "another-token-with-more-than-thirty-two-characters",
		ClusterID: "radar-instance", ClusterName: "Development", RadarURL: "http://127.0.0.1:9280",
		MCPBaseURL: "http://127.0.0.1:9280", AllowHTTP: true,
	}
	load := func() (portalagent.Config, bool, error) { return config, true, nil }
	snapshot := func() portalagent.Snapshot {
		return portalagent.Snapshot{RadarVersion: "dev", AuthMode: "none", ConnectionState: "connecting"}
	}
	for _, mode := range []string{"single", "in-cluster"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if !startPortalReporter(ctx, AppConfig{}, mode, load, snapshot) {
				t.Fatalf("reporter did not start for %q mode", mode)
			}
		})
	}
}

func TestPortalReporterDoesNotStartWhenConfigurationIsOmitted(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if startPortalReporter(ctx, AppConfig{}, "single",
		func() (portalagent.Config, bool, error) { return portalagent.Config{}, false, nil },
		func() portalagent.Snapshot { return portalagent.Snapshot{} }) {
		t.Fatal("reporter started without Portal configuration")
	}
}

func TestPortalReporterSkipsCloudBeforeParsingEnvironment(t *testing.T) {
	t.Setenv("RADAR_PORTAL_URL", "not-a-url")
	calls := 0
	load := func() (portalagent.Config, bool, error) {
		calls++
		return portalagent.ConfigFromEnv()
	}
	if startPortalReporter(context.Background(), AppConfig{CloudTunnelConfigured: true}, "in-cluster", load,
		func() portalagent.Snapshot { return portalagent.Snapshot{} }) {
		t.Fatal("reporter started for cloud tunnel mode")
	}
	if calls != 0 {
		t.Fatalf("portal environment was parsed %d times for cloud mode", calls)
	}
}
