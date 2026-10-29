package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/skyhook-io/radar/internal/fleet"
	pkgauth "github.com/skyhook-io/radar/pkg/auth"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	discoveryfake "k8s.io/client-go/discovery/fake"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	kubernetesfake "k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
	k8stesting "k8s.io/client-go/testing"
)

func fleetTestManager(t *testing.T, endpoint string) *fleet.Manager {
	t.Helper()
	config := fleet.Config{Version: 1, Connections: []fleet.ConnectionSpec{
		{ID: "a", Name: "A", Source: fleet.SourceSpec{Type: fleet.SourceInCluster}, Namespaces: []string{"team"}},
		{ID: "b", Name: "B", Source: fleet.SourceSpec{Type: fleet.SourceInCluster}, Namespaces: []string{"team"}},
	}, Fleets: []fleet.FleetSpec{{ID: "one", Name: "One", Connections: []string{"a"}}}}
	factory := func(_ context.Context, spec fleet.ConnectionSpec) (fleet.RuntimeClients, error) {
		typed := kubernetesfake.NewSimpleClientset(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "kube-system", UID: types.UID(spec.ID)}})
		pod := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "Pod", "metadata": map[string]any{"name": "same-name", "namespace": "team", "uid": spec.ID, "resourceVersion": "42"}}}
		var client dynamic.Interface = dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
			{Version: "v1", Resource: "pods"}:                                                    "PodList",
			{Version: "v1", Resource: "services"}:                                                "ServiceList",
			{Version: "v1", Resource: "nodes"}:                                                   "NodeList",
			{Version: "v1", Resource: "namespaces"}:                                              "NamespaceList",
			{Group: "apps", Version: "v1", Resource: "deployments"}:                              "DeploymentList",
			{Group: "apps", Version: "v1", Resource: "statefulsets"}:                             "StatefulSetList",
			{Group: "apps", Version: "v1", Resource: "daemonsets"}:                               "DaemonSetList",
			{Group: "apps", Version: "v1", Resource: "replicasets"}:                              "ReplicaSetList",
			{Group: "cluster.karmada.io", Version: "v1alpha1", Resource: "clusters"}:             "ClusterList",
			{Group: "work.karmada.io", Version: "v1alpha2", Resource: "resourcebindings"}:        "ResourceBindingList",
			{Group: "work.karmada.io", Version: "v1alpha2", Resource: "clusterresourcebindings"}: "ClusterResourceBindingList",
		}, pod)
		var cfg *rest.Config
		if endpoint != "" {
			cfg = &rest.Config{Host: endpoint, Timeout: time.Second}
			var err error
			client, err = dynamic.NewForConfig(cfg)
			if err != nil {
				return fleet.RuntimeClients{}, err
			}
		}
		return fleet.RuntimeClients{Typed: typed, Dynamic: client, Discovery: &discoveryfake.FakeDiscovery{Fake: &k8stesting.Fake{}}, Config: cfg}, nil
	}
	manager, err := fleet.New(config, filepath.Join(t.TempDir(), "state.json"), fleet.WithRuntimeFactory(factory), fleet.WithCacheSyncTimeout(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	if err := manager.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		ready := true
		for _, status := range manager.Statuses() {
			ready = ready && status.State == fleet.StateReady
		}
		if ready {
			return manager
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("fake runtimes not ready: %+v", manager.Statuses())
	return nil
}

func TestFleetSearchIdentityAndScope(t *testing.T) {
	manager := fleetTestManager(t, "")
	tools := fleetTools{source: manager}
	_, result, err := tools.search(context.Background(), nil, fleetSearchInput{Resource: "pods"})
	if err != nil || len(result.Resources) != 2 {
		t.Fatalf("search = %+v, %v", result, err)
	}
	a, b := result.Resources[0], result.Resources[1]
	if a.ClusterID == b.ClusterID || a.UID == b.UID || a.Ref != b.Ref || a.Source != "kubernetes-api" {
		t.Fatalf("identities collapsed: %+v", result.Resources)
	}
	_, selected, err := tools.search(context.Background(), nil, fleetSearchInput{Resource: "pods", FleetID: "one"})
	if err != nil || len(selected.Resources) != 1 || selected.Resources[0].ConnectionID != "a" {
		t.Fatalf("Fleet selection = %+v, %v", selected, err)
	}
	_, scoped, err := tools.search(context.Background(), nil, fleetSearchInput{Resource: "pods", Namespace: "outside"})
	if err != nil || len(scoped.Resources) != 0 || scoped.Coverage[0].Collection.State != "out_of_scope" {
		t.Fatalf("namespace clamp failed: %+v, %v", scoped, err)
	}
	if _, _, err := tools.search(context.Background(), nil, fleetSearchInput{Resource: "pods", ClusterIDs: []string{"missing"}}); err == nil {
		t.Fatal("unknown cluster fell back to an active cluster")
	}
	_, limited, err := tools.search(context.Background(), nil, fleetSearchInput{Resource: "pods", Limit: 1})
	if err != nil || len(limited.Resources) != 1 || !limited.Truncated {
		t.Fatalf("limit failed: %+v, %v", limited, err)
	}
}

func TestFleetAuthenticatedReadsUseTargetIdentity(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Impersonate-User") != "alice" || r.Header.Get("Impersonate-Group") != "readers" {
			t.Errorf("target request not impersonated")
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(metav1.Status{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Status"}, Reason: metav1.StatusReasonForbidden, Code: 403, Message: "sensitive upstream detail"})
	}))
	defer api.Close()
	manager := fleetTestManager(t, api.URL)
	tools := fleetTools{source: manager, requireAuth: true}
	if _, _, err := tools.search(context.Background(), nil, fleetSearchInput{Resource: "pods"}); err == nil {
		t.Fatal("missing caller accepted")
	}
	ctx := pkgauth.ContextWithUser(context.Background(), &pkgauth.User{Username: "alice", Groups: []string{"readers"}})
	_, result, err := tools.search(ctx, nil, fleetSearchInput{Resource: "pods"})
	if err != nil || len(result.Resources) != 0 {
		t.Fatalf("denied query = %+v, %v", result, err)
	}
	for _, coverage := range result.Coverage {
		if coverage.Collection.State != "denied" || coverage.Collection.Count != nil {
			t.Fatalf("denied represented as data: %+v", coverage)
		}
	}
	encoded, _ := json.Marshal(result)
	if strings.Contains(string(encoded), "sensitive upstream detail") {
		t.Fatal("upstream message leaked")
	}
}

func TestFleetCollectionCoverage(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusForbidden, http.StatusServiceUnavailable} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Query().Get("limit") != "500" {
					t.Error("unbounded collection")
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(status)
				if status == http.StatusOK {
					_, _ = w.Write([]byte(`{"apiVersion":"v1","kind":"PodList","metadata":{"continue":"next"},"items":[{"metadata":{"name":"one","namespace":"team"}}]}`))
				} else {
					reason := metav1.StatusReasonServiceUnavailable
					if status == http.StatusForbidden {
						reason = metav1.StatusReasonForbidden
					}
					_ = json.NewEncoder(w).Encode(metav1.Status{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Status"}, Reason: reason, Code: int32(status)})
				}
			}))
			defer api.Close()
			client, err := dynamic.NewForConfig(&rest.Config{Host: api.URL})
			if err != nil {
				t.Fatal(err)
			}
			_, coverage := fleetCollect(context.Background(), client, nil, fleetKinds[0], "", "")
			if status == http.StatusOK {
				if coverage.State != "partial" || coverage.Count == nil || *coverage.Count != 1 {
					t.Fatalf("truncated list was exact: %+v", coverage)
				}
			} else if coverage.Count != nil || coverage.State == "complete" {
				t.Fatalf("failed list was healthy zero: %+v", coverage)
			}
		})
	}
}

