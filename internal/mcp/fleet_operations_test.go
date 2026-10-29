package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/fleet"
	pkgauth "github.com/skyhook-io/radar/pkg/auth"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
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
	largeInteger := strings.Replace(valid, "  replicas: 2", "  template:\n    spec:\n      securityContext:\n        runAsUser: 9007199254740993", 1)
	parsed, _, err := parseFleetManifest(largeInteger, kind, "prod", "web")
	if err != nil {
		t.Fatal(err)
	}
	spec := parsed["spec"].(map[string]any)
	template := spec["template"].(map[string]any)
	templateSpec := template["spec"].(map[string]any)
	securityContext := templateSpec["securityContext"].(map[string]any)
	if got, ok := securityContext["runAsUser"].(int64); !ok || got != 9007199254740993 {
		t.Fatalf("large integer = %#v (%T)", securityContext["runAsUser"], securityContext["runAsUser"])
	}
	for _, forbidden := range []string{
		strings.Replace(valid, "spec:\n", "status: null\nspec:\n", 1),
		strings.Replace(valid, "metadata:\n", "metadata:\n  managedFields: null\n", 1),
		strings.Replace(valid, "metadata:\n", "metadata:\n  generateName: null\n", 1),
	} {
		if _, _, err := parseFleetManifest(forbidden, kind, "prod", "web"); err == nil {
			t.Fatalf("forbidden null field accepted: %s", forbidden)
		}
	}
}

func TestFleetApplyPreviewReportsOnlyChangedStructuralPaths(t *testing.T) {
	plan := fleetPendingOperation{
		action: "apply", kind: evidenceKinds["deployments"], namespace: "prod", name: "web",
		beforeObject: map[string]any{
			"metadata": map[string]any{"resourceVersion": "41", "labels": map[string]any{"secret-key": "old"}},
			"spec":     map[string]any{"replicas": int64(1)},
		},
	}
	after := map[string]any{
		"metadata": map[string]any{"resourceVersion": "42", "labels": map[string]any{"secret-key": "credential-like"}},
		"spec":     map[string]any{"replicas": int64(2)},
		"status":   map[string]any{"readyReplicas": int64(1)},
	}
	summary := safeOperationSummary(plan, &unstructured.Unstructured{Object: after})
	text := strings.Join(summary, " ")
	if !strings.Contains(text, "metadata.labels") || !strings.Contains(text, "spec.replicas") ||
		strings.Contains(text, "secret-key") || strings.Contains(text, "credential-like") ||
		strings.Contains(text, "resourceVersion") || strings.Contains(text, "status") {
		t.Fatalf("unsafe or incomplete apply summary: %s", text)
	}
	unchanged := safeOperationSummary(plan, &unstructured.Unstructured{Object: plan.beforeObject})
	if !strings.Contains(strings.Join(unchanged, " "), "No desired-field differences") {
		t.Fatalf("no-change summary = %v", unchanged)
	}
}

func TestFleetApplyPreviewCoversFieldsOutsideDetailedAllowlist(t *testing.T) {
	for _, test := range []struct {
		resource string
		before   map[string]any
		after    map[string]any
		path     string
	}{
		{"statefulsets", map[string]any{"spec": map[string]any{"updateStrategy": map[string]any{"type": "OnDelete"}}}, map[string]any{"spec": map[string]any{"updateStrategy": map[string]any{"type": "RollingUpdate"}}}, "spec"},
		{"services", map[string]any{"spec": map[string]any{"externalName": "old.example"}}, map[string]any{"spec": map[string]any{"externalName": "private.example"}}, "spec"},
		{"pods", map[string]any{"metadata": map[string]any{"finalizers": []any{"old"}}}, map[string]any{"metadata": map[string]any{"finalizers": []any{"private-finalizer"}}}, "metadata.finalizers"},
	} {
		t.Run(test.resource, func(t *testing.T) {
			plan := fleetPendingOperation{action: "apply", kind: evidenceKinds[test.resource], beforeObject: test.before}
			summary := strings.Join(safeOperationSummary(plan, &unstructured.Unstructured{Object: test.after}), " ")
			if strings.Contains(summary, "No desired-field differences") || !strings.Contains(summary, test.path) || strings.Contains(summary, "private") {
				t.Fatalf("incomplete or unsafe summary: %s", summary)
			}
		})
	}
}

