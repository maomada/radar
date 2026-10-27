package server

import (
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/cloud"
	"github.com/skyhook-io/radar/internal/investigationrefs"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/settings"
)

var updatePostureMatrix = flag.Bool("update-posture-matrix", false, "rewrite testdata/posture_matrix.json from the current decisions")

const postureMatrixPath = "testdata/posture_matrix.json"

// The posture matrix pins every decision that depends on how Radar is deployed
// (hosted Cloud, Hub tunnel, auth, listener, in-cluster, config ownership) for
// every combination of the inputs that decision reads. A change to a row is a
// change to who can use a capability; review it as one.

var (
	matrixListeners    = []string{"", "127.0.0.1", "::1", "0.0.0.0", "192.168.1.5"}
	matrixAuthModes    = []string{"none", "proxy"}
	matrixBools        = []bool{false, true}
	matrixKubeModes    = []string{"single", "in-cluster"}
	matrixPprof        = []string{"unset", "true", "false"}
	matrixRequestHosts = []string{"localhost:9280", "192.168.1.5:9280", "radar:9280", "allowed.example.com:9280", "rebound.example.com:9280"}
)

type postureEnv struct {
	hostedCloud    bool
	kubeMode       string
	serviceHostEnv bool
	operator       bool
	forceNoTerm    bool
}

// apply sets the process-wide inputs a decision reads and checks they took.
func (e postureEnv) apply(t *testing.T) {
	t.Helper()
	if e.hostedCloud {
		t.Setenv("RADAR_CLOUD_MODE", "true")
	} else {
		t.Setenv("RADAR_CLOUD_MODE", "")
	}
	if e.serviceHostEnv {
		t.Setenv("KUBERNETES_SERVICE_HOST", "10.0.0.1")
	} else {
		t.Setenv("KUBERNETES_SERVICE_HOST", "")
	}
	kubeMode := e.kubeMode
	if kubeMode == "" {
		kubeMode = "single"
	}
	restoreMode := k8s.SetTestKubeconfigMode(kubeMode)
	t.Cleanup(restoreMode)
	if e.operator {
		settings.SetOperatorConfig(&settings.OperatorConfig{Version: 1})
	} else {
		settings.SetOperatorConfig(nil)
	}
	t.Cleanup(func() { settings.SetOperatorConfig(nil) })
	prevForce := k8s.ForceDisableLocalTerminal
	k8s.ForceDisableLocalTerminal = e.forceNoTerm
	t.Cleanup(func() { k8s.ForceDisableLocalTerminal = prevForce })

	if cloud.Mode() != e.hostedCloud {
		t.Fatalf("cloud.Mode() = %v, want %v", cloud.Mode(), e.hostedCloud)
	}
	if got := k8s.IsInCluster(); got != (kubeMode == "in-cluster") {
		t.Fatalf("k8s.IsInCluster() = %v for kubeconfig mode %q", got, kubeMode)
	}
	if got := k8s.GetKubeconfigSummary().Mode; got != kubeMode {
		t.Fatalf("kubeconfig summary mode = %q, want %q", got, kubeMode)
	}
	if settings.OperatorConfigured() != e.operator {
		t.Fatalf("settings.OperatorConfigured() = %v, want %v", settings.OperatorConfigured(), e.operator)
	}
}

func matrixServer(listen, authMode string, tunnel bool) *Server {
	mode := authMode
	if mode == "none" {
		mode = ""
	}
	return &Server{
		listenAddress:   listen,
		authConfig:      auth.Config{Mode: mode},
		cloudConnectCfg: CloudConnectConfig{CloudTunnelConfigured: tunnel},
		allowedHosts:    map[string]struct{}{"allowed.example.com": {}},
	}
}

func matrixRequest(host string, viaTunnel bool) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	r.Host = host
	if viaTunnel {
		cloud.AuthenticatedTunnelHandler(http.HandlerFunc(func(_ http.ResponseWriter, tr *http.Request) { r = tr })).ServeHTTP(httptest.NewRecorder(), r)
	}
	return r
}

func key(parts ...any) string {
	var b strings.Builder
	for i := 0; i+1 < len(parts); i += 2 {
		if b.Len() > 0 {
			b.WriteByte(' ')
		}
		fmt.Fprintf(&b, "%v=%v", parts[i], parts[i+1])
	}
	return b.String()
}

