package portal

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

const (
	testToken    = "token-value-with-at-least-thirty-two-characters"
	testUsername = "portal-viewer-user"
	testPassword = "portal-viewer-password"
)

func testRegistration(id, name string) Registration {
	return Registration{
		ID: id, Name: name, RadarURL: "https://radar.example.com/radar",
		RuntimeMode: "local", KubeContext: "prod-context", ClusterName: "cluster-a",
		RadarVersion: "1.2.3", ConnectionState: "connected", AuthMode: "proxy",
		MCP: MCPRegistration{
			Mode: "full", Transport: "streamable-http",
			URL:         "https://radar.example.com/radar/mcp",
			ReadOnlyURL: "https://radar.example.com/radar/mcp-readonly",
		},
	}
}

func testHandler(t *testing.T, path string) (*Handler, *Registry) {
	t.Helper()
	registry, err := OpenRegistry(path)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler(registry, Credentials{
		Tokens:   map[string]string{"cluster-a": testToken, "cluster-b": "other-token-with-at-least-thirty-two-characters"},
		Username: testUsername, Password: testPassword,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return handler, registry
}

func TestRegistrationAuthIsIDScopedAndViewerCannotRegister(t *testing.T) {
	handler, _ := testHandler(t, filepath.Join(t.TempDir(), "registry.json"))
	register := func(path, authorization string, value Registration) *httptest.ResponseRecorder {
		t.Helper()
		body, _ := json.Marshal(value)
		request := httptest.NewRequest(http.MethodPut, path, strings.NewReader(string(body)))
		request.Header.Set("Authorization", authorization)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}

	if got := register("/api/registrations/cluster-a", "Bearer "+testToken, testRegistration("cluster-a", "good")); got.Code != http.StatusNoContent {
		t.Fatalf("valid registration status = %d, want 204", got.Code)
	}
	if got := register("/api/registrations/cluster-a", "Bearer wrong-token", testRegistration("cluster-a", "wrong")); got.Code != http.StatusUnauthorized {
		t.Fatalf("wrong token status = %d, want 401", got.Code)
	}
	if got := register("/api/registrations/cluster-b", "Bearer "+testToken, testRegistration("cluster-b", "wrong id")); got.Code != http.StatusUnauthorized {
		t.Fatalf("wrong ID token status = %d, want 401", got.Code)
	}
	if got := register("/api/registrations/cluster-a", "Basic dmlld2VyOnBhc3N3b3Jk", testRegistration("cluster-a", "viewer")); got.Code != http.StatusUnauthorized {
		t.Fatalf("viewer registration status = %d, want 401", got.Code)
	}

	read := httptest.NewRequest(http.MethodGet, "/api/clusters", nil)
	read.Header.Set("Authorization", "Bearer "+testToken)
	readResponse := httptest.NewRecorder()
	handler.ServeHTTP(readResponse, read)
	if readResponse.Code != http.StatusUnauthorized {
		t.Fatalf("bearer read status = %d, want 401", readResponse.Code)
	}
	read = httptest.NewRequest(http.MethodGet, "/api/clusters", nil)
	read.AddCookie(loginForTest(t, handler))
	readResponse = httptest.NewRecorder()
	handler.ServeHTTP(readResponse, read)
	if readResponse.Code != http.StatusOK {
		t.Fatalf("viewer read status = %d, want 200", readResponse.Code)
	}
	var listed ClusterList
	if err := json.Unmarshal(readResponse.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Clusters) != 1 || listed.Clusters[0].Name != "good" ||
		listed.Clusters[0].RuntimeMode != "local" ||
		listed.Clusters[0].KubeContext != "prod-context" ||
		listed.Clusters[0].ClusterName != "cluster-a" {
		t.Fatalf("unexpected registry contents: %+v", listed.Clusters)
	}
}

func TestRegistryRestartAndStaleTimestamp(t *testing.T) {
	path := filepath.Join(t.TempDir(), "registry.json")
	registry, err := OpenRegistry(path)
	if err != nil {
		t.Fatal(err)
	}
	registeredAt := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	registry.now = func() time.Time { return registeredAt }
	if err := registry.Register(testRegistration("cluster-a", "alpha")); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("registry permissions = %o, want 600", info.Mode().Perm())
	}

	restarted, err := OpenRegistry(path)
	if err != nil {
		t.Fatal(err)
	}
	restarted.now = func() time.Time { return registeredAt.Add(staleAfter + time.Second) }
	listed := restarted.List(map[string]struct{}{"cluster-a": {}})
	if len(listed.Clusters) != 1 || listed.Clusters[0].Status != "stale" ||
		!listed.Clusters[0].RegisteredAt.Equal(registeredAt) ||
		!listed.Clusters[0].LastSeen.Equal(registeredAt) ||
		listed.Clusters[0].RuntimeMode != "local" ||
		listed.Clusters[0].KubeContext != "prod-context" ||
		listed.Clusters[0].ClusterName != "cluster-a" {
		t.Fatalf("restart did not preserve record timestamps: %+v", listed)
	}
}

func TestRegistrationRejectsUnsafeURLsAndOversizedBody(t *testing.T) {
	handler, _ := testHandler(t, filepath.Join(t.TempDir(), "registry.json"))
	invalid := []Registration{
		testRegistration("cluster-a", "bad"),
		testRegistration("cluster-a", "bad"),
		testRegistration("cluster-a", "bad"),
		testRegistration("cluster-a", "bad"),
	}
	invalid[0].RadarURL = "javascript:alert(1)"
	invalid[1].RadarURL = "https://radar.example.com/radar?"
	invalid[2].RadarURL = "https://radar.example.com/radar#"
	invalid[3].MCP.URL = "https://mcp.example.com/other"
	for _, value := range invalid {
		body, _ := json.Marshal(value)
		request := httptest.NewRequest(http.MethodPut, "/api/registrations/cluster-a", strings.NewReader(string(body)))
		request.Header.Set("Authorization", "Bearer "+testToken)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("invalid URL registration status = %d, want 400", response.Code)
		}
	}

	request := httptest.NewRequest(http.MethodPut, "/api/registrations/cluster-a", strings.NewReader(strings.Repeat(" ", maxRegistrationBytes+1)))
	request.Header.Set("Authorization", "Bearer "+testToken)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized body status = %d, want 413", response.Code)
	}
}