func TestFleetOperationWorkflowDryRunAndExecuteRequestParity(t *testing.T) {
	tests := []struct {
		name           string
		action         string
		absent         bool
		createConflict bool
	}{
		{name: "restart", action: "restart"},
		{name: "scale", action: "scale"},
		{name: "existing apply", action: "apply"},
		{name: "absent create", action: "apply", absent: true},
		{name: "absent create race", action: "apply", absent: true, createConflict: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			targetName := "web"
			if test.absent {
				targetName = "created"
			}
			type capturedRequest struct {
				method      string
				path        string
				contentType string
				query       url.Values
				body        []byte
			}
			var mu sync.Mutex
			requests := []capturedRequest{}
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/deployments/"+targetName) {
					if test.absent {
						w.WriteHeader(http.StatusNotFound)
						_, _ = w.Write([]byte(`{"apiVersion":"v1","kind":"Status","status":"Failure","reason":"NotFound","code":404}`))
						return
					}
					_ = json.NewEncoder(w).Encode(map[string]any{
						"apiVersion": "apps/v1", "kind": "Deployment",
						"metadata": map[string]any{"name": targetName, "namespace": "team", "uid": "current-uid", "resourceVersion": "71"},
						"spec":     map[string]any{"replicas": int64(1), "template": map[string]any{"metadata": map[string]any{}}},
					})
					return
				}
				if (r.Method == http.MethodPatch || r.Method == http.MethodPost) && strings.Contains(r.URL.Path, "/deployments") {
					body, _ := io.ReadAll(r.Body)
					mu.Lock()
					requests = append(requests, capturedRequest{
						method: r.Method, path: r.URL.Path, contentType: r.Header.Get("Content-Type"),
						query: r.URL.Query(), body: body,
					})
					mu.Unlock()
					if test.createConflict && r.Method == http.MethodPost && r.URL.Query().Get("dryRun") == "" {
						w.WriteHeader(http.StatusConflict)
						_, _ = w.Write([]byte(`{"apiVersion":"v1","kind":"Status","status":"Failure","reason":"Conflict","code":409}`))
						return
					}
					status := http.StatusOK
					if r.Method == http.MethodPost {
						status = http.StatusCreated
					}
					w.WriteHeader(status)
					_ = json.NewEncoder(w).Encode(map[string]any{
						"apiVersion": "apps/v1", "kind": "Deployment",
						"metadata": map[string]any{"name": targetName, "namespace": "team", "uid": "current-uid", "resourceVersion": "72"},
						"spec":     map[string]any{"replicas": int64(1), "template": map[string]any{"metadata": map[string]any{}}},
					})
					return
				}
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(`{"apiVersion":"v1","kind":"Status","status":"Failure","reason":"Forbidden","code":403}`))
			}))
			defer api.Close()
			manager := fleetTestManager(t, api.URL)
			clusterID := ""
			for _, status := range manager.Statuses() {
				if status.ConnectionID == "a" {
					clusterID = status.ClusterID
				}
			}
			tools := fleetTools{source: manager, writesEnabled: true, operations: newFleetOperationStore()}
			input := fleetOperationInput{ClusterID: clusterID, Action: test.action, Resource: "deployments", Namespace: "team", Name: targetName}
			switch test.action {
			case "scale":
				replicas := 3
				input.Replicas = &replicas
			case "apply":
				input.Manifest = fmt.Sprintf("apiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: %s\n  namespace: team\nspec:\n  replicas: 2\n", targetName)
			}
			preview, err := tools.prepareOperation(context.Background(), input)
			if err != nil {
				t.Fatalf("prepare: %v", err)
			}
			result, err := tools.executeOperation(context.Background(), fleetExecuteOperationInput{ClusterID: clusterID, Confirmation: preview.Confirmation})
			expectedState := "applied"
			if test.createConflict {
				expectedState = "rejected"
			}
			if err != nil || result.State != expectedState {
				t.Fatalf("execute = %+v, %v", result, err)
			}
			mu.Lock()
			got := append([]capturedRequest(nil), requests...)
			mu.Unlock()
			if len(got) != 2 {
				t.Fatalf("mutation requests = %+v, want dry-run and one actual request", got)
			}
			dryOptions := got[0].query
			executeOptions := got[1].query
			if dryOptions.Get("dryRun") != "All" {
				t.Fatalf("prepare mutation lacks dryRun=All: %v", dryOptions)
			}
			dryOptions.Del("dryRun")
			if dryOptions.Encode() != executeOptions.Encode() || got[0].method != got[1].method ||
				got[0].path != got[1].path || got[0].contentType != got[1].contentType ||
				string(got[0].body) != string(got[1].body) {
				t.Fatalf("prepare and execute mutation differ: dry=%+v execute=%+v", got[0], got[1])
			}
			if got[0].query.Get("fieldValidation") != "Strict" {
				t.Fatalf("strict validation missing: %v", got[0].query)
			}
			if test.action == "scale" && !strings.HasSuffix(got[0].path, "/scale") {
				t.Fatalf("scale did not use subresource: %s", got[0].path)
			}
			if test.action == "apply" && !test.absent && got[0].query.Get("force") != "false" {
				t.Fatalf("server-side apply force option = %q", got[0].query.Get("force"))
			}
		})
	}
}