func TestFleetEvidenceMCPCatalogAndIsolation(t *testing.T) {
	manager := fleetTestManager(t, "")
	server := newFleetServer(fleetTools{source: manager})
	st, ct := mcpsdk.NewInMemoryTransports()
	ctx := context.Background()
	ss, err := server.Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "test", Version: "test"}, nil)
	cs, err := client.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cs.Close(); _ = ss.Wait() }()
	listed, err := cs.ListTools(ctx, nil)
	expectedTools := map[string]bool{
		"list_clusters": true, "get_fleet_overview": true, "search_fleet_resources": true,
		"get_fleet_resource": true, "get_fleet_events": true, "get_fleet_logs": true,
		"diagnose_fleet_resource": true, "get_fleet_relationships": true, "get_fleet_topology": true,
		"get_fleet_audit": true,
	}
	if err != nil || len(listed.Tools) != len(expectedTools) {
		t.Fatalf("catalog = %+v, %v", listed, err)
	}
	seenTools := make(map[string]bool, len(expectedTools))
	for _, tool := range listed.Tools {
		if !expectedTools[tool.Name] || seenTools[tool.Name] {
			t.Fatalf("unexpected tool in catalog: %s", tool.Name)
		}
		seenTools[tool.Name] = true
		if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
			t.Fatalf("write tool exposed: %s", tool.Name)
		}
	}
	for name := range expectedTools {
		if !seenTools[name] {
			t.Errorf("expected tool missing from catalog: %s", name)
		}
	}
	expectedProperties := map[string][]string{
		"get_fleet_resource":      {"clusterId", "resource", "namespace", "name", "uid"},
		"get_fleet_events":        {"clusterId", "resource", "namespace", "name", "uid", "limit"},
		"get_fleet_logs":          {"clusterId", "namespace", "name", "container", "tailLines", "previous"},
		"diagnose_fleet_resource": {"clusterId", "resource", "namespace", "name", "uid"},
		"get_fleet_relationships": {"fleetId", "clusterIds"},
		"get_fleet_topology":      {"fleetId", "clusterIds"},
	}
	for _, tool := range listed.Tools {
		required := expectedProperties[tool.Name]
		if len(required) == 0 {
			continue
		}
		schemaJSON, err := json.Marshal(tool.InputSchema)
		if err != nil {
			t.Fatalf("marshal input schema for %s: %v", tool.Name, err)
		}
		var schemaObject map[string]any
		if err := json.Unmarshal(schemaJSON, &schemaObject); err != nil {
			t.Fatalf("decode input schema for %s: %v", tool.Name, err)
		}
		properties, _ := schemaObject["properties"].(map[string]any)
		for _, property := range required {
			if _, ok := properties[property]; !ok {
				t.Errorf("%s input schema omits %q: %s", tool.Name, property, schemaJSON)
			}
		}
	}
	result, err := cs.CallTool(ctx, &mcpsdk.CallToolParams{Name: "search_fleet_resources", Arguments: map[string]any{"resource": "pods"}})
	if err != nil || result.IsError {
		t.Fatalf("MCP query = %+v, %v", result, err)
	}
	request := httptest.NewRequest(http.MethodPost, "http://localhost/mcp-fleet", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	request.Header.Set("Origin", "https://evil.example")
	request.Header.Set("Sec-Fetch-Site", "cross-site")
	w := httptest.NewRecorder()
	NewFleetHandler(manager, false).ServeHTTP(w, request)
	if w.Code != http.StatusForbidden {
		t.Fatalf("cross-site request status = %d", w.Code)
	}
}

