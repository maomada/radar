package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/fleet"
	"github.com/skyhook-io/radar/pkg/health"
	"github.com/skyhook-io/radar/pkg/resourceid"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
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

type fleetRuntimeSwapSource struct {
	first       fleetSource
	replacement fleetSource
	calls       int
}

func (s *fleetRuntimeSwapSource) Catalog() fleet.Catalog {
	return s.first.Catalog()
}

func (s *fleetRuntimeSwapSource) Statuses() []fleet.ConnectionStatus {
	return s.first.Statuses()
}

func (s *fleetRuntimeSwapSource) Runtime(clusterID string) (*fleet.ClusterRuntime, error) {
	s.calls++
	if s.calls == 1 {
		return s.first.Runtime(clusterID)
	}
	return s.replacement.Runtime(clusterID)
}

func TestFleetEvidenceRedaction(t *testing.T) {
	object := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "Pod",
		"metadata": map[string]any{
			"name": "web", "namespace": "prod",
			"managedFields": []any{"private"},
			"annotations":   map[string]any{"kubectl.kubernetes.io/last-applied-configuration": "password=do-not-return"},
		},
		"spec": map[string]any{"password": "do-not-return", "containers": []any{map[string]any{"name": "web", "env": []any{map[string]any{"name": "TOKEN", "value": "do-not-return"}}}}},
	}}
	data, err := json.Marshal(sanitizeFleetObject(object))
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, secret := range []string{"do-not-return", "managedFields", "last-applied"} {
		if strings.Contains(text, secret) {
			t.Fatalf("sanitized object contains %q: %s", secret, text)
		}
	}
}

func TestFleetLogsContainerValidation(t *testing.T) {
	pod := &corev1.Pod{Spec: corev1.PodSpec{
		Containers:          []corev1.Container{{Name: "app"}},
		InitContainers:      []corev1.Container{{Name: "init"}},
		EphemeralContainers: []corev1.EphemeralContainer{{EphemeralContainerCommon: corev1.EphemeralContainerCommon{Name: "debug"}}},
	}}
	for _, name := range []string{"app", "init", "debug"} {
		if !fleetContainerExists(pod, name) {
			t.Fatalf("container %q was rejected", name)
		}
	}
	if fleetContainerExists(pod, "missing") {
		t.Fatal("missing container accepted")
	}
}

func TestFleetDiagnosisMissingFieldsIsUnknown(t *testing.T) {
	object := &unstructured.Unstructured{Object: map[string]any{"kind": "Pod", "metadata": map[string]any{"uid": "u"}}}
	level, complete, finding := classifyFleetObject(object, "Pod", time.Now())
	if complete || finding != nil || level != health.LevelUnknown {
		t.Fatalf("missing phase classified as %s complete=%v finding=%+v", level, complete, finding)
	}
}

func TestFleetTopologyReferentialIntegrity(t *testing.T) {
	resources := fleetRelationshipsResult{Relationships: []fleetRelationship{
		{
			ID: "distribution:one", Type: "declared_distribution", SourceClusterID: "a",
			SourceResource: &fleetResource{ClusterID: "a", Ref: resourceid.NewRef("", "Pod", "team", "source"), UID: "source-uid"},
			TargetResource: &fleetResource{ClusterID: "b", Ref: resourceid.NewRef("", "Pod", "team", "target"), UID: "target-uid"},
			State:          "observed", Evidence: []fleetResource{{UID: "binding-uid"}},
		},
		{
			ID: "identity:unresolved", Type: "declared_identity_match", SourceClusterID: "a",
			MemberName: "member", State: "unresolved", Evidence: []fleetResource{{UID: "cluster-uid"}},
		},
		{
			ID: "identity:ambiguous", Type: "declared_identity_match", SourceClusterID: "a",
			TargetCandidates: []string{"a", "b"}, State: "ambiguous", Evidence: []fleetResource{{UID: "cluster-uid"}},
		},
	}}
	result := buildFleetTopology([]fleet.ConnectionStatus{{ClusterID: "a"}, {ClusterID: "b"}}, resources)
	nodes := map[string]bool{}
	for _, node := range result.Nodes {
		nodes[node.ID] = true
	}
	for _, edge := range result.Edges {
		if !nodes[edge.Source] || !nodes[edge.Target] {
			t.Fatalf("edge references missing node: %+v", edge)
		}
	}
}

