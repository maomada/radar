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

	"github.com/skyhook-io/radar/internal/fleet"
	pkgauth "github.com/skyhook-io/radar/pkg/auth"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	discoveryfake "k8s.io/client-go/discovery/fake"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes"
	kubernetesfake "k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
	k8stesting "k8s.io/client-go/testing"
)

func callFleetAPI(t *testing.T, handler http.Handler, method, path, body string, username string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, "http://localhost"+path, strings.NewReader(body))
	if username != "" {
		request = request.WithContext(pkgauth.ContextWithUser(request.Context(), &pkgauth.User{Username: username}))
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func TestFleetAPIManagementAuthorizationAndInventoryContract(t *testing.T) {
	manager := fleetTestManager(t, "")
	_, api := NewFleetEndpoints(manager, FleetOptions{RequireAuth: true, AdminUsers: []string{"alice"}})

	if response := callFleetAPI(t, api, http.MethodGet, "/capabilities", "", ""); response.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous capabilities status=%d", response.Code)
	}
	if response := callFleetAPI(t, api, http.MethodPost, "/query/list_clusters", `{}`, ""); response.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous inventory query status=%d", response.Code)
	}
	response := callFleetAPI(t, api, http.MethodGet, "/capabilities", "", "alice-other")
	var capabilities map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &capabilities); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusOK || len(capabilities) != 4 || capabilities["enabled"] != true ||
		capabilities["canManage"] != false || capabilities["localConnections"] != false || capabilities["writesEnabled"] != false {
		t.Fatalf("capabilities contract = %d %v", response.Code, capabilities)
	}
	if response := callFleetAPI(t, api, http.MethodPost, "/query/prepare_fleet_operation", `{}`, "alice"); response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "disabled") {
		t.Fatalf("disabled Fleet operation status=%d body=%s", response.Code, response.Body.String())
	}
	if response := callFleetAPI(t, api, http.MethodPut, "/fleets/new", `{"name":"New","clusterIds":[]}`, "alice-other"); response.Code != http.StatusForbidden {
		t.Fatalf("non-admin metadata write status=%d", response.Code)
	}
	if response := callFleetAPI(t, api, http.MethodPut, "/fleets/new", `{"name":"New","clusterIds":[]}`, "alice"); response.Code != http.StatusOK {
		t.Fatalf("exact admin metadata write status=%d: %s", response.Code, response.Body.String())
	}
	if response := callFleetAPI(t, api, http.MethodPost, "/query/list_clusters", `{}`, "alice-other"); response.Code != http.StatusOK {
		t.Fatalf("ordinary authenticated inventory query status=%d: %s", response.Code, response.Body.String())
	}
	for _, method := range []string{http.MethodPut, http.MethodDelete} {
		path := "/fleets/one"
		body := `{"name":"Changed","clusterIds":[]}`
		if method == http.MethodDelete {
			body = ""
		}
		response := callFleetAPI(t, api, method, path, body, "alice")
		if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "edit fleet configuration") {
			t.Fatalf("declarative fleet %s status=%d body=%s", method, response.Code, response.Body.String())
		}
	}

	response = callFleetAPI(t, api, http.MethodPost, "/query/list_clusters", `{}`, "alice")
	if response.Code != http.StatusOK {
		t.Fatalf("inventory query status=%d: %s", response.Code, response.Body.String())
	}
	var inventory map[string]json.RawMessage
	if err := json.Unmarshal(response.Body.Bytes(), &inventory); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"clusters", "fleets", "memberships", "managedConnectionIds", "duplicateDecisions"} {
		if string(inventory[key]) == "" || string(inventory[key]) == "null" {
			t.Fatalf("inventory %s is missing or null: %s", key, response.Body.String())
		}
	}
	var managedIDs []string
	if err := json.Unmarshal(inventory["managedConnectionIds"], &managedIDs); err != nil || managedIDs == nil || len(managedIDs) != 0 {
		t.Fatalf("managedConnectionIds = %v, %v", managedIDs, err)
	}
}

