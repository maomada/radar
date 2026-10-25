package issues

import (
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/issuesapi"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	"testing"
	"time"
)

func TestNativeFailuresDoNotDuplicateAfterDynamicWatch(t *testing.T) {
	defer k8s.ResetTestState()
	defer k8s.ResetTestDynamicState()
	now := metav1.NewTime(time.Now().Add(-time.Hour))
	replicas := int32(2)
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "unready", CreationTimestamp: now}, Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionFalse, Reason: "KubeletNotReady", LastTransitionTime: now}}}}
	deployment := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "unavailable", Namespace: "test", CreationTimestamp: now}, Spec: appsv1.DeploymentSpec{Replicas: &replicas}, Status: appsv1.DeploymentStatus{Replicas: 2, UnavailableReplicas: 2, Conditions: []appsv1.DeploymentCondition{{Type: appsv1.DeploymentAvailable, Status: corev1.ConditionFalse, Reason: "MinimumReplicasUnavailable", LastTransitionTime: now}}}}
	if err := k8s.InitTestResourceCache(fake.NewClientset(node, deployment)); err != nil {
		t.Fatal(err)
	}
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := appsv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	dynamic := dynamicfake.NewSimpleDynamicClient(scheme, node, deployment)
	if err := k8s.InitTestDynamicResourceCache(dynamic, []k8s.APIResource{
		{Version: "v1", Kind: "Node", Name: "nodes", Verbs: []string{"get", "list", "watch"}},
		{Group: "apps", Version: "v1", Kind: "Deployment", Name: "deployments", Namespaced: true, Verbs: []string{"get", "list", "watch"}},
	}); err != nil {
		t.Fatal(err)
	}
	provider := NewCacheProvider()
	assertFailures := func() {
		t.Helper()
		for _, grouped := range []bool{false, true} {
			counts := map[string]int{}
			for _, issue := range Compose(provider, Filters{Grouped: grouped, Limit: NoLimit}) {
				if issue.Name != node.Name && issue.Name != deployment.Name {
					continue
				}
				counts[issue.Name]++
				want := issuesapi.CategoryNodeNotReady
				if issue.Kind == "Deployment" {
					want = issuesapi.CategoryWorkloadDegraded
				}
				if issue.Source != SourceProblem || issue.Category != want || issue.Severity != SeverityCritical {
					t.Errorf("grouped=%v: wrong failure %+v", grouped, issue)
				}
			}
			if counts[node.Name] != 1 || counts[deployment.Name] != 1 {
				t.Fatalf("grouped=%v: want one critical each, got %v", grouped, counts)
			}
		}
	}
	assertFailures()
	for _, gvr := range []schema.GroupVersionResource{{Version: "v1", Resource: "nodes"}, {Group: "apps", Version: "v1", Resource: "deployments"}} {
		if _, err := k8s.GetDynamicResourceCache().ListBlocking(gvr, "", 2*time.Second); err != nil {
			t.Fatal(err)
		}
	}
	assertFailures()
}