func TestFleetEvidenceUsesSelectedRuntimeAndScope(t *testing.T) {
	manager := fleetTestManager(t, "")
	tools := fleetTools{source: manager}
	statuses := manager.Statuses()
	if len(statuses) != 2 {
		t.Fatalf("statuses = %+v", statuses)
	}
	seen := map[string]string{}
	for _, status := range statuses {
		_, result, err := tools.getFleetResource(context.Background(), nil, fleetEvidenceInput{
			ClusterID: status.ClusterID, Resource: "pods", Namespace: "team", Name: "same-name", UID: status.ConnectionID,
		})
		if err != nil || result.State != "available" || result.Target.UID != status.ConnectionID {
			t.Fatalf("explicit target %s = %+v, %v", status.ClusterID, result, err)
		}
		seen[result.Target.ClusterID] = result.Target.UID
		_, stale, err := tools.getFleetResource(context.Background(), nil, fleetEvidenceInput{
			ClusterID: status.ClusterID, Resource: "pods", Namespace: "team", Name: "same-name", UID: "other-uid",
		})
		if err != nil || stale.State != "stale" || stale.Object != nil {
			t.Fatalf("UID mismatch = %+v, %v", stale, err)
		}
		_, outOfScope, err := tools.getFleetResource(context.Background(), nil, fleetEvidenceInput{
			ClusterID: status.ClusterID, Resource: "pods", Namespace: "outside", Name: "same-name",
		})
		if err != nil || outOfScope.State != "out_of_scope" || outOfScope.Object != nil {
			t.Fatalf("out-of-scope read = %+v, %v", outOfScope, err)
		}
	}
	if len(seen) != 2 || seen[statuses[0].ClusterID] == seen[statuses[1].ClusterID] {
		t.Fatalf("same-name resources were routed to one identity: %v", seen)
	}
	_, secret, err := tools.getFleetResource(context.Background(), nil, fleetEvidenceInput{
		ClusterID: statuses[0].ClusterID, Resource: "secrets", Namespace: "team", Name: "credential",
	})
	if err != nil || secret.State != "unsupported" || secret.Object != nil {
		t.Fatalf("Secret read was not denied: %+v, %v", secret, err)
	}
	_, unknown, err := tools.getFleetResource(context.Background(), nil, fleetEvidenceInput{
		ClusterID: statuses[0].ClusterID, Resource: "widgets", Namespace: "team", Name: "unknown",
	})
	if err != nil || unknown.State != "unsupported" || unknown.Object != nil {
		t.Fatalf("unknown resource was not rejected: %+v, %v", unknown, err)
	}
}

func TestFleetEvidenceDenialDoesNotExposeAPIError(t *testing.T) {
	manager := fleetTestManager(t, "")
	status := manager.Statuses()[0]
	cluster, err := manager.Runtime(status.ClusterID)
	if err != nil {
		t.Fatal(err)
	}
	client := cluster.Dynamic().(*dynamicfake.FakeDynamicClient)
	client.PrependReactor("get", "pods", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "pods"}, "same-name", errors.New("sensitive raw api detail"))
	})
	_, result, err := (fleetTools{source: manager}).getFleetResource(context.Background(), nil, fleetEvidenceInput{
		ClusterID: status.ClusterID, Resource: "pods", Namespace: "team", Name: "same-name",
	})
	if err != nil || result.State != "denied" || strings.Contains(strings.Join(result.Limitations, " "), "sensitive raw api detail") {
		t.Fatalf("denied read leaked API error: %+v, %v", result, err)
	}
}

func TestFleetEventsVerifyUIDReturnedByAPI(t *testing.T) {
	manager := fleetTestManager(t, "")
	status := manager.Statuses()[0]
	cluster, err := manager.Runtime(status.ClusterID)
	if err != nil {
		t.Fatal(err)
	}
	typed := cluster.Kubernetes().(*kubernetesfake.Clientset)
	typed.PrependReactor("list", "events", func(action k8stesting.Action) (bool, runtime.Object, error) {
		listAction, ok := action.(k8stesting.ListAction)
		if !ok || listAction.GetListRestrictions().Fields.String() != "involvedObject.uid="+status.ConnectionID {
			t.Errorf("events were not UID-scoped: %#v", action)
		}
		return true, &corev1.EventList{Items: []corev1.Event{
			{ObjectMeta: metav1.ObjectMeta{Namespace: "team", UID: types.UID("wrong-event")}, InvolvedObject: corev1.ObjectReference{UID: "other-pod"}, Message: "must not leak"},
			{ObjectMeta: metav1.ObjectMeta{Namespace: "team", UID: types.UID("right-event")}, InvolvedObject: corev1.ObjectReference{UID: types.UID(status.ConnectionID)}, Type: corev1.EventTypeWarning, Reason: "BackOff", Message: "waiting"},
		}}, nil
	})
	_, result, err := (fleetTools{source: manager}).getFleetEvents(context.Background(), nil, fleetEventsInput{
		fleetEvidenceInput: fleetEvidenceInput{ClusterID: status.ClusterID, Resource: "pods", Namespace: "team", Name: "same-name", UID: status.ConnectionID},
	})
	if err != nil || result.State != "available" || len(result.Events) != 1 || result.Events[0].UID != "right-event" {
		t.Fatalf("UID-bound events = %+v, %v", result, err)
	}
}

