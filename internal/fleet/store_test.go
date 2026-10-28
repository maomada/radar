package fleet

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRegistryStableIDsAcrossRestartAndReorder(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "nested", "state.json")
	config := testConfig(
		ConnectionSpec{ID: "prod", Name: "Production", Source: SourceSpec{Type: SourceInCluster}},
		ConnectionSpec{ID: "stage", Name: "Staging", Source: SourceSpec{Type: SourceInCluster}},
	)
	first, err := OpenRegistry(statePath)
	if err != nil {
		t.Fatal(err)
	}
	ids, err := first.SyncConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	reordered := testConfig(config.Connections[1], config.Connections[0])
	second, err := OpenRegistry(statePath)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	restartedIDs, err := second.SyncConfig(reordered)
	if err != nil {
		t.Fatal(err)
	}
	if restartedIDs["prod"] != ids["prod"] || restartedIDs["stage"] != ids["stage"] {
		t.Fatalf("IDs changed after reorder: first=%v restarted=%v", ids, restartedIDs)
	}
	catalog := second.Snapshot()
	if len(catalog.Connections) != 2 || len(catalog.Clusters) != 2 {
		t.Fatalf("catalog = %#v", catalog)
	}
}

func TestRegistryRetainsRemovedConnectionIdentityAsInactive(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "state.json")
	registry, err := OpenRegistry(statePath)
	if err != nil {
		t.Fatal(err)
	}
	defer registry.Close()
	initial := testConfig(ConnectionSpec{ID: "prod", Name: "Production", Source: SourceSpec{Type: SourceInCluster}})
	ids, err := registry.SyncConfig(initial)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := registry.SyncConfig(Config{Version: ConfigVersion, Connections: []ConnectionSpec{}, Fleets: []FleetSpec{}}); err != nil {
		t.Fatal(err)
	}
	catalog := registry.Snapshot()
	if len(catalog.Connections) != 1 || catalog.Connections[0].Active || catalog.Connections[0].ClusterID != ids["prod"] {
		t.Fatalf("removed connection record = %#v", catalog.Connections)
	}
	if len(catalog.Clusters) != 1 || catalog.Clusters[0].Active {
		t.Fatalf("removed cluster record = %#v", catalog.Clusters)
	}
}

func TestRegistryRejectsRetargetAndRollsBackState(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "state.json")
	registry, err := OpenRegistry(statePath)
	if err != nil {
		t.Fatal(err)
	}
	defer registry.Close()
	config := testConfig(ConnectionSpec{
		ID: "prod", Name: "Production",
		Source: SourceSpec{Type: SourceKubeconfig, Kubeconfig: "/tmp/one.yaml", Context: "prod"},
	})
	ids, err := registry.SyncConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	before := registry.Snapshot()
	config.Connections[0].Source.Kubeconfig = "/tmp/two.yaml"
	if _, err := registry.SyncConfig(config); err == nil {
		t.Fatal("retarget unexpectedly succeeded")
	}
	after := registry.Snapshot()
	if after.Connections[0].ClusterID != ids["prod"] || after.Connections[0].ClusterID != before.Connections[0].ClusterID {
		t.Fatalf("retarget changed identity: before=%#v after=%#v", before, after)
	}
}

func TestRegistryIdentityConflictPreservesOriginalEvidence(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "state.json")
	registry, err := OpenRegistry(statePath)
	if err != nil {
		t.Fatal(err)
	}
	defer registry.Close()
	config := testConfig(ConnectionSpec{ID: "prod", Name: "Production", Source: SourceSpec{Type: SourceInCluster}})
	ids, err := registry.SyncConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.RecordIdentity(ids["prod"], "uid-one", testTime()); err != nil {
		t.Fatal(err)
	}
	if err := registry.RecordIdentity(ids["prod"], "uid-two", testTime()); err == nil {
		t.Fatal("conflicting identity unexpectedly succeeded")
	}
	cluster := registry.Snapshot().Clusters[0]
	if cluster.Identity != IdentityConflict || cluster.IdentityUID != "uid-one" {
		t.Fatalf("cluster evidence = %#v", cluster)
	}
}

