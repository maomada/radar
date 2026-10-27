package app

import (
	"context"
	"log"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/portalagent"
)

func StartPortalReporter(ctx context.Context, cfg AppConfig) {
	mode := k8s.GetKubeconfigSummary().Mode
	startPortalReporter(ctx, cfg, mode, portalagent.ConfigFromEnv, func() portalagent.Snapshot {
		status := k8s.GetConnectionStatus()
		connectionState := string(status.State)
		if connectionState != "connected" && connectionState != "connecting" && connectionState != "disconnected" {
			connectionState = "connecting"
		}
		authMode := cfg.AuthConfig.Mode
		if authMode == "" {
			authMode = "none"
		}
		return portalagent.Snapshot{
			RadarVersion: cfg.Version, ConnectionState: connectionState, AuthMode: authMode,
			KubeContext: status.Context, ClusterName: status.ClusterName,
			MCPEnabled: cfg.MCPEnabled, MCPReadOnly: cfg.MCPReadOnly,
		}
	})
}

func startPortalReporter(
	ctx context.Context,
	cfg AppConfig,
	mode string,
	loadConfig func() (portalagent.Config, bool, error),
	snapshot func() portalagent.Snapshot,
) bool {
	if cfg.CloudTunnelConfigured {
		return false
	}
	reporterConfig, enabled, err := loadConfig()
	if err != nil {
		log.Printf("[portal] registration disabled: invalid configuration")
		return false
	}
	if !enabled {
		return false
	}
	runtimeMode := "local"
	if mode == "in-cluster" {
		runtimeMode = "in-cluster"
	}
	reporter, err := portalagent.New(reporterConfig, func() portalagent.Snapshot {
		current := snapshot()
		current.RuntimeMode = runtimeMode
		return current
	})
	if err != nil {
		log.Printf("[portal] registration disabled: invalid configuration")
		return false
	}
	log.Printf("[portal] cluster registration enabled")
	go reporter.Run(ctx, func(err error) {
		log.Printf("[portal] registration attempt failed: %v", err)
	})
	return true
}