func TestFleetLogsSuppressTextWhenPodUIDChanges(t *testing.T) {
	manager := fleetTestManager(t, "")
	status := manager.Statuses()[0]
	cluster, err := manager.Runtime(status.ClusterID)
	if err != nil {
		t.Fatal(err)
	}
	dynamicClient := cluster.Dynamic()
	pod, err := dynamicClient.Resource(schema.GroupVersionResource{Version: "v1", Resource: "pods"}).Namespace("team").Get(context.Background(), "same-name", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := unstructured.SetNestedSlice(pod.Object, []any{map[string]any{"name": "app"}}, "spec", "containers"); err != nil {
		t.Fatal(err)
	}
	if _, err := dynamicClient.Resource(schema.GroupVersionResource{Version: "v1", Resource: "pods"}).Namespace("team").Update(context.Background(), pod, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	typed := cluster.Kubernetes().(*kubernetesfake.Clientset)
	typed.PrependReactor("get", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
		if action.GetSubresource() == "log" {
			return true, &runtime.Unknown{Raw: []byte("private log text")}, nil
		}
		return true, &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "same-name", Namespace: "team", UID: "replacement"}}, nil
	})
	_, result, err := (fleetTools{source: manager}).getFleetLogs(context.Background(), nil, fleetLogsInput{
		ClusterID: status.ClusterID, Namespace: "team", Name: "same-name", Container: "app",
	})
	if err != nil || result.State != "stale" || result.Text != "" {
		t.Fatalf("stale log read = %+v, %v", result, err)
	}
}

func TestFleetRelationshipsDistinguishDeclaredAndObservedEndpoints(t *testing.T) {
	pods := schema.GroupVersionResource{Version: "v1", Resource: "pods"}
	makePod := func(uid string) *unstructured.Unstructured {
		return &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "v1", "kind": "Pod",
			"metadata": map[string]any{"name": "app", "namespace": "team", "uid": uid},
		}}
	}
	newClient := func(objects ...*unstructured.Unstructured) *dynamicfake.FakeDynamicClient {
		runtimeObjects := make([]runtime.Object, len(objects))
		for i, object := range objects {
			runtimeObjects[i] = object
		}
		return dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{pods: "PodList"}, runtimeObjects...)
	}
	sourceClient, targetClient := newClient(makePod("source-uid")), newClient()
	binding := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "work.karmada.io/v1alpha2", "kind": "ResourceBinding",
		"metadata": map[string]any{"name": "binding", "namespace": "team", "uid": "binding-uid", "resourceVersion": "9"},
		"spec": map[string]any{
			"resource": map[string]any{"apiVersion": "v1", "kind": "Pod", "namespace": "team", "name": "app", "uid": "source-uid"},
			"clusters": []any{map[string]any{"name": "member"}},
		},
	}}
	sourceStatus := fleet.ConnectionStatus{ClusterID: "source-cluster", ConnectionID: "source-connection"}
	targetStatus := fleet.ConnectionStatus{ClusterID: "target-cluster", ConnectionID: "target-connection"}
	tools := fleetTools{}
	clients := map[string]dynamic.Interface{"source-cluster": sourceClient, "target-cluster": targetClient}
	namespaces := map[string][]string{"source-cluster": {"team"}, "target-cluster": {"team"}}
	identities := map[string][]string{"source-cluster:member": {"target-cluster"}}
	statuses := map[string]fleet.ConnectionStatus{"source-cluster": sourceStatus, "target-cluster": targetStatus}
	declared := tools.bindingRelationship(context.Background(), sourceStatus, "ResourceBinding", binding, "member", clients, namespaces, identities, statuses)
	if declared == nil || declared.State != "unresolved" || declared.SourceResource == nil || declared.TargetResource != nil {
		t.Fatalf("declared but unobserved relationship = %+v", declared)
	}
	if _, err := targetClient.Resource(pods).Namespace("team").Create(context.Background(), makePod("target-uid"), metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	observed := tools.bindingRelationship(context.Background(), sourceStatus, "ResourceBinding", binding, "member", clients, namespaces, identities, statuses)
	if observed == nil || observed.State != "observed" || observed.SourceResource == nil || observed.TargetResource == nil ||
		observed.SourceResource.ClusterID != sourceStatus.ClusterID || observed.TargetResource.ClusterID != targetStatus.ClusterID ||
		observed.TargetResource.ConnectionID != targetStatus.ConnectionID {
		t.Fatalf("authorized observed endpoints = %+v", observed)
	}
}