func TestFleetOperationRejectsStaleUIDResourceVersionAndRuntime(t *testing.T) {
	tests := []struct {
		name           string
		staleUID       bool
		staleVersion   bool
		replaceRuntime bool
	}{
		{name: "UID", staleUID: true},
		{name: "resource version", staleVersion: true},
		{name: "runtime replacement", replaceRuntime: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var gets atomic.Int32
			var patches atomic.Int32
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/deployments/web") {
					count := gets.Add(1)
					uid, resourceVersion := "uid-original", "41"
					if count > 1 {
						if test.staleUID {
							uid = "uid-replaced"
						}
						if test.staleVersion {
							resourceVersion = "42"
						}
					}
					_ = json.NewEncoder(w).Encode(map[string]any{
						"apiVersion": "apps/v1", "kind": "Deployment",
						"metadata": map[string]any{"name": "web", "namespace": "team", "uid": uid, "resourceVersion": resourceVersion},
						"spec":     map[string]any{"template": map[string]any{"metadata": map[string]any{}}},
					})
					return
				}
				if r.Method == http.MethodPatch {
					if r.URL.Query().Get("dryRun") == "" {
						patches.Add(1)
					}
					_ = json.NewEncoder(w).Encode(map[string]any{
						"apiVersion": "apps/v1", "kind": "Deployment",
						"metadata": map[string]any{"name": "web", "namespace": "team", "uid": "uid-original", "resourceVersion": "42"},
					})
					return
				}
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(`{"apiVersion":"v1","kind":"Status","status":"Failure","reason":"Forbidden","code":403}`))
			}))
			defer api.Close()
			manager := fleetTestManager(t, api.URL)
			clusterID := ""
			for _, status := range manager.Statuses() {
				if status.ConnectionID == "a" {
					clusterID = status.ClusterID
				}
			}
			tools := fleetTools{source: manager, writesEnabled: true, operations: newFleetOperationStore()}
			input := fleetOperationInput{ClusterID: clusterID, Action: "restart", Resource: "deployments", Namespace: "team", Name: "web"}
			preview, err := tools.prepareOperation(context.Background(), input)
			if err != nil {
				t.Fatalf("prepare: %v", err)
			}
			if test.replaceRuntime {
				if err := manager.Refresh(context.Background(), clusterID); err != nil {
					t.Fatalf("refresh: %v", err)
				}
				deadline := time.Now().Add(5 * time.Second)
				ready := false
				for time.Now().Before(deadline) {
					for _, status := range manager.Statuses() {
						if status.ClusterID == clusterID && string(status.State) == "ready" {
							ready = true
						}
					}
					if ready {
						break
					}
					time.Sleep(10 * time.Millisecond)
				}
				if !ready {
					t.Fatalf("replacement runtime did not become ready: %+v", manager.Statuses())
				}
			}
			if _, err := tools.executeOperation(context.Background(), fleetExecuteOperationInput{ClusterID: clusterID, Confirmation: preview.Confirmation}); err == nil {
				t.Fatal("stale preview executed")
			}
			if patches.Load() != 0 {
				t.Fatalf("patches = %d after stale preview", patches.Load())
			}
		})
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