func TestFleetAPIRequiresStrictObjectJSON(t *testing.T) {
	manager := fleetTestManager(t, "")
	_, api := NewFleetEndpoints(manager, FleetOptions{RequireAuth: true, AdminUsers: []string{"alice"}})
	for _, body := range []string{"null", "[]", `{} {}`, strings.Repeat(" ", fleetAPIRequestLimit+1)} {
		response := callFleetAPI(t, api, http.MethodPost, "/query/list_clusters", body, "alice")
		if response.Code != http.StatusBadRequest {
			t.Fatalf("body length %d status=%d want=%d", len(body), response.Code, http.StatusBadRequest)
		}
	}
}

func TestFleetAPIFailsClosedInUserTokenMode(t *testing.T) {
	previous := pkgauth.CurrentCredentialMode()
	pkgauth.SetCredentialMode(pkgauth.CredentialsUserToken)
	defer pkgauth.SetCredentialMode(previous)

	manager := fleetTestManager(t, "")
	_, api := NewFleetEndpoints(manager, FleetOptions{RequireAuth: true, AdminUsers: []string{"alice"}})
	for _, request := range []struct {
		method string
		path   string
		body   string
	}{
		{method: http.MethodGet, path: "/capabilities"},
		{method: http.MethodPost, path: "/query/list_clusters", body: `{}`},
	} {
		response := callFleetAPI(t, api, request.method, request.path, request.body, "alice")
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("user-token mode %s %s status=%d", request.method, request.path, response.Code)
		}
	}
}

func TestFleetAPICandidateDTOAndCurrentTrustResolution(t *testing.T) {
	source := "/trusted/config"
	candidate := fleet.ConnectionCandidate{
		ID: fleet.CandidateID(source, "prod"), Name: "Production", Context: "prod",
		SourceLabel: "config", SourceFile: source, RawContext: "prod", DisplayClusterName: "prod-cluster",
	}
	trusted := true
	provider := func() ([]fleet.ConnectionCandidate, error) {
		if !trusted {
			return []fleet.ConnectionCandidate{}, nil
		}
		return []fleet.ConnectionCandidate{candidate}, nil
	}
	manager := fleetManagementTestManager(t, filepath.Join(t.TempDir(), "state.json"), provider, false)
	_, api := NewFleetEndpoints(manager, FleetOptions{
		RequireAuth: true, AdminUsers: []string{"alice"}, LocalConnections: true, Candidates: provider,
	})

	response := callFleetAPI(t, api, http.MethodGet, "/candidates", "", "alice-other")
	if response.Code != http.StatusForbidden {
		t.Fatalf("non-admin candidate listing status=%d", response.Code)
	}
	response = callFleetAPI(t, api, http.MethodGet, "/candidates", "", "alice")
	if response.Code != http.StatusOK {
		t.Fatalf("candidate list status=%d: %s", response.Code, response.Body.String())
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	var candidates []map[string]any
	if err := json.Unmarshal(payload["candidates"], &candidates); err != nil || len(candidates) != 1 {
		t.Fatalf("candidates = %v, %v", candidates, err)
	}
	if len(candidates[0]) != 4 || candidates[0]["sourceLabel"] != "config" {
		t.Fatalf("candidate DTO exposed unexpected fields: %v", candidates[0])
	}
	encoded, _ := json.Marshal(candidates[0])
	if strings.Contains(string(encoded), source) || strings.Contains(string(encoded), "rawContext") || strings.Contains(string(encoded), "credential") {
		t.Fatalf("candidate DTO leaked trusted source details: %s", encoded)
	}

	body, _ := json.Marshal(map[string]string{"id": "local-prod", "name": "Prod", "candidateId": candidate.ID})
	response = callFleetAPI(t, api, http.MethodPost, "/connections", string(body), "alice")
	if response.Code != http.StatusOK {
		t.Fatalf("trusted candidate admission status=%d: %s", response.Code, response.Body.String())
	}
	trusted = false
	response = callFleetAPI(t, api, http.MethodPost, "/connections", string(body), "alice")
	if response.Code != http.StatusConflict || strings.Contains(response.Body.String(), source) {
		t.Fatalf("stale candidate admission status=%d: %s", response.Code, response.Body.String())
	}
}

func TestFleetAPIRefreshOutlivesRequestContext(t *testing.T) {
	manager := fleetManagementTestManager(t, "", nil, false)
	_, api := NewFleetEndpoints(manager, FleetOptions{RequireAuth: true, AdminUsers: []string{"alice"}})
	clusterID := manager.Statuses()[0].ClusterID
	ctx, cancel := context.WithCancel(context.Background())
	ctx = pkgauth.ContextWithUser(ctx, &pkgauth.User{Username: "alice"})
	cancel()
	request := httptest.NewRequest(http.MethodPost, "http://localhost/clusters/"+clusterID+"/refresh", nil).WithContext(ctx)
	response := httptest.NewRecorder()
	api.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("refresh status=%d: %s", response.Code, response.Body.String())
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		status, err := manager.Status(clusterID)
		if err == nil && status.State == fleet.StateReady {
			if _, err := manager.Runtime(clusterID); err != nil {
				t.Fatalf("refreshed runtime unavailable: %v", err)
			}
			return
		}
		if err == nil && status.State == fleet.StateUnavailable {
			t.Fatalf("refresh was canceled with the HTTP request: %+v", status)
		}
		time.Sleep(10 * time.Millisecond)
	}
	status, _ := manager.Status(clusterID)
	t.Fatalf("refreshed runtime did not become ready: %+v", status)
}