func TestFleetRelationshipsIdentityMatchingStaysConservative(t *testing.T) {
	freshUIDs := map[string][]string{"namespace-uid": {"a", "b"}, "": {"must-not-match"}}
	if got := fleetIdentityCandidates("namespace-uid", freshUIDs); len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("duplicate identity candidates = %v", got)
	}
	if got := fleetIdentityCandidates("custom-id", freshUIDs); len(got) != 0 {
		t.Fatalf("custom ID was treated as identity evidence: %v", got)
	}
	if got := fleetIdentityCandidates("", freshUIDs); len(got) != 0 {
		t.Fatalf("empty ID was treated as identity evidence: %v", got)
	}
}

func TestFleetRelationshipsIdentityReadHonorsNamespaceClamp(t *testing.T) {
	manager := fleetTestManager(t, "")
	_, result, err := (fleetTools{source: manager}).getFleetRelationships(context.Background(), nil, fleetSelection{})
	if err != nil {
		t.Fatal(err)
	}
	for _, status := range manager.Statuses() {
		found := false
		for _, coverage := range result.Coverage {
			if coverage.ClusterID == status.ClusterID && coverage.Resource == "identity" {
				found = true
				if coverage.State != "out_of_scope" {
					t.Errorf("identity read escaped namespace clamp: %+v", coverage)
				}
			}
		}
		if !found {
			t.Errorf("missing identity coverage for %s: %+v", status.ClusterID, result.Coverage)
		}
	}
}

func TestFleetRelationshipsResolveOnlyFreshNamespaceUIDs(t *testing.T) {
	config := fleet.Config{Version: 1, Connections: []fleet.ConnectionSpec{
		{ID: "a", Name: "A", Source: fleet.SourceSpec{Type: fleet.SourceInCluster}, Namespaces: []string{"kube-system", "team"}},
		{ID: "b", Name: "B", Source: fleet.SourceSpec{Type: fleet.SourceInCluster}, Namespaces: []string{"kube-system", "team"}},
	}, Fleets: []fleet.FleetSpec{}}
	clustersGVR := karmadaClustersGVR
	listKinds := map[schema.GroupVersionResource]string{
		clustersGVR:                       "ClusterList",
		karmadaResourceBindingsGVR:        "ResourceBindingList",
		karmadaClusterResourceBindingsGVR: "ClusterResourceBindingList",
	}
	factory := func(_ context.Context, spec fleet.ConnectionSpec) (fleet.RuntimeClients, error) {
		clusterA := &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "cluster.karmada.io/v1alpha1", "kind": "Cluster",
			"metadata": map[string]any{"name": "shared-" + spec.ID, "uid": "cluster-" + spec.ID},
			"spec":     map[string]any{"id": "fresh-kube-system-uid"},
		}}
		clusterB := &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "cluster.karmada.io/v1alpha1", "kind": "Cluster",
			"metadata": map[string]any{"name": "a", "uid": "custom-" + spec.ID},
			"spec":     map[string]any{"id": "operator-custom-id"},
		}}
		var client dynamic.Interface = dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), listKinds, clusterA, clusterB)
		typed := kubernetesfake.NewSimpleClientset(
			&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "kube-system", UID: "fresh-kube-system-uid"}},
			&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "team", UID: "team-uid"}},
		)
		return fleet.RuntimeClients{
			Typed: typed, Dynamic: client,
			Discovery: &discoveryfake.FakeDiscovery{Fake: &k8stesting.Fake{}},
			Config:    &rest.Config{},
		}, nil
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
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	for _, status := range manager.Statuses() {
		if status.State != fleet.StateReady {
			t.Fatalf("fake runtime not ready: %+v", manager.Statuses())
		}
	}
	_, result, err := (fleetTools{source: manager}).getFleetRelationships(context.Background(), nil, fleetSelection{})
	if err != nil {
		t.Fatal(err)
	}
	matches, custom := 0, 0
	for _, relationship := range result.Relationships {
		if relationship.Type != "declared_identity_match" {
			continue
		}
		switch relationship.MemberName {
		case "shared-a", "shared-b":
			matches++
			if relationship.State != "ambiguous" || len(relationship.TargetCandidates) != 2 ||
				relationship.TargetCandidates[0] >= relationship.TargetCandidates[1] {
				t.Fatalf("duplicate UID match was not retained as ambiguous: %+v", relationship)
			}
		case "a":
			custom++
			if relationship.State != "unresolved" || relationship.TargetClusterID != "" || len(relationship.TargetCandidates) != 0 {
				t.Fatalf("custom ID/name was incorrectly matched: %+v", relationship)
			}
		}
	}
	if matches != 2 || custom != 2 {
		t.Fatalf("identity results missing: duplicate=%d custom=%d relationships=%+v", matches, custom, result.Relationships)
	}
}