func TestFleetOperationAuditPersistenceFailures(t *testing.T) {
	for _, failBeforeMutation := range []bool{true, false} {
		t.Run(fmt.Sprintf("intent_failure=%t", failBeforeMutation), func(t *testing.T) {
			statePath := filepath.Join(t.TempDir(), "state.json")
			auditPath := statePath + ".audit.jsonl"
			var mutations atomic.Int32
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.Method == http.MethodPatch && r.URL.Query().Get("dryRun") == "" {
					mutations.Add(1)
					data, err := os.ReadFile(auditPath)
					var intent fleet.AuditRecord
					if err != nil || json.Unmarshal(data, &intent) != nil || intent.Stage != "intent" || intent.OperationID == "" {
						t.Errorf("mutation dispatched without persisted intent: %s, %v", data, err)
					}
					if err := os.Chmod(auditPath, 0644); err != nil {
						t.Errorf("cannot inject result audit failure: %v", err)
					}
				}
				_, _ = w.Write([]byte(`{"apiVersion":"apps/v1","kind":"Deployment","metadata":{"name":"web","namespace":"team","uid":"stable","resourceVersion":"42"},"spec":{"template":{"metadata":{}}}}`))
			}))
			defer api.Close()
			manager := fleetTestManagerWithEndpoints(t, map[string]string{"a": api.URL, "b": api.URL}, statePath)
			clusterID := manager.Statuses()[0].ClusterID
			tools := fleetTools{source: manager, writesEnabled: true, operations: newFleetOperationStore()}
			preview, err := tools.prepareOperation(context.Background(), fleetOperationInput{ClusterID: clusterID, Action: "restart", Resource: "deployments", Namespace: "team", Name: "web"})
			if err != nil {
				t.Fatal(err)
			}
			if failBeforeMutation {
				if err := os.WriteFile(auditPath, []byte("torn audit fixture\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			input := fleetExecuteOperationInput{ClusterID: clusterID, Confirmation: preview.Confirmation}
			result, err := tools.executeOperation(context.Background(), input)
			if failBeforeMutation {
				if err == nil || mutations.Load() != 0 {
					t.Fatalf("failed intent allowed mutation: count=%d result=%+v err=%v", mutations.Load(), result, err)
				}
			} else if err != nil || result.State != "outcome_unknown" || mutations.Load() != 1 {
				t.Fatalf("failed result persistence hid uncertainty: count=%d result=%+v err=%v", mutations.Load(), result, err)
			}
			count := mutations.Load()
			if _, err := tools.executeOperation(context.Background(), input); err == nil || mutations.Load() != count {
				t.Fatal("failed audit permitted confirmation replay")
			}
		})
	}
}