func TestRegistryDeepCopyAndAtomicMode(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "state.json")
	registry, err := OpenRegistry(statePath)
	if err != nil {
		t.Fatal(err)
	}
	config := testConfig(ConnectionSpec{ID: "prod", Name: "Production", Source: SourceSpec{Type: SourceInCluster}})
	if _, err := registry.SyncConfig(config); err != nil {
		t.Fatal(err)
	}
	snapshot := registry.Snapshot()
	snapshot.Connections[0].Name = "mutated"
	if registry.Snapshot().Connections[0].Name == "mutated" {
		t.Fatal("Snapshot returned mutable registry storage")
	}
	if err := registry.Close(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("state mode = %o, want 600", info.Mode().Perm())
	}
}

func TestRegistryPersistenceFailureRollsBackMemory(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "state.json")
	registry, err := OpenRegistry(statePath)
	if err != nil {
		t.Fatal(err)
	}
	defer registry.Close()
	config := testConfig(ConnectionSpec{ID: "prod", Name: "Original", Source: SourceSpec{Type: SourceInCluster}})
	if _, err := registry.SyncConfig(config); err != nil {
		t.Fatal(err)
	}
	before := registry.Snapshot()
	blocker := filepath.Join(dir, "not-a-directory")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	registry.path = filepath.Join(blocker, "state.json")
	config.Connections[0].Name = "Changed"
	if _, err := registry.SyncConfig(config); err == nil {
		t.Fatal("sync succeeded despite an unwritable state location")
	}
	after := registry.Snapshot()
	if after.Connections[0].Name != before.Connections[0].Name {
		t.Fatalf("failed persistence changed memory: before=%#v after=%#v", before, after)
	}
	persisted, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	persistedState, err := decodeState(persisted)
	if err != nil || persistedState.Connections[0].Name != before.Connections[0].Name {
		t.Fatalf("pre-rename failure changed persisted state: %#v, %v", persistedState, err)
	}
}

func TestRegistryDirectorySyncFailureKeepsRenamedStateInMemory(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "state.json")
	registry, err := OpenRegistry(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := registry.SyncConfig(testConfig(ConnectionSpec{
		ID: "prod", Name: "Original", Source: SourceSpec{Type: SourceInCluster},
	})); err != nil {
		t.Fatal(err)
	}
	registry.syncDir = func(string) error {
		return errors.New("synthetic directory sync failure")
	}
	config := testConfig(ConnectionSpec{
		ID: "prod", Name: "Renamed", Source: SourceSpec{Type: SourceInCluster},
	})
	if _, err := registry.SyncConfig(config); err == nil {
		t.Fatal("sync succeeded despite the post-rename directory sync failure")
	}
	if got := registry.Snapshot().Connections[0].Name; got != "Renamed" {
		t.Fatalf("memory state = %q, want renamed state", got)
	}
	if err := registry.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenRegistry(statePath)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if got := reopened.Snapshot().Connections[0].Name; got != "Renamed" {
		t.Fatalf("disk state = %q, want renamed state", got)
	}
}

