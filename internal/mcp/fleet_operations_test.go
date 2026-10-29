package mcp

import (
	"strings"
	"testing"
	"time"
)

func TestFleetOperationManifestValidation(t *testing.T) {
	valid := "apiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: web\n  namespace: prod\nspec:\n  replicas: 2\n"
	kind := evidenceKinds["deployments"]
	if _, _, err := parseFleetManifest(valid, kind, "prod", "web"); err != nil {
		t.Fatalf("valid manifest rejected: %v", err)
	}
	for name, manifest := range map[string]string{
		"duplicate":       strings.Replace(valid, "  name: web\n", "  name: web\n  name: other\n", 1),
		"extra document":  valid + "---\n",
		"list":            strings.Replace(valid, "kind: Deployment", "kind: List", 1),
		"generated name":  strings.Replace(valid, "  name: web", "  generateName: web-", 1),
		"secret":          strings.Replace(valid, "apiVersion: apps/v1\nkind: Deployment", "apiVersion: v1\nkind: Secret", 1),
		"target mismatch": strings.Replace(valid, "name: web", "name: other", 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := parseFleetManifest(manifest, kind, "prod", "web"); err == nil {
				t.Fatal("invalid manifest accepted")
			}
		})
	}
	input := fleetOperationInput{ClusterID: "east", Action: "apply", Resource: "deployments", Namespace: "prod", Name: "web", Manifest: strings.Repeat("x", fleetManifestLimit+1)}
	if _, _, _, _, _, err := validateFleetOperationInput(input); err == nil {
		t.Fatal("oversized manifest accepted")
	}
}

func TestFleetOperationConfirmationIsOneShotAndActorBound(t *testing.T) {
	store := newFleetOperationStore()
	confirmation := strings.Repeat("a", 64)
	plan := fleetPendingOperation{
		clusterID: "east", actor: "user:alice", groups: []string{"ops"},
		expiresAt: time.Now().Add(time.Minute),
	}
	if err := store.add(confirmation, plan); err != nil {
		t.Fatal(err)
	}
	if _, err := store.consume("east", confirmation, "user:bob", []string{"ops"}, time.Now()); err == nil {
		t.Fatal("cross-actor confirmation accepted")
	}
	if _, err := store.consume("west", confirmation, "user:alice", []string{"ops"}, time.Now()); err == nil {
		t.Fatal("cross-cluster confirmation accepted")
	}
	if _, err := store.consume("east", confirmation, "user:alice", []string{"ops"}, time.Now()); err != nil {
		t.Fatalf("valid confirmation rejected: %v", err)
	}
	if _, err := store.consume("east", confirmation, "user:alice", []string{"ops"}, time.Now()); err == nil {
		t.Fatal("replayed confirmation accepted")
	}
}