func TestFleetOperationsIsolateTwoEndpointsAndReauthorizeAdmin(t *testing.T) {
	type observed struct {
		gets, dryRuns, mutations atomic.Int32
		deny                     atomic.Bool
	}
	endpoints := map[string]string{}
	counts := map[string]*observed{"a": {}, "b": {}}
	for _, connection := range []string{"a", "b"} {
		counter := counts[connection]
		api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			if r.URL.Path != "/apis/apps/v1/namespaces/team/deployments/web" ||
				r.Header.Get("Impersonate-User") != "alice" || r.Header.Get("Impersonate-Group") != "operators" {
				t.Errorf("wrong caller or target on %s: %s, %q, %q", connection, r.URL.Path, r.Header.Get("Impersonate-User"), r.Header.Get("Impersonate-Group"))
			}
			if counter.deny.Load() {
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(`{"apiVersion":"v1","kind":"Status","status":"Failure","reason":"Forbidden","code":403,"message":"private-upstream-fixture"}`))
				return
			}
			switch {
			case r.Method == http.MethodGet:
				counter.gets.Add(1)
			case r.Method == http.MethodPatch && r.URL.Query().Get("dryRun") == "All":
				counter.dryRuns.Add(1)
			case r.Method == http.MethodPatch:
				counter.mutations.Add(1)
			default:
				t.Errorf("unexpected method: %s", r.Method)
			}
			_, _ = fmt.Fprintf(w, `{"apiVersion":"apps/v1","kind":"Deployment","metadata":{"name":"web","namespace":"team","uid":"uid-%s","resourceVersion":"42"},"spec":{"template":{"metadata":{}}}}`, connection)
		}))
		t.Cleanup(api.Close)
		endpoints[connection] = api.URL
	}
	manager := fleetTestManagerWithEndpoints(t, endpoints, filepath.Join(t.TempDir(), "state.json"))
	ids := map[string]string{}
	for _, status := range manager.Statuses() {
		ids[status.ConnectionID] = status.ClusterID
	}
	tools := fleetTools{source: manager, writesEnabled: true, requireAuth: true, operations: newFleetOperationStore(), admins: map[string]struct{}{"alice": {}}}
	ctx := pkgauth.ContextWithUser(context.Background(), &pkgauth.User{Username: "alice", Groups: []string{"operators"}})
	for _, connection := range []string{"a", "b"} {
		other := "a"
		if connection == "a" {
			other = "b"
		}
		otherGets, otherDryRuns, otherMutations := counts[other].gets.Load(), counts[other].dryRuns.Load(), counts[other].mutations.Load()
		preview, err := tools.prepareOperation(ctx, fleetOperationInput{ClusterID: ids[connection], Action: "restart", Resource: "deployments", Namespace: "team", Name: "web"})
		if err != nil || preview.ConnectionID != connection || preview.Target.UID != "uid-"+connection {
			t.Fatalf("%s preview misrouted: %+v, %v", connection, preview, err)
		}
		if _, err := tools.executeOperation(ctx, fleetExecuteOperationInput{ClusterID: ids[other], Confirmation: preview.Confirmation}); err == nil {
			t.Fatal("wrong-cluster confirmation accepted")
		}
		result, err := tools.executeOperation(ctx, fleetExecuteOperationInput{ClusterID: ids[connection], Confirmation: preview.Confirmation})
		if err != nil || result.State != "applied" || counts[connection].gets.Load() != 2 || counts[connection].dryRuns.Load() != 1 || counts[connection].mutations.Load() != 1 {
			t.Fatalf("%s execution failed: %+v, %v", connection, result, err)
		}
		if counts[other].gets.Load() != otherGets || counts[other].dryRuns.Load() != otherDryRuns || counts[other].mutations.Load() != otherMutations {
			t.Fatal("operation crossed into the other endpoint")
		}
	}
	counts["b"].deny.Store(true)
	if _, err := tools.prepareOperation(ctx, fleetOperationInput{ClusterID: ids["b"], Action: "restart", Resource: "deployments", Namespace: "team", Name: "web"}); err == nil || strings.Contains(err.Error(), "private-upstream") {
		t.Fatalf("Fleet admin bypassed target denial or received raw upstream detail: %v", err)
	}
	if counts["b"].mutations.Load() != 1 {
		t.Fatal("denied admin caused another mutation")
	}
}