func TestFleetManagedConnectionPersistsAndRemovesOnlyOwnedRuntime(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "state.json")
	source := "/trusted/config"
	candidate := fleet.ConnectionCandidate{
		ID: fleet.CandidateID(source, "prod"), Name: "Production", Context: "prod",
		SourceLabel: "config", SourceFile: source, RawContext: "prod",
	}
	trusted := true
	provider := func() ([]fleet.ConnectionCandidate, error) {
		if !trusted {
			return []fleet.ConnectionCandidate{}, nil
		}
		return []fleet.ConnectionCandidate{candidate}, nil
	}
	manager := fleetManagementTestManager(t, statePath, provider, true)
	_, api := NewFleetEndpoints(manager, FleetOptions{
		RequireAuth: true, AdminUsers: []string{"alice"}, LocalConnections: true, Candidates: provider,
	})
	body := `{"id":"local-prod","name":"Prod","candidateId":"` + candidate.ID + `"}`
	response := callFleetAPI(t, api, http.MethodPost, "/connections", body, "alice")
	if response.Code != http.StatusOK {
		t.Fatalf("connection add status=%d: %s", response.Code, response.Body.String())
	}
	if response := callFleetAPI(t, api, http.MethodDelete, "/connections/a", "", "alice"); response.Code != http.StatusConflict {
		t.Fatalf("declarative connection removal status=%d: %s", response.Code, response.Body.String())
	}
	var added struct {
		ClusterID string `json:"clusterId"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &added); err != nil || added.ClusterID == "" {
		t.Fatalf("add response = %+v, %v", added, err)
	}
	waitForFleetRuntime(t, manager, added.ClusterID)
	response = callFleetAPI(t, api, http.MethodPost, "/query/list_clusters", `{}`, "alice")
	var inventory struct {
		ManagedConnectionIDs []string `json:"managedConnectionIds"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &inventory); err != nil ||
		len(inventory.ManagedConnectionIDs) != 1 || inventory.ManagedConnectionIDs[0] != "local-prod" {
		t.Fatalf("managed connection inventory = %+v, %v", inventory, err)
	}
	declaredID := ""
	for _, connection := range manager.Catalog().Connections {
		if connection.ID == "a" {
			declaredID = connection.ClusterID
			break
		}
	}
	if declaredID == "" {
		t.Fatal("declared peer cluster ID missing")
	}
	if err := manager.SetDuplicateDecision(declaredID, added.ClusterID, "keep_separate"); err != nil {
		t.Fatalf("persist duplicate decision: %v", err)
	}
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}
	manager = fleetManagementTestManager(t, statePath, provider, true)
	if len(manager.Catalog().DuplicateDecisions) != 1 {
		t.Fatalf("trusted restart lost duplicate decision: %+v", manager.Catalog().DuplicateDecisions)
	}
	if _, err := manager.Runtime(declaredID); err != nil {
		t.Fatalf("declared peer not routable after trusted restart: %v", err)
	}
	waitForFleetRuntime(t, manager, added.ClusterID)
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}
	trusted = false
	manager = fleetManagementTestManager(t, statePath, provider, true)
	managedID := ""
	for _, connection := range manager.Catalog().Connections {
		if connection.ID == "local-prod" {
			managedID = connection.ClusterID
			if connection.Active {
				t.Fatal("missing trusted source remained active after restart")
			}
		}
	}
	if managedID != added.ClusterID {
		t.Fatalf("managed cluster ID changed after restart: before=%s after=%s", added.ClusterID, managedID)
	}
	if _, err := manager.Runtime(managedID); err == nil {
		t.Fatal("managed runtime without a trusted source remained routable")
	}
	if _, err := manager.Runtime(declaredID); err != nil {
		t.Fatalf("trust loss for managed source affected declared peer: %v", err)
	}
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}
	trusted = true
	manager = fleetManagementTestManager(t, statePath, provider, true)
	if manager.Catalog().Connections[0].ID != "a" && manager.Catalog().Connections[1].ID != "a" {
		t.Fatalf("declarative connections lost after restart: %+v", manager.Catalog().Connections)
	}
	for _, connection := range manager.Catalog().Connections {
		if connection.ID == "local-prod" && (!connection.Active || connection.ClusterID != added.ClusterID) {
			t.Fatalf("managed connection did not reactivate with its stable ID: %+v", connection)
		}
	}
	if _, err := manager.Runtime(declaredID); err != nil {
		t.Fatalf("declarative peer not routable after restart: %v", err)
	}
	_, api = NewFleetEndpoints(manager, FleetOptions{
		RequireAuth: true, AdminUsers: []string{"alice"}, LocalConnections: true, Candidates: provider,
	})
	response = callFleetAPI(t, api, http.MethodDelete, "/connections/local-prod", "", "alice")
	if response.Code != http.StatusOK {
		t.Fatalf("connection remove status=%d: %s", response.Code, response.Body.String())
	}
	if _, err := manager.Runtime(managedID); err == nil {
		t.Fatal("removed managed runtime remained routable")
	}
	if _, err := manager.Runtime(declaredID); err != nil {
		t.Fatalf("removing managed runtime affected peer: %v", err)
	}
	for _, connection := range manager.Catalog().Connections {
		if connection.ID == "local-prod" && connection.Active {
			t.Fatal("removed managed record remains active")
		}
	}
}