func TestFleetCollectionPreservesNamespaceGaps(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "/blocked/") {
			w.WriteHeader(http.StatusForbidden)
			_ = json.NewEncoder(w).Encode(metav1.Status{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Status"}, Reason: metav1.StatusReasonForbidden, Code: 403})
			return
		}
		if !strings.Contains(r.URL.Path, "/allowed/") {
			t.Error("collection widened beyond configured namespaces")
		}
		_, _ = w.Write([]byte(`{"apiVersion":"v1","kind":"PodList","metadata":{},"items":[]}`))
	}))
	defer api.Close()
	client, err := dynamic.NewForConfig(&rest.Config{Host: api.URL})
	if err != nil {
		t.Fatal(err)
	}
	_, coverage := fleetCollect(context.Background(), client, []string{"allowed", "blocked"}, fleetKinds[0], "", "")
	if coverage.State != "partial" || coverage.Count == nil || *coverage.Count != 0 || coverage.Scope != "namespaces" {
		t.Fatalf("partial scope lost: %+v", coverage)
	}
	if len(coverage.Namespaces) != 2 || coverage.Namespaces[0].State != "complete" || coverage.Namespaces[1].State != "denied" {
		t.Fatalf("namespace gap lost: %+v", coverage.Namespaces)
	}
}