func TestFleetOperationExecuteDoesNotDeadlockRefreshOrRetryMutation(t *testing.T) {
	var targetGets atomic.Int32
	var actualMutations atomic.Int32
	var denyReads atomic.Bool
	var blockExecute atomic.Bool
	executeGet := make(chan struct{}, 1)
	releaseGet := make(chan struct{})
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/deployments/web"):
			if r.Header.Get("Impersonate-User") != "alice" || r.Header.Get("Impersonate-Group") != "operators" {
				t.Errorf("operation request impersonation = %q, %q", r.Header.Get("Impersonate-User"), r.Header.Get("Impersonate-Group"))
			}
			targetGets.Add(1)
			if denyReads.Load() {
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(`{"apiVersion":"v1","kind":"Status","status":"Failure","reason":"Forbidden","code":403}`))
				return
			}
			if blockExecute.Load() {
				executeGet <- struct{}{}
				select {
				case <-releaseGet:
				case <-r.Context().Done():
				}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"apiVersion": "apps/v1", "kind": "Deployment",
				"metadata": map[string]any{"name": "web", "namespace": "team", "uid": "stable-uid", "resourceVersion": "42"},
				"spec":     map[string]any{"template": map[string]any{"metadata": map[string]any{}}},
			})
		case r.Method == http.MethodPatch:
			if r.Header.Get("Impersonate-User") != "alice" || r.Header.Get("Impersonate-Group") != "operators" {
				t.Errorf("operation request impersonation = %q, %q", r.Header.Get("Impersonate-User"), r.Header.Get("Impersonate-Group"))
			}
			if r.URL.Query().Get("dryRun") == "All" {
				_ = json.NewEncoder(w).Encode(map[string]any{
					"apiVersion": "apps/v1", "kind": "Deployment",
					"metadata": map[string]any{"name": "web", "namespace": "team", "uid": "stable-uid", "resourceVersion": "42"},
					"spec":     map[string]any{"template": map[string]any{"metadata": map[string]any{}}},
				})
				return
			}
			actualMutations.Add(1)
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"apiVersion":"v1","kind":"Status","status":"Failure","reason":"ServiceUnavailable","code":503}`))
		default:
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"apiVersion":"v1","kind":"Status","status":"Failure","reason":"Forbidden","code":403}`))
		}
	}))
	defer api.Close()
	manager := fleetTestManager(t, api.URL)
	clusterID := ""
	for _, status := range manager.Statuses() {
		if status.ConnectionID == "a" {
			clusterID = status.ClusterID
		}
	}
	if clusterID == "" {
		t.Fatal("connection a has no active cluster")
	}
	tools := fleetTools{source: manager, writesEnabled: true, requireAuth: true, operations: newFleetOperationStore()}
	ctx := pkgauth.ContextWithUser(context.Background(), &pkgauth.User{Username: "alice", Groups: []string{"operators"}})
	input := fleetOperationInput{ClusterID: clusterID, Action: "restart", Resource: "deployments", Namespace: "team", Name: "web"}
	if _, err := tools.prepareOperation(ctx, fleetOperationInput{ClusterID: clusterID, Action: "restart", Resource: "deployments", Namespace: "outside", Name: "web"}); err == nil {
		t.Fatal("namespace outside the selected runtime clamp was accepted")
	}
	denyReads.Store(true)
	if _, err := tools.prepareOperation(ctx, input); err == nil {
		t.Fatal("Kubernetes-denied caller prepared an operation")
	}
	denyReads.Store(false)
	preview, err := tools.prepareOperation(ctx, input)
	if err != nil || preview.ConnectionID != "a" || !preview.DryRun {
		t.Fatalf("prepare = %+v, %v", preview, err)
	}
	type executeResponse struct {
		result fleetOperationResult
		err    error
	}
	executed := make(chan executeResponse, 2)
	startExecute := make(chan struct{})
	inputExecute := fleetExecuteOperationInput{ClusterID: clusterID, Confirmation: preview.Confirmation}
	for i := 0; i < 2; i++ {
		go func() {
			<-startExecute
			result, err := tools.executeOperation(ctx, inputExecute)
			executed <- executeResponse{result: result, err: err}
		}()
	}
	blockExecute.Store(true)
	close(startExecute)
	select {
	case <-executeGet:
	case <-time.After(5 * time.Second):
		t.Fatal("execute target GET did not start")
	}
	refreshStarted := make(chan struct{})
	refreshDone := make(chan error, 1)
	go func() {
		close(refreshStarted)
		refreshDone <- manager.Refresh(context.Background(), clusterID)
	}()
	<-refreshStarted
	time.Sleep(50 * time.Millisecond)
	close(releaseGet)
	successfulConsumes := 0
	for i := 0; i < 2; i++ {
		select {
		case response := <-executed:
			if response.err == nil {
				successfulConsumes++
				if response.result.State != "outcome_unknown" {
					t.Fatalf("execute = %+v, %v", response.result, response.err)
				}
			}
		case <-time.After(8 * time.Second):
			t.Fatal("execute deadlocked behind queued refresh")
		}
	}
	if successfulConsumes != 1 {
		t.Fatalf("successful confirmation consumes = %d, want one", successfulConsumes)
	}
	select {
	case err := <-refreshDone:
		if err != nil {
			t.Fatalf("refresh = %v", err)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("refresh did not finish")
	}
	if actualMutations.Load() != 1 {
		t.Fatalf("actual mutation requests = %d, want exactly one", actualMutations.Load())
	}
}