func TestRegistrationValidatesRuntimeContextMetadata(t *testing.T) {
	valid := testRegistration("cluster-a", "alpha")
	for _, registration := range []Registration{
		func() Registration {
			value := valid
			value.RuntimeMode = "multi-source"
			return value
		}(),
		func() Registration {
			value := valid
			value.KubeContext = "dev\ncontext"
			return value
		}(),
		func() Registration {
			value := valid
			value.KubeContext = strings.Repeat("c", 1025)
			return value
		}(),
		func() Registration {
			value := valid
			value.ClusterName = "prod\x7fcluster"
			return value
		}(),
	} {
		if err := ValidateRegistration(registration); err == nil {
			t.Fatalf("ValidateRegistration accepted invalid metadata: %+v", registration)
		}
	}
}

func TestRegistrationWriteFailureDoesNotChangeMemory(t *testing.T) {
	dir := t.TempDir()
	parent := filepath.Join(dir, "not-a-directory")
	if err := os.Mkdir(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	registryPath := filepath.Join(parent, "registry.json")
	registry, err := OpenRegistry(registryPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(testRegistration("cluster-a", "alpha")); err != nil {
		t.Fatalf("initial registry write failed: %v", err)
	}
	if err := os.Remove(registryPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(parent); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(parent, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(testRegistration("cluster-a", "changed")); err == nil {
		t.Fatal("Register succeeded with an invalid parent directory")
	}
	if got := registry.List(map[string]struct{}{"cluster-a": {}}); len(got.Clusters) != 1 || got.Clusters[0].Name != "alpha" {
		t.Fatalf("failed persistence mutated in-memory registry: %+v", got.Clusters)
	}
}

func TestViewerUsernameValidationAndEmbeddedAssetServing(t *testing.T) {
	registry, err := OpenRegistry(filepath.Join(t.TempDir(), "registry.json"))
	if err != nil {
		t.Fatal(err)
	}
	credentials := Credentials{
		Tokens: map[string]string{"cluster-a": testToken}, Username: "p", Password: testPassword,
	}
	static := fstest.MapFS{
		"portal.html":   &fstest.MapFile{Data: []byte("<html>portal</html>")},
		"assets/app.js": &fstest.MapFile{Data: []byte("console.log('portal')")},
	}
	handler, err := NewHandler(registry, credentials, static)
	if err != nil {
		t.Fatalf("short valid username rejected: %v", err)
	}
	for _, path := range []string{"/login", "/assets/app.js"} {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("%s status = %d, want 200: %s", path, response.Code, response.Body.String())
		}
	}
	for _, username := range []string{"", "user:name", "user\nname"} {
		credentials.Username = username
		if _, err := NewHandler(registry, credentials, nil); err == nil {
			t.Errorf("invalid username %q accepted", username)
		}
	}
}

func TestViewerAuthAndHealthSeparation(t *testing.T) {
	handler, _ := testHandler(t, filepath.Join(t.TempDir(), "registry.json"))
	for _, path := range []string{"/api/clusters", "/api/auth/session"} {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusUnauthorized {
			t.Errorf("%s status = %d, want 401", path, response.Code)
		}
	}
	request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("/healthz status = %d, want 204", response.Code)
	}
}