func TestFleetAPIJSONUsesNoStore(t *testing.T) {
	success := httptest.NewRecorder()
	writeFleetJSON(success, http.StatusOK, map[string]bool{"enabled": true})
	if got := success.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("success Cache-Control = %q, want no-store", got)
	}
	failure := httptest.NewRecorder()
	writeFleetError(failure, http.StatusBadRequest, context.Canceled)
	if got := failure.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("error Cache-Control = %q, want no-store", got)
	}
}

func TestFleetAPIDuplicateDecisionsRequireVerifiedPairAndStaySeparate(t *testing.T) {
	manager := fleetManagementTestManager(t, filepath.Join(t.TempDir(), "state.json"), nil, true)
	_, api := NewFleetEndpoints(manager, FleetOptions{RequireAuth: true, AdminUsers: []string{"alice"}})
	clusters := manager.Catalog().Clusters
	if len(clusters) != 2 || clusters[0].ID == clusters[1].ID {
		t.Fatalf("clusters are not independent: %+v", clusters)
	}
	body := `{"clusterId":"` + clusters[0].ID + `","otherClusterId":"` + clusters[1].ID + `","decision":"keep_separate"}`
	response := callFleetAPI(t, api, http.MethodPost, "/duplicates", body, "alice")
	if response.Code != http.StatusOK {
		t.Fatalf("verified duplicate decision status=%d: %s", response.Code, response.Body.String())
	}
	if len(manager.Catalog().DuplicateDecisions) != 1 || len(manager.Catalog().Clusters) != 2 {
		t.Fatalf("duplicate decision merged or disappeared: %+v", manager.Catalog())
	}
	if _, err := manager.Runtime(clusters[0].ID); err != nil {
		t.Fatalf("first duplicate runtime unavailable: %v", err)
	}
	if _, err := manager.Runtime(clusters[1].ID); err != nil {
		t.Fatalf("second duplicate runtime unavailable: %v", err)
	}
}

