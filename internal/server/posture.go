package server

import (
	"net/http"

	"github.com/skyhook-io/radar/internal/cloud"
	"github.com/skyhook-io/radar/internal/k8s"
)

// This file holds every decision that depends on how Radar is deployed: hosted
// Radar Cloud, an OSS Radar connected to Radar Hub through the tunnel, auth on
// or off, and a loopback or shared listener. The rules are in
// docs/authentication.md#without-authentication; testdata/posture_matrix.json
// pins each decision for every combination of its inputs.
//
// Code elsewhere in this package reads the listener and the tunnel marker only
// through here (TestPostureFactsReadOnlyHere).

type listenerExposure int

const (
	// listenerUnstarted is a Server built by New and never started: tests and
	// direct Handler() users. Start replaces "" with 127.0.0.1, and the CLI
	// and Desktop pass a normalized address, so production never has it.
	listenerUnstarted listenerExposure = iota
	listenerLoopback
	listenerShared
)

func exposureOf(listenAddress string) listenerExposure {
	switch {
	case listenAddress == "":
		return listenerUnstarted
	case cloud.IsLoopbackHostname(listenAddress):
		return listenerLoopback
	default:
		return listenerShared
	}
}

// posture holds the deployment facts that are cheap to read on every request.
// Facts that need the kubeconfig summary (deployment mode, config ownership)
// are read only by the decisions that use them.
type posture struct {
	hostedCloud      bool // Radar Cloud: the app is served only over the tunnel
	tunnelConfigured bool // an OSS Radar connected to Radar Hub (--cloud-url)
	auth             bool
	listener         listenerExposure
}

func (s *Server) posture() posture {
	return posture{
		hostedCloud:      cloudMode(),
		tunnelConfigured: s.cloudConnectCfg.CloudTunnelConfigured,
		auth:             s.authConfig.Enabled(),
		listener:         exposureOf(s.listenAddress),
	}
}

// viaTunnel reports whether r arrived over the authenticated Radar Hub tunnel.
func viaTunnel(r *http.Request) bool {
	return cloud.IsAuthenticatedTunnelRequest(r.Context())
}

type hostCheckMode int

const (
	hostCheckNone hostCheckMode = iota
	hostCheckLoopbackOnly
	hostCheckSharedAllowlist
)

// hostCheck decides how protectUnauthenticatedHost treats r. Authenticated
// deployments and the tunnel have their own boundary. An unstarted server is
// exempt, because tests send httptest's example.com Host.
func (s *Server) hostCheck(r *http.Request) hostCheckMode {
	p := s.posture()
	if p.listener == listenerUnstarted || p.auth || viaTunnel(r) {
		return hostCheckNone
	}
	if p.listener == listenerLoopback {
		return hostCheckLoopbackOnly
	}
	return hostCheckSharedAllowlist
}

// localTerminalUnavailable says why the host local terminal, which runs as
// Radar's operating-system user and can't be impersonated per caller, is
// refused, or returns 0 when it may open. It needs a loopback listener and a
// loopback Host; an unstarted server counts as shared.
func (s *Server) localTerminalUnavailable(r *http.Request) (int, string) {
	p := s.posture()
	if k8s.ForceDisableLocalTerminal {
		return http.StatusForbidden, "local terminal is disabled"
	}
	if p.auth {
		return http.StatusForbidden, "local terminal is unavailable when authentication is enabled"
	}
	if viaTunnel(r) {
		return http.StatusForbidden, "local terminal is unavailable over the Radar Hub tunnel"
	}
	if k8s.IsInCluster() {
		return http.StatusBadRequest, "local terminal not available in-cluster mode"
	}
	if p.listener != listenerLoopback || !requestHostIsLoopback(r) {
		return http.StatusForbidden, "local terminal is only available over a loopback address"
	}
	return 0, ""
}

// pprofEnabled decides whether /debug/pprof is mounted. Profiles describe the
// Radar process, so an unauthenticated shared listener needs RADAR_PPROF=true;
// a local or authenticated install keeps them on, where restarting to turn
// them on could erase a leak or stuck state. Radar Cloud never mounts them.
func (s *Server) pprofEnabled() bool {
	p := s.posture()
	if p.hostedCloud {
		return false
	}
	if s.enablePprof != nil {
		return *s.enablePprof
	}
	return p.auth || p.listener != listenerShared
}

// pprofCmdlineAllowed: process arguments can hold session keys, OIDC client
// secrets or authorization headers, which signed-in users must not read.
func (s *Server) pprofCmdlineAllowed() bool {
	return !s.posture().auth
}

// cloudConnectDriverEnabled is the security boundary for the driver lane: a
// local process, no auth, and no existing tunnel. Anyone who can reach an
// unauthenticated Radar already wields the server's kubeconfig through
// /api/resources/apply and pods/exec — strictly more power than installing a
// chart — so gating this lane on the listener address too would hold the
// weaker capability to a higher bar than the stronger ones. A non-loopback
// listener is surfaced as an exposure note on the plan card instead
// (cloudConnectNeedsSharedAck); mutating endpoints still require a
// same-origin request.
func (s *Server) cloudConnectDriverEnabled() bool {
	p := s.posture()
	return !p.hostedCloud &&
		cloudConnectDeploymentMode() == k8s.DeploymentModeLocal &&
		!p.auth &&
		!p.tunnelConfigured
}

// cloudConnectNeedsSharedAck reports whether Radar answers beyond loopback,
// meaning someone other than the operator could reach the approval flow.
func (s *Server) cloudConnectNeedsSharedAck() bool {
	return s.posture().listener != listenerLoopback
}

// aiDeploymentSupported reports whether this Radar can run local AI
// investigations at all, whatever is installed. The engine drives the CLI
// against this server's own investigation MCP mount with no credentials, which
// only works when MCP is mounted and unauthenticated. Under proxy/OIDC auth
// (team and cloud deployments) the MCP requires identity headers a local CLI
// can't supply, and investigations are the embedding host's job (e.g. Radar
// Hub). An operator-managed shared installation is never a user's own machine.
func (s *Server) aiDeploymentSupported() bool {
	return s.configManagement() != "operator" && !s.posture().auth &&
		s.mcpInvestigationHandler != nil && s.aiInvestigationRefs != nil
}

// usageShared reports whether several people use this Radar: in-cluster,
// behind sign-in, or on a listener others can reach. Its configuration then
// decides for everyone, and nobody is asked.
func (s *Server) usageShared() bool {
	return deploymentMode() != k8s.DeploymentModeLocal ||
		s.configManagement() != "local" ||
		s.posture().listener != listenerLoopback
}

// unauthenticatedListenerWarning decides the startup warning that anyone who
// reaches the listener can use Radar. Radar Cloud's listener serves only
// health probes.
func unauthenticatedListenerWarning(listenAddress string, authEnabled, hostedCloud bool) bool {
	return !authEnabled && exposureOf(listenAddress) != listenerLoopback && !hostedCloud
}

// allowAnyHostWarning decides the startup warning for --allowed-hosts='*'. It
// reads the configured address, before Start normalizes "" to loopback.
func allowAnyHostWarning(allowAnyHost, authEnabled bool, listenAddress string) bool {
	return allowAnyHost && !authEnabled && exposureOf(listenAddress) != listenerLoopback
}
