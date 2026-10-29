package main

import (
	"testing"

	"github.com/skyhook-io/radar/internal/app"
)

func TestFleetStartupBoundaries(t *testing.T) {
	t.Setenv("KUBERNETES_SERVICE_HOST", "")
	for _, tt := range []struct {
		name      string
		config    string
		state     string
		cfg       app.AppConfig
		catalog   bool
		wantError bool
	}{
		{name: "disabled unchanged"},
		{name: "orphan state", state: "state.json", wantError: true},
		{name: "admin users need config", cfg: app.AppConfig{FleetAdminUsers: []string{"alice"}}, wantError: true},
		{name: "local connections need config", cfg: app.AppConfig{FleetLocalConnections: true}, wantError: true},
		{name: "disabled MCP", config: "unused.json", cfg: app.AppConfig{ListenAddress: "127.0.0.1"}, wantError: true},
		{name: "shared anonymous", config: "unused.json", cfg: app.AppConfig{ListenAddress: "0.0.0.0", MCPEnabled: true}, wantError: true},
		{name: "cloud", config: "unused.json", cfg: app.AppConfig{ListenAddress: "127.0.0.1", MCPEnabled: true, CloudTunnelConfigured: true}, wantError: true},
		{name: "catalog", config: "unused.json", cfg: app.AppConfig{ListenAddress: "127.0.0.1", MCPEnabled: true}, catalog: true, wantError: true},
		{name: "local connections forbidden in cluster", config: "unused.json", cfg: app.AppConfig{ListenAddress: "127.0.0.1", MCPEnabled: true, FakeInCluster: true, FleetLocalConnections: true}, wantError: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := configureFleet(&tt.cfg, tt.config, tt.state, tt.catalog)
			if (err != nil) != tt.wantError || tt.cfg.FleetManager != nil {
				t.Fatalf("startup boundary = %v, manager=%v", err, tt.cfg.FleetManager)
			}
		})
	}
}