func TestFleetDiagnosisUsesCanonicalHealthAndObservedFields(t *testing.T) {
	now := time.Now().UTC()
	crashing := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "Pod",
		"metadata": map[string]any{"name": "api", "namespace": "team", "uid": "pod-uid"},
		"status": map[string]any{
			"phase":      "Running",
			"conditions": []any{map[string]any{"type": "Ready", "status": "False"}},
			"containerStatuses": []any{map[string]any{
				"name": "app", "ready": false,
				"state": map[string]any{"waiting": map[string]any{"reason": "CrashLoopBackOff"}},
			}},
		},
	}}
	level, complete, finding := classifyFleetObject(crashing, "Pod", now)
	if !complete || finding == nil || finding.Code != "crash_loop_back_off" || level == "unknown" {
		t.Fatalf("canonical Pod diagnosis = (%s, %v, %+v)", level, complete, finding)
	}
	historicalPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{UID: "historical-pod"},
		Status: corev1.PodStatus{
			Phase:      corev1.PodRunning,
			Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}},
			ContainerStatuses: []corev1.ContainerStatus{{
				Name: "app", Ready: true,
				State:                corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
				LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "OOMKilled"}},
			}},
		},
	}
	historicalObject, err := runtime.DefaultUnstructuredConverter.ToUnstructured(historicalPod)
	if err != nil {
		t.Fatal(err)
	}
	historicalLevel, complete, historicalFinding := classifyFleetObject(&unstructured.Unstructured{Object: historicalObject}, "Pod", now)
	if !complete || historicalLevel != health.Pod(historicalPod, now).Level ||
		historicalFinding == nil || historicalFinding.Code != "historical_oom_killed" {
		t.Fatalf("historical OOM changed current health: (%s, %v, %+v)", historicalLevel, complete, historicalFinding)
	}

	staleDeployment := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apps/v1", "kind": "Deployment",
		"metadata": map[string]any{"name": "api", "namespace": "team", "generation": int64(3)},
		"spec":     map[string]any{"replicas": int64(3)},
		"status":   map[string]any{"observedGeneration": int64(2), "availableReplicas": int64(0)},
	}}
	level, complete, finding = classifyFleetObject(staleDeployment, "Deployment", now)
	if complete || level != "unknown" || finding != nil {
		t.Fatalf("stale Deployment status treated as current: (%s, %v, %+v)", level, complete, finding)
	}
}

func TestFleetOverviewPartialAssessmentRemainsUnknown(t *testing.T) {
	now := time.Now().UTC()
	partial := []fleetCoverage{{Resource: "pods", State: "partial"}}
	state, findings, coverage, truncated := assessFleetObjects(map[string][]unstructured.Unstructured{}, partial, now)
	if state != "unknown" || findings == nil || len(findings) != 0 || coverage[0].State != "partial" || truncated {
		t.Fatalf("partial empty assessment = %s, %+v, %+v, truncated=%v", state, findings, coverage, truncated)
	}
	state, _, _, _ = assessFleetObjects(map[string][]unstructured.Unstructured{}, []fleetCoverage{}, now)
	if state != "unknown" {
		t.Fatalf("empty inventory was healthy: %s", state)
	}
}

func TestFleetOverviewReturnsPartialCoverageAsUnknown(t *testing.T) {
	manager := fleetTestManager(t, "")
	status := manager.Statuses()[0]
	cluster, err := manager.Runtime(status.ClusterID)
	if err != nil {
		t.Fatal(err)
	}
	dynamicClient := cluster.Dynamic().(*dynamicfake.FakeDynamicClient)
	dynamicClient.PrependReactor("list", "pods", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "pods"}, "", errors.New("raw forbidden detail"))
	})
	_, result, err := (fleetTools{source: manager}).overview(context.Background(), nil, fleetSelection{ClusterIDs: []string{status.ClusterID}})
	if err != nil || len(result.Clusters) != 1 {
		t.Fatalf("overview = %+v, %v", result, err)
	}
	row := result.Clusters[0]
	if row.AssessmentState != "unknown" || row.Health != "unknown" || row.Findings == nil ||
		len(row.AssessmentCoverage) != 3 || row.AssessmentCoverage[0].State != "denied" {
		t.Fatalf("partial overview was presented as complete: %+v", row)
	}
}

