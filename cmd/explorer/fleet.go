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
		return nil
	}
	if catalogOnly || cfg.CloudTunnelConfigured || !cfg.MCPEnabled {
		return errors.New("Fleet requires enabled MCP and cannot use Cloud or catalog-only mode")
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
	manager, err := fleet.Open(configPath, statePath)
	if err != nil {
		return err
	}
	cfg.FleetManager = manager
	return nil
}
