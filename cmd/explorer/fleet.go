package main

import (
	"errors"
	"net"
	"os"
	"path/filepath"

	"github.com/skyhook-io/radar/internal/app"
	"github.com/skyhook-io/radar/internal/fleet"
)

func configureFleet(cfg *app.AppConfig, configPath, statePath string, catalogOnly bool) error {
	if configPath == "" {
		if statePath != "" {
			return errors.New("--fleet-state requires --fleet-config")
		}
		if cfg.FleetLocalConnections || cfg.FleetWrites || len(cfg.FleetAdminUsers) > 0 {
			return errors.New("Fleet management flags require --fleet-config")
		}
		return nil
	}
	if cfg.FleetWrites && cfg.MCPReadOnly {
		return errors.New("--fleet-writes conflicts with MCP read-only mode")
	}
	if catalogOnly || cfg.CloudTunnelConfigured || !cfg.MCPEnabled {
		return errors.New("Fleet requires enabled MCP and cannot use Cloud or catalog-only mode")
	}
	if cfg.FleetLocalConnections && (cfg.FakeInCluster || os.Getenv("KUBERNETES_SERVICE_HOST") != "") {
		return errors.New("--fleet-local-connections is unavailable in-cluster")
	}
	loopback := cfg.ListenAddress == "localhost" || net.ParseIP(cfg.ListenAddress).IsLoopback()
	if !loopback && !cfg.AuthConfig.Enabled() {
		return errors.New("Fleet on a shared listener requires Radar authentication")
	}
	if statePath == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return errors.New("cannot resolve Fleet state directory; set --fleet-state")
		}
		statePath = filepath.Join(home, ".radar", "fleet-state.json")
	}
	namespaceConfig := *cfg
	options := []fleet.ManagerOption{
		fleet.WithCandidateProvider(app.FleetCandidates),
		fleet.WithManagedNamespaceProvider(func() []string {
			return app.FleetManagedNamespaces(namespaceConfig)
		}),
	}
	manager, err := fleet.Open(configPath, statePath, options...)
	if err != nil {
		return err
	}
	cfg.FleetManager = manager
	cfg.FleetCandidateProvider = app.FleetCandidates
	return nil
}