func listenLabel(listen string) string {
	if listen == "" {
		return `""`
	}
	return listen
}

// buildPostureMatrix records today's decisions. Each decision runs in its own
// subtest so t.Setenv and the restore funcs reset the process-wide inputs.
func buildPostureMatrix(t *testing.T) map[string]map[string]string {
	t.Helper()
	m := map[string]map[string]string{}
	record := func(decision, k, v string) {
		if m[decision] == nil {
			m[decision] = map[string]string{}
		}
		if _, dup := m[decision][k]; dup {
			t.Fatalf("%s: duplicate key %q", decision, k)
		}
		m[decision][k] = v
	}
	run := func(env postureEnv, fn func()) {
		t.Run("case", func(t *testing.T) {
			env.apply(t)
			fn()
		})
	}

	// Host check: what the middleware does with a request, per listener, auth,
	// --allowed-hosts='*' and request channel.
	for _, listen := range matrixListeners {
		for _, authMode := range matrixAuthModes {
			for _, anyHost := range matrixBools {
				for _, host := range matrixRequestHosts {
					for _, viaTunnel := range matrixBools {
						run(postureEnv{}, func() {
							s := matrixServer(listen, authMode, false)
							s.allowAnyHost = anyHost
							reached := false
							w := httptest.NewRecorder()
							s.protectUnauthenticatedHost(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached = true })).ServeHTTP(w, matrixRequest(host, viaTunnel))
							out := "served"
							if !reached {
								out = strconv.Itoa(w.Code)
							}
							record("hostCheck", key("listen", listenLabel(listen), "auth", authMode, "allowAnyHost", anyHost, "host", host, "viaTunnel", viaTunnel), out)
						})
					}
				}
			}
		}
	}

	// Host local terminal.
	for _, forceNoTerm := range matrixBools {
		for _, authMode := range matrixAuthModes {
			for _, kubeMode := range matrixKubeModes {
				for _, listen := range matrixListeners {
					for _, req := range []struct {
						host      string
						viaTunnel bool
					}{{"localhost:9280", false}, {"192.168.1.5:9280", false}, {"localhost:9280", true}} {
						run(postureEnv{kubeMode: kubeMode, forceNoTerm: forceNoTerm}, func() {
							s := matrixServer(listen, authMode, false)
							status, msg := s.localTerminalUnavailable(matrixRequest(req.host, req.viaTunnel))
							out := "available"
							if status != 0 {
								out = fmt.Sprintf("%d %s", status, msg)
							}
							record("localTerminal", key("forceDisabled", forceNoTerm, "auth", authMode, "kubeconfig", kubeMode, "listen", listenLabel(listen), "host", req.host, "viaTunnel", req.viaTunnel), out)
						})
					}
				}
			}
		}
	}

	// Profiling routes mounted.
	for _, hostedCloud := range matrixBools {
		for _, override := range matrixPprof {
			for _, authMode := range matrixAuthModes {
				for _, listen := range matrixListeners {
					run(postureEnv{hostedCloud: hostedCloud}, func() {
						s := matrixServer(listen, authMode, false)
						if override != "unset" {
							v := override == "true"
							s.enablePprof = &v
						}
						record("pprof", key("hostedCloud", hostedCloud, "RADAR_PPROF", override, "auth", authMode, "listen", listenLabel(listen)), strconv.FormatBool(s.pprofEnabled()))
					})
				}
			}
		}
	}

	// Cloud Connect driver lane, and the shared-listener acknowledgement.
	for _, hostedCloud := range matrixBools {
		for _, kubeMode := range matrixKubeModes {
			for _, authMode := range matrixAuthModes {
				for _, tunnel := range matrixBools {
					run(postureEnv{hostedCloud: hostedCloud, kubeMode: kubeMode}, func() {
						s := matrixServer("127.0.0.1", authMode, tunnel)
						record("cloudConnectDriver", key("hostedCloud", hostedCloud, "kubeconfig", kubeMode, "auth", authMode, "tunnelConfigured", tunnel), strconv.FormatBool(s.cloudConnectDriverEnabled()))
					})
				}
			}
		}
	}
	for _, listen := range matrixListeners {
		run(postureEnv{}, func() {
			record("cloudConnectSharedListenerAck", key("listen", listenLabel(listen)), strconv.FormatBool(matrixServer(listen, "none", false).cloudConnectNeedsSharedAck()))
		})
	}

	// Decisions that read config ownership.
	for _, hostedCloud := range matrixBools {
		for _, tunnel := range matrixBools {
			for _, authMode := range matrixAuthModes {
				for _, kubeMode := range matrixKubeModes {
					for _, serviceHost := range matrixBools {
						for _, operator := range matrixBools {
							for _, listen := range matrixListeners {
								env := postureEnv{hostedCloud: hostedCloud, kubeMode: kubeMode, serviceHostEnv: serviceHost, operator: operator}
								k := key("hostedCloud", hostedCloud, "tunnelConfigured", tunnel, "auth", authMode, "kubeconfig", kubeMode, "KUBERNETES_SERVICE_HOST", serviceHost, "operatorSettings", operator, "listen", listenLabel(listen))
								run(env, func() {
									s := matrixServer(listen, authMode, tunnel)
									record("configManagement", k, s.configManagement())
									record("usageShared", k, strconv.FormatBool(s.usageShared()))
									for _, mounted := range matrixBools {
										s.mcpInvestigationHandler, s.aiInvestigationRefs = nil, nil
										if mounted {
											s.mcpInvestigationHandler = http.NotFoundHandler()
											s.aiInvestigationRefs = &investigationrefs.Registry{}
										}
										record("localAIEngine", k+" "+key("investigationMCP", mounted), strconv.FormatBool(s.aiDeploymentSupported()))
									}
								})
							}
						}
					}
				}
			}
		}
	}

	// Startup warnings. The Start path skips them in hosted Cloud mode; the
	// startup summary reads the same flag from its summary.
	for _, hostedCloud := range matrixBools {
		for _, authMode := range matrixAuthModes {
			for _, listen := range matrixListeners {
				run(postureEnv{hostedCloud: hostedCloud}, func() {
					k := key("hostedCloud", hostedCloud, "auth", authMode, "listen", listenLabel(listen))
					s := matrixServer(listen, authMode, false)
					record("unauthenticatedListenerWarning/start", k, strconv.FormatBool(unauthenticatedListenerWarning(listen, s.authConfig.Enabled(), cloud.Mode())))
					mode := s.authConfig.Mode
					lines := formatStartupLogSummary(startupLogSummary{listenAddress: listen, authMode: mode, cloudMode: cloud.Mode()}, false)
					record("unauthenticatedListenerWarning/summary", k, strconv.FormatBool(strings.Contains(strings.Join(lines, "\n"), "Remote clients can access Radar without authentication")))
					for _, anyHost := range matrixBools {
						record("allowAnyHostWarning", k+" "+key("allowAnyHost", anyHost), strconv.FormatBool(allowAnyHostWarning(anyHost, s.authConfig.Enabled(), listen)))
					}
				})
			}
		}
	}
	return m
}

func TestPostureMatrix(t *testing.T) {
	got := buildPostureMatrix(t)
	if t.Failed() {
		return
	}
	data, err := json.MarshalIndent(got, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, '\n')
	if *updatePostureMatrix {
		if err := os.MkdirAll(filepath.Dir(postureMatrixPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(postureMatrixPath, data, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(postureMatrixPath)
	if err != nil {
		t.Fatalf("read %s: %v (run with -update-posture-matrix to create it)", postureMatrixPath, err)
	}
	var wantM map[string]map[string]string
	if err := json.Unmarshal(want, &wantM); err != nil {
		t.Fatal(err)
	}
	for decision, rows := range wantM {
		for k, w := range rows {
			if g, ok := got[decision][k]; !ok {
				t.Errorf("%s [%s]: row missing, want %q", decision, k, w)
			} else if g != w {
				t.Errorf("%s [%s] = %q, want %q", decision, k, g, w)
			}
		}
	}
	for decision, rows := range got {
		for k := range rows {
			if _, ok := wantM[decision][k]; !ok {
				t.Errorf("%s [%s]: new row %q not in %s; review it and rerun with -update-posture-matrix", decision, k, rows[k], postureMatrixPath)
			}
		}
	}
}