func TestFleetDiagnosisDTOContract(t *testing.T) {
	result := fleetDiagnosisResult{
		Target: fleetResource{ClusterID: "cluster"},
		State:  "unknown", Findings: []fleetFinding{}, Limitations: []string{},
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatal(err)
	}
	expected := []string{"target", "state", "findings", "limitations"}
	if len(fields) != len(expected) {
		t.Fatalf("diagnosis DTO has unexpected fields: %s", encoded)
	}
	for _, field := range expected {
		if len(fields[field]) == 0 || string(fields[field]) == "null" {
			t.Fatalf("diagnosis DTO field %q is missing or null: %s", field, encoded)
		}
	}
}

func TestFleetEvidenceAPIQueriesUseSharedHandlers(t *testing.T) {
	manager := fleetTestManager(t, "")
	status := manager.Statuses()[0]
	_, api := NewFleetEndpoints(manager, FleetOptions{})
	requests := []struct {
		tool string
		body string
	}{
		{"get_fleet_resource", `{"clusterId":"` + status.ClusterID + `","resource":"pods","namespace":"team","name":"same-name"}`},
		{"get_fleet_events", `{"clusterId":"` + status.ClusterID + `","resource":"pods","namespace":"team","name":"same-name","uid":"` + status.ConnectionID + `"}`},
		{"get_fleet_logs", `{"clusterId":"` + status.ClusterID + `","namespace":"team","name":"same-name","container":"missing"}`},
		{"diagnose_fleet_resource", `{"clusterId":"` + status.ClusterID + `","resource":"pods","namespace":"team","name":"same-name"}`},
		{"get_fleet_relationships", `{}`},
		{"get_fleet_topology", `{}`},
	}
	for _, item := range requests {
		t.Run(item.tool, func(t *testing.T) {
			response := callFleetAPI(t, api, http.MethodPost, "/query/"+item.tool, item.body, "")
			if response.Code != http.StatusOK {
				t.Fatalf("%s status=%d: %s", item.tool, response.Code, response.Body.String())
			}
			if response.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("%s response missing no-store: %v", item.tool, response.Header())
			}
			var result map[string]json.RawMessage
			if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
				t.Fatalf("%s returned invalid JSON: %v", item.tool, err)
			}
			if len(result) == 0 {
				t.Fatalf("%s returned an empty result", item.tool)
			}
		})
	}
}

func TestFleetEvidenceOperationCapturesRuntimeOnce(t *testing.T) {
	first := fleetTestManager(t, "")
	replacement := fleetTestManager(t, "")
	status := first.Statuses()[0]
	source := &fleetRuntimeSwapSource{first: first, replacement: replacement}
	_, result, err := (fleetTools{source: source}).getFleetEvents(context.Background(), nil, fleetEventsInput{
		fleetEvidenceInput: fleetEvidenceInput{ClusterID: status.ClusterID, Resource: "pods", Namespace: "team", Name: "same-name", UID: status.ConnectionID},
	})
	if err != nil || result.State != "available" || source.calls != 1 || len(result.Events) != 0 {
		t.Fatalf("runtime was reacquired or event read failed: calls=%d result=%+v err=%v", source.calls, result, err)
	}
}

func TestFleetEventsMixedNamespaceCoverageIsPartial(t *testing.T) {
	manager := fleetTestManager(t, "")
	status := manager.Statuses()[0]
	cluster, err := manager.Runtime(status.ClusterID)
	if err != nil {
		t.Fatal(err)
	}
	typed := cluster.Kubernetes().(*kubernetesfake.Clientset)
	typed.PrependReactor("list", "events", func(action k8stesting.Action) (bool, runtime.Object, error) {
		if action.GetNamespace() == "other" {
			return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "events"}, "", errors.New("hidden"))
		}
		return true, &corev1.EventList{Items: []corev1.Event{{
			ObjectMeta:     metav1.ObjectMeta{Namespace: "team", UID: "event-uid"},
			InvolvedObject: corev1.ObjectReference{UID: types.UID("node-uid")},
		}}}, nil
	})
	target := fleetResource{ClusterID: status.ClusterID, ConnectionID: status.ConnectionID}
	object := &unstructured.Unstructured{Object: map[string]any{"metadata": map[string]any{"uid": "node-uid", "name": "node"}}}
	result := collectFleetEvents(context.Background(), fleetEventsInput{
		fleetEvidenceInput: fleetEvidenceInput{ClusterID: status.ClusterID, Resource: "nodes", Name: "node", UID: "node-uid"},
		Limit:              20,
	}, target, object, evidenceKind{Kind: "Node"}, fleetClientBundle{
		Status: status, Typed: typed, Namespaces: []string{"team", "other"},
	}, false)
	if result.State != "partial" || len(result.Events) != 1 || len(result.Limitations) == 0 {
		t.Fatalf("mixed namespace event coverage = %+v", result)
	}
}

