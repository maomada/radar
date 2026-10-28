package fleet

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

func TestParseConfigStrictAndBounded(t *testing.T) {
	valid := `{"version":1,"connections":[],"fleets":[]}`
	if _, err := ParseConfig([]byte(valid)); err != nil {
		t.Fatalf("ParseConfig(valid): %v", err)
	}
	for _, input := range []string{
		`{"version":1,"connections":[],"fleets":[],"extra":true}`,
		`{"version":1,"connections":[],"fleets":[]}{"version":1}`,
	} {
		if _, err := ParseConfig([]byte(input)); err == nil {
			t.Fatalf("ParseConfig(%q) unexpectedly succeeded", input)
		}
	}
	oversized := make([]byte, MaxConfigBytes+1)
	if _, err := ParseConfig(oversized); err == nil {
		t.Fatal("ParseConfig accepted an oversized document")
	}
}

func TestLoadConfigResolvesKubeconfigRelativeToConfig(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "fleet.json")
	data := []byte(`{"version":1,"connections":[{"id":"prod","name":"Production","source":{"type":"kubeconfig","kubeconfig":"kube/config.yaml","context":"prod"}}],"fleets":[]}`)
	if err := os.WriteFile(configPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	config, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	want := filepath.Join(dir, "kube", "config.yaml")
	if config.Connections[0].Source.Kubeconfig != want {
		t.Fatalf("resolved path = %q, want %q", config.Connections[0].Source.Kubeconfig, want)
	}
}

func TestValidateConfigRejectsInvalidNamespacesAndSources(t *testing.T) {
	config := Config{
		Version: ConfigVersion,
		Connections: []ConnectionSpec{{
			ID: "prod", Name: "Production",
			Source: SourceSpec{Type: SourceInCluster, Context: "forbidden"},
		}},
		Fleets: []FleetSpec{},
	}
	if err := ValidateConfig(config); err == nil {
		t.Fatal("ValidateConfig accepted in-cluster context")
	}
	config.Connections[0].Source = SourceSpec{Type: SourceKubeconfig, Kubeconfig: "config", Context: "prod"}
	config.Connections[0].Namespaces = []string{"bad_namespace"}
	if err := ValidateConfig(config); err == nil {
		t.Fatal("ValidateConfig accepted an invalid namespace")
	}
	if _, err := ParseConfig([]byte(`{"version":1,"connections":[{"id":"local","name":"Local","source":{"type":"in-cluster","context":""}}],"fleets":[]}`)); err == nil {
		t.Fatal("ParseConfig accepted an explicitly supplied in-cluster context")
	}
	if _, err := ParseConfig([]byte(`{"version":1,"connections":[{"id":"local","name":"Local","source":{"type":"in-cluster","context":null}}],"fleets":[]}`)); err == nil {
		t.Fatal("ParseConfig accepted a null in-cluster context field")
	}
}

func TestDiscoverContextsIsolatedAndNonSecret(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "one.yaml")
	raw := clientcmdapi.NewConfig()
	raw.Clusters["cluster-one"] = &clientcmdapi.Cluster{Server: "https://example.invalid"}
	raw.AuthInfos["user"] = &clientcmdapi.AuthInfo{Token: "not-returned"}
	raw.Contexts["prod"] = &clientcmdapi.Context{Cluster: "cluster-one", AuthInfo: "user"}
	raw.CurrentContext = "prod"
	if err := clientcmd.WriteToFile(*raw, path); err != nil {
		t.Fatal(err)
	}
	candidates, err := DiscoverContexts([]string{path})
	if err != nil {
		t.Fatalf("DiscoverContexts: %v", err)
	}
	if len(candidates) != 1 {
		t.Fatalf("candidate count = %d, want 1", len(candidates))
	}
	if candidates[0].RawContext != "prod" || candidates[0].DisplayClusterName != "cluster-one" {
		t.Fatalf("candidate = %#v", candidates[0])
	}
	encoded, err := json.Marshal(candidates)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "not-returned") {
		t.Fatal("candidate exposed credentials")
	}
}

func TestDefaultRuntimeFactoryUsesExplicitKubeconfigContext(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kubeconfig.yaml")
	raw := clientcmdapi.NewConfig()
	raw.Clusters["default-cluster"] = &clientcmdapi.Cluster{Server: "https://default.invalid"}
	raw.Clusters["selected-cluster"] = &clientcmdapi.Cluster{Server: "https://selected.invalid"}
	raw.AuthInfos["user"] = &clientcmdapi.AuthInfo{Token: "synthetic-token"}
	raw.Contexts["ambient"] = &clientcmdapi.Context{Cluster: "default-cluster", AuthInfo: "user"}
	raw.Contexts["selected"] = &clientcmdapi.Context{Cluster: "selected-cluster", AuthInfo: "user"}
	raw.CurrentContext = "ambient"
	if err := clientcmd.WriteToFile(*raw, path); err != nil {
		t.Fatal(err)
	}
	clients, err := defaultRuntimeFactory(t.Context(), ConnectionSpec{
		ID: "prod", Name: "Production",
		Source: SourceSpec{Type: SourceKubeconfig, Kubeconfig: path, Context: "selected"},
	})
	if err != nil {
		t.Fatalf("defaultRuntimeFactory: %v", err)
	}
	if clients.Config.Host != "https://selected.invalid" {
		t.Fatalf("client host = %q, want explicit selected context", clients.Config.Host)
	}
	encoded, err := json.Marshal(clients)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "synthetic-token") {
		t.Fatal("client bundle JSON exposed kubeconfig credentials")
	}
}