func fleetManagementTestManager(t *testing.T, statePath string, provider fleet.CandidateProvider, sharedUID bool) *fleet.Manager {
	t.Helper()
	if statePath == "" {
		statePath = filepath.Join(t.TempDir(), "state.json")
	}
	config := fleet.Config{
		Version: 1,
		Connections: []fleet.ConnectionSpec{
			{ID: "a", Name: "A", Source: fleet.SourceSpec{Type: fleet.SourceInCluster}, Namespaces: []string{"team"}},
			{ID: "b", Name: "B", Source: fleet.SourceSpec{Type: fleet.SourceInCluster}, Namespaces: []string{"team"}},
		},
		Fleets: []fleet.FleetSpec{{ID: "one", Name: "One", Connections: []string{"a"}}},
	}
	factory := func(_ context.Context, spec fleet.ConnectionSpec) (fleet.RuntimeClients, error) {
		typed := kubernetesfake.NewSimpleClientset(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "kube-system", UID: types.UID(spec.ID)}})
		listKinds := map[schema.GroupVersionResource]string{
			{Version: "v1", Resource: "pods"}:                       "PodList",
			{Group: "apps", Version: "v1", Resource: "deployments"}: "DeploymentList",
			{Version: "v1", Resource: "services"}:                   "ServiceList",
			{Version: "v1", Resource: "nodes"}:                      "NodeList",
			{Version: "v1", Resource: "namespaces"}:                 "NamespaceList",
		}
		var dynamicClient dynamic.Interface = dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), listKinds)
		return fleet.RuntimeClients{
			Typed: typed, Dynamic: dynamicClient,
			Discovery: &discoveryfake.FakeDiscovery{Fake: &k8stesting.Fake{}},
			Config:    &rest.Config{Host: "https://fleet.test"},
		}, nil
	}
	probe := func(ctx context.Context, client kubernetes.Interface) (string, error) {
		if sharedUID {
			return "shared-uid", nil
		}
		namespace, err := client.CoreV1().Namespaces().Get(ctx, "kube-system", metav1.GetOptions{})
		if err != nil {
			return "", err
		}
		return string(namespace.UID), nil
	}
	options := []fleet.ManagerOption{
		fleet.WithRuntimeFactory(factory),
		fleet.WithIdentityProbe(probe),
		fleet.WithCacheSyncTimeout(time.Second),
		fleet.WithProbeTimeout(time.Second),
		fleet.WithCandidateProvider(provider),
	}
	manager, err := fleet.New(config, statePath, options...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	if err := manager.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		statuses := manager.Statuses()
		expected := make(map[string]struct{})
		for _, connection := range manager.Catalog().Connections {
			if connection.Active {
				expected[connection.ClusterID] = struct{}{}
			}
		}
		ready := len(statuses) == len(expected)
		for _, status := range statuses {
			_, expectedCluster := expected[status.ClusterID]
			ready = ready && expectedCluster && status.State == fleet.StateReady
		}
		if ready {
			return manager
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("fake Fleet runtimes not ready: %+v", manager.Statuses())
	return nil
}

func waitForFleetRuntime(t *testing.T, manager *fleet.Manager, clusterID string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := manager.Runtime(clusterID); err == nil {
			return
		}
		status, err := manager.Status(clusterID)
		if err != nil {
			t.Fatal(err)
		}
		if status.State != fleet.StateConnecting {
			t.Fatalf("runtime unavailable in settled state %s", status.State)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("runtime %q remained unavailable", clusterID)
}