func TestFleetDiagnosisCompletenessGates(t *testing.T) {
	missingWorkloadFields := map[string]map[string]any{
		"Deployment": {
			"apiVersion": "apps/v1", "kind": "Deployment",
			"metadata": map[string]any{"generation": int64(1)},
			"spec":     map[string]any{"replicas": int64(1)},
			"status":   map[string]any{"observedGeneration": int64(1)},
		},
		"StatefulSet": {
			"apiVersion": "apps/v1", "kind": "StatefulSet",
			"metadata": map[string]any{"generation": int64(1)},
			"spec":     map[string]any{"replicas": int64(1)},
			"status":   map[string]any{"observedGeneration": int64(1)},
		},
		"ReplicaSet": {
			"apiVersion": "apps/v1", "kind": "ReplicaSet",
			"metadata": map[string]any{"generation": int64(1)},
			"spec":     map[string]any{"replicas": int64(1)},
			"status":   map[string]any{"observedGeneration": int64(1)},
		},
		"DaemonSet": {
			"apiVersion": "apps/v1", "kind": "DaemonSet",
			"metadata": map[string]any{"generation": int64(1)},
			"status":   map[string]any{"observedGeneration": int64(1), "desiredNumberScheduled": int64(1)},
		},
	}
	for kind, object := range missingWorkloadFields {
		level, complete, finding := classifyFleetObject(&unstructured.Unstructured{Object: object}, kind, time.Now())
		if complete || finding != nil || level != health.LevelUnknown {
			t.Errorf("%s missing replica fields = (%s, %v, %+v)", kind, level, complete, finding)
		}
	}
	runningMissingReady := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "Pod",
		"metadata": map[string]any{"uid": "running"},
		"status":   map[string]any{"phase": "Running"},
	}}
	if level, complete, finding := classifyFleetObject(runningMissingReady, "Pod", time.Now()); complete || finding != nil || level != health.LevelUnknown {
		t.Fatalf("running Pod without Ready condition = (%s, %v, %+v)", level, complete, finding)
	}
	pendingMissingCreation := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "Pod",
		"metadata": map[string]any{"uid": "pending"},
		"status":   map[string]any{"phase": "Pending"},
	}}
	if level, complete, finding := classifyFleetObject(pendingMissingCreation, "Pod", time.Now()); complete || finding != nil || level != health.LevelUnknown {
		t.Fatalf("Pending Pod without creation timestamp = (%s, %v, %+v)", level, complete, finding)
	}
	completed := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "Pod",
		"metadata": map[string]any{"uid": "completed"},
		"status": map[string]any{
			"phase":      "Succeeded",
			"conditions": []any{map[string]any{"type": "Ready", "status": "False", "message": "normal completion"}},
		},
	}}
	if level, complete, finding := classifyFleetObject(completed, "Pod", time.Now()); !complete || level != health.LevelNeutral || finding != nil {
		t.Fatalf("completed Pod readiness was diagnosed: (%s, %v, %+v)", level, complete, finding)
	}
}

func TestFleetDiagnosisRedactsConditionMessage(t *testing.T) {
	secret := "Authorization: Bearer eyJhbGciOiJSUzI1NiIsInR5cCI6IkpXVCJ9"
	object := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "Pod",
		"metadata": map[string]any{"uid": "pod"},
		"status": map[string]any{
			"phase":      "Running",
			"conditions": []any{map[string]any{"type": "Ready", "status": "False", "message": secret}},
		},
	}}
	_, complete, finding := classifyFleetObject(object, "Pod", time.Now())
	if !complete || finding == nil || strings.Contains(finding.Summary, "eyJhbGci") || !strings.Contains(finding.Summary, "[REDACTED]") {
		t.Fatalf("condition message was not redacted: complete=%v finding=%+v", complete, finding)
	}
}