func TestIdentityPersistenceFailureDoesNotCommitEvidence(t *testing.T) {
	dir := t.TempDir()
	registry, err := OpenRegistry(filepath.Join(dir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer registry.Close()
	ids, err := registry.SyncConfig(testConfig(ConnectionSpec{
		ID: "prod", Name: "Production", Source: SourceSpec{Type: SourceInCluster},
	}))
	if err != nil {
		t.Fatal(err)
	}
	blocker := filepath.Join(dir, "not-a-directory")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	registry.path = filepath.Join(blocker, "state.json")
	if err := registry.RecordIdentity(ids["prod"], "uid-one", testTime()); err == nil {
		t.Fatal("identity persisted despite an unwritable state location")
	}
	cluster := registry.Snapshot().Clusters[0]
	if cluster.Identity != IdentityUnresolved || cluster.IdentityUID != "" {
		t.Fatalf("failed identity persistence changed evidence: %#v", cluster)
	}
}

func TestOpenRegistryRejectsCorruptState(t *testing.T) {
	for _, contents := range []string{
		`{"version":1,"connections":[],"clusters":[],"fleets":[],"memberships":[],"unknown":true}`,
		`{"version":1,"connections":[`,
	} {
		statePath := filepath.Join(t.TempDir(), "state.json")
		if err := os.WriteFile(statePath, []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
		if registry, err := OpenRegistry(statePath); err == nil {
			_ = registry.Close()
			t.Fatalf("OpenRegistry accepted corrupt state %q", contents)
		}
	}
}

func TestOpenRegistryRejectsAliasedConnectionsAndActiveMismatch(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "state.json")
	registry, err := OpenRegistry(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := registry.SyncConfig(testConfig(ConnectionSpec{
		ID: "prod", Name: "Production", Source: SourceSpec{Type: SourceInCluster},
	})); err != nil {
		t.Fatal(err)
	}
	if err := registry.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	original, err := decodeState(data)
	if err != nil {
		t.Fatal(err)
	}
	tests := map[string]func(*storedState){
		"aliased cluster": func(state *storedState) {
			alias := state.Connections[0]
			alias.ID = "alias"
			alias.Name = "Alias"
			state.Connections = append(state.Connections, alias)
		},
		"active-state mismatch": func(state *storedState) {
			state.Clusters[0].Active = !state.Connections[0].Active
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			state := cloneStoredState(original)
			mutate(&state)
			corrupt, err := json.Marshal(state)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(statePath, corrupt, 0o600); err != nil {
				t.Fatal(err)
			}
			if registry, err := OpenRegistry(statePath); err == nil {
				_ = registry.Close()
				t.Fatal("OpenRegistry accepted an invalid connection-cluster relationship")
			}
		})
	}
}

func TestRegistryEnforcesSingleWriterLock(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "state.json")
	first, err := OpenRegistry(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if second, err := OpenRegistry(statePath); err == nil {
		_ = second.Close()
		_ = first.Close()
		t.Fatal("second registry acquired the single-writer lock")
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := OpenRegistry(statePath)
	if err != nil {
		t.Fatalf("reopen after lock release: %v", err)
	}
	_ = second.Close()
}

func TestFleetMembershipMutationAPIs(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "state.json")
	registry, err := OpenRegistry(statePath)
	if err != nil {
		t.Fatal(err)
	}
	defer registry.Close()
	ids, err := registry.SyncConfig(testConfig(ConnectionSpec{
		ID: "prod", Name: "Production", Source: SourceSpec{Type: SourceInCluster},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.UpsertFleet("ops", "Operations", nil); err != nil {
		t.Fatal(err)
	}
	if err := registry.SetMembership("ops", []string{ids["prod"]}); err != nil {
		t.Fatal(err)
	}
	if got := registry.Snapshot().Memberships; len(got) != 1 || got[0].ClusterID != ids["prod"] {
		t.Fatalf("memberships after set = %#v", got)
	}
	if err := registry.Close(); err != nil {
		t.Fatal(err)
	}
	registry, err = OpenRegistry(statePath)
	if err != nil {
		t.Fatal(err)
	}
	defer registry.Close()
	if _, err := registry.SyncConfig(testConfig(ConnectionSpec{
		ID: "prod", Name: "Production", Source: SourceSpec{Type: SourceInCluster},
	})); err != nil {
		t.Fatal(err)
	}
	if catalog := registry.Snapshot(); len(catalog.Fleets) != 1 || !catalog.Fleets[0].Active || catalog.Fleets[0].Declarative {
		t.Fatalf("manual fleet was not retained across config sync: %#v", catalog.Fleets)
	}
	if err := registry.DeleteFleet("ops"); err != nil {
		t.Fatal(err)
	}
	catalog := registry.Snapshot()
	if len(catalog.Fleets) != 0 || len(catalog.Memberships) != 0 {
		t.Fatalf("catalog after delete = %#v", catalog)
	}
}

func testConfig(connections ...ConnectionSpec) Config {
	return Config{Version: ConfigVersion, Connections: connections, Fleets: []FleetSpec{}}
}

func testTime() (t time.Time) {
	return time.Unix(1700000000, 0).UTC()
}