func TestFleetLogsFinalRedactionCap(t *testing.T) {
	text, truncated := truncateFleetText(strings.Repeat("x", 64*1024+1), 64*1024)
	if !truncated || len(text) != 64*1024 {
		t.Fatalf("final log cap = len %d truncated=%v", len(text), truncated)
	}
}

func TestFleetOverviewFindingsHaveDeterministicResourceEvidence(t *testing.T) {
	now := time.Now().UTC()
	makePod := func(namespace, uid, resourceVersion string) unstructured.Unstructured {
		return unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "v1", "kind": "Pod",
			"metadata": map[string]any{"name": "api", "namespace": namespace, "uid": uid, "resourceVersion": resourceVersion},
			"status": map[string]any{
				"phase":      "Running",
				"conditions": []any{map[string]any{"type": "Ready", "status": "True"}},
				"containerStatuses": []any{map[string]any{
					"name": "app", "state": map[string]any{"waiting": map[string]any{"reason": "CrashLoopBackOff"}},
				}},
			},
		}}
	}
	state, findings, _, _ := assessFleetObjects(map[string][]unstructured.Unstructured{
		"Pod": {makePod("zeta", "z-uid", "2"), makePod("alpha", "a-uid", "1")},
	}, []fleetCoverage{{Resource: "pods", State: "complete"}}, now)
	if state != "observed_issues" || len(findings) != 2 {
		t.Fatalf("overview findings = state %s findings %+v", state, findings)
	}
	if !strings.Contains(findings[0].Evidence[len(findings[0].Evidence)-1], "namespace=alpha") ||
		!strings.Contains(findings[1].Evidence[len(findings[1].Evidence)-1], "namespace=zeta") {
		t.Fatalf("overview findings were not sorted by namespace: %+v", findings)
	}
	for _, finding := range findings {
		evidence := finding.Evidence[len(finding.Evidence)-1]
		for _, field := range []string{"group=", "kind=Pod", "namespace=", "name=api", "uid=", "resourceVersion="} {
			if !strings.Contains(evidence, field) {
				t.Errorf("resource evidence missing %q: %s", field, evidence)
			}
		}
	}
}

func TestFleetRelationshipsCollectBindingsWhenClusterListDenied(t *testing.T) {
	manager := fleetTestManager(t, "")
	status := manager.Statuses()[0]
	cluster, err := manager.Runtime(status.ClusterID)
	if err != nil {
		t.Fatal(err)
	}
	client := cluster.Dynamic().(*dynamicfake.FakeDynamicClient)
	binding := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "work.karmada.io/v1alpha2", "kind": "ResourceBinding",
		"metadata": map[string]any{"name": "binding", "namespace": "team", "uid": "binding-uid", "resourceVersion": "1"},
		"spec": map[string]any{
			"resource": map[string]any{"apiVersion": "v1", "kind": "Pod", "namespace": "team", "name": "same-name", "uid": status.ConnectionID},
			"clusters": []any{map[string]any{"name": "member"}},
		},
	}}
	if _, err := client.Resource(karmadaResourceBindingsGVR).Namespace("team").Create(context.Background(), binding, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	client.PrependReactor("list", "clusters", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Group: "cluster.karmada.io", Resource: "clusters"}, "", errors.New("cluster list denied"))
	})
	_, result, err := (fleetTools{source: manager}).getFleetRelationships(context.Background(), nil, fleetSelection{ClusterIDs: []string{status.ClusterID}})
	if err != nil {
		t.Fatal(err)
	}
	coverage := map[string]string{}
	for _, item := range result.Coverage {
		coverage[item.Resource] = item.State
	}
	if coverage["clusters"] != "denied" || coverage["resourcebindings"] != "complete" || coverage["clusterresourcebindings"] != "complete" {
		t.Fatalf("independent relationship coverage = %+v", result.Coverage)
	}
	found := false
	for _, relationship := range result.Relationships {
		if relationship.Type == "declared_distribution" {
			found = true
			if relationship.State != "unresolved" || relationship.TargetClusterID != "" || len(relationship.TargetCandidates) != 0 {
				t.Fatalf("denied identity was guessed from binding = %+v", relationship)
			}
		}
	}
	if !found {
		t.Fatalf("readable ResourceBinding did not produce declared unresolved evidence: %+v", result)
	}
	if !strings.Contains(strings.Join(result.Limitations, " "), "delivery") {
		t.Fatalf("relationship endpoint limitation missing: %+v", result.Limitations)
	}
}
