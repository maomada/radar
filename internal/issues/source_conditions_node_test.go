package issues

import (
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/issuesapi"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	"strconv"
	"testing"
	"time"
)

func TestNodeRemovalConditionNoise(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	pending, unavailable := k8s.KindPending, k8s.KindUnavailable
	for _, tc := range []struct {
		name, group, root                                                                                   string
		readiness                                                                                           *k8s.KindReadiness
		old, priorOutage, unknownReady, unknownStart, otherFailure, pressure, withheld, noReadinessProvider bool
		want                                                                                                []string
	}{
		{name: "expected removal"},
		{name: "expected removal keeps independent error", otherFailure: true, want: []string{"Healthy: ControllerError"}},
		{name: "pressure remains critical", pressure: true, root: "MemoryPressure", want: []string{"MemoryPressure"}},
		{name: "pending typed pressure fallback", pressure: true, readiness: &pending, want: []string{"Ready: KubeletNotReady"}},
		{name: "disabled typed pressure fallback", pressure: true, readiness: &unavailable, want: []string{"Ready: KubeletNotReady"}},
		{name: "unknown typed provider fallback", noReadinessProvider: true, want: []string{"Ready: KubeletNotReady"}},
		{name: "outage before removal", priorOutage: true, want: []string{"Ready: KubeletNotReady"}},
		{name: "unknown readiness timing", unknownReady: true, want: []string{"Ready: KubeletNotReady"}},
		{name: "Karpenter removal without start", unknownStart: true, want: []string{"Ready: KubeletNotReady"}},
		{name: "overdue fallback", old: true, want: []string{"Ready: KubeletNotReady"}},
		{name: "overdue native root", old: true, root: "Removal delayed", want: []string{"Removal delayed"}},
		{name: "overdue withheld root", old: true, root: "Removal delayed", withheld: true, want: []string{"Ready: KubeletNotReady"}},
		{name: "colliding Node kind", group: "example.com", want: []string{"Ready: KubeletNotReady"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			start := now.Add(-5 * time.Minute)
			if tc.old {
				start = now.Add(-11 * time.Minute)
			}
			readyAt := start.Add(time.Minute)
			if tc.priorOutage {
				readyAt = start.Add(-time.Minute)
			}
			ready := map[string]any{"type": "Ready", "status": "False", "reason": "KubeletNotReady"}
			if !tc.unknownReady {
				ready["lastTransitionTime"] = readyAt.Format(time.RFC3339)
			}
			conds := []map[string]any{ready}
			if tc.pressure {
				conds = append(conds, map[string]any{"type": "MemoryPressure", "status": "True"})
			}
			if tc.otherFailure {
				conds = append(conds, map[string]any{"type": "Healthy", "status": "False", "reason": "ControllerError"})
			}
			u := rolloutWithConditions(conds)
			u.SetName("removing")
			u.SetCreationTimestamp(metav1.NewTime(now.Add(-2 * time.Hour)))
			taint := map[string]any{"key": "ToBeDeletedByClusterAutoscaler", "effect": "NoSchedule", "value": strconv.FormatInt(start.Unix(), 10)}
			if tc.unknownStart {
				taint = map[string]any{"key": "karpenter.sh/disrupted", "effect": "NoSchedule"}
			}
			u.Object["spec"] = map[string]any{"taints": []any{taint}}
			gvr := schema.GroupVersionResource{Group: tc.group, Version: "v1", Resource: "nodes"}
			p := &fakeProvider{dynamic: map[schema.GroupVersionResource][]*unstructured.Unstructured{gvr: {u}}, kinds: map[schema.GroupVersionResource]string{gvr: "Node"}, namespaced: map[schema.GroupVersionResource]bool{gvr: false}}
			if tc.root != "" {
				d := k8s.Detection{Kind: "Node", Name: "removing", Reason: tc.root, Severity: "critical"}
				if tc.withheld {
					d.RequiredReads = []issuesapi.EvidenceRead{{Resource: "secrets", Namespace: "test", Verb: "get"}}
				}
				p.problems = []k8s.Detection{d}
			}
			state := k8s.KindReady
			if tc.readiness != nil {
				state = *tc.readiness
			}
			var provider Provider = &loadingProvider{fakeProvider: p, readiness: map[string]k8s.KindReadiness{"nodes": state}}
			if tc.noReadinessProvider {
				provider = p
			}
			for _, grouped := range []bool{false, true} {
				f := Filters{Kinds: []string{"Node"}, Grouped: grouped}
				if tc.withheld {
					f.CanReadEvidence = func(r EvidenceRead) bool { return r.Resource != "secrets" }
				}
				out, stats := ComposeWithStats(provider, f)
				if len(out) != len(tc.want) {
					t.Fatalf("grouped=%v want%v got%+v", grouped, tc.want, out)
				}
				for _, want := range tc.want {
					found := false
					for _, i := range out {
						if i.Reason == want {
							found = true
						}
						if i.Source == SourceProblem && i.Severity != SeverityCritical {
							t.Errorf("native severity lost %+v", i)
						}
					}
					if !found {
						t.Fatalf("missing%q: %+v", want, out)
					}
				}
				if state == k8s.KindPending && len(stats.Unchecked) == 0 {
					t.Fatal("pending native coverage omitted")
				}
			}
		})
	}
}

func TestExpectedNodeRemovalAfterDynamicWatch(t *testing.T) {
	defer k8s.ResetTestState()
	defer k8s.ResetTestDynamicState()
	now := time.Now()
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "expected-removal", CreationTimestamp: metav1.NewTime(now.Add(-time.Hour))}, Spec: corev1.NodeSpec{Taints: []corev1.Taint{{Key: "ToBeDeletedByClusterAutoscaler", Value: strconv.FormatInt(now.Add(-5*time.Minute).Unix(), 10), Effect: corev1.TaintEffectNoSchedule}}}, Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionFalse, Reason: "KubeletNotReady", LastTransitionTime: metav1.NewTime(now.Add(-4 * time.Minute))}}}}
	pressure := node.DeepCopy()
	pressure.Name = "pressure-control"
	pressure.Status.Conditions = append(pressure.Status.Conditions, corev1.NodeCondition{Type: corev1.NodeMemoryPressure, Status: corev1.ConditionTrue})
	custom := node.DeepCopy()
	custom.Name = "custom-failure-control"
	custom.Status.Conditions = append(custom.Status.Conditions, corev1.NodeCondition{Type: "Healthy", Status: corev1.ConditionFalse, Reason: "ControllerError"})
	prior := node.DeepCopy()
	prior.Name = "prior-outage-control"
	prior.Status.Conditions[0].LastTransitionTime = metav1.NewTime(now.Add(-6 * time.Minute))
	if err := k8s.InitTestResourceCache(fake.NewClientset(node, pressure, prior, custom)); err != nil {
		t.Fatal(err)
	}
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := k8s.InitTestDynamicResourceCache(dynamicfake.NewSimpleDynamicClient(scheme, node, pressure, prior, custom), []k8s.APIResource{{Version: "v1", Kind: "Node", Name: "nodes", Verbs: []string{"get", "list", "watch"}}}); err != nil {
		t.Fatal(err)
	}
	provider := NewCacheProvider()
	check := func(warm bool) {
		t.Helper()
		for _, grouped := range []bool{false, true} {
			found := map[string]bool{}
			foundCustom := false
			for _, i := range Compose(provider, Filters{Kinds: []string{"Node"}, Grouped: grouped}) {
				if i.Name == custom.Name && i.Source == SourceCondition && i.Reason == "Healthy: ControllerError" {
					foundCustom = true
				}
				if i.Name == node.Name {
					t.Errorf("expected readiness loss became issue %+v", i)
				}
				if i.Source == SourceProblem && i.Severity == SeverityCritical {
					found[i.Name] = true
				}
			}
			if warm && !foundCustom {
				t.Fatal("dynamic custom-condition failure was not scanned")
			}
			if !found[pressure.Name] || !found[prior.Name] {
				t.Fatalf("real failures lost: %v", found)
			}
		}
	}
	check(false)
	if _, err := k8s.GetDynamicResourceCache().ListBlocking(schema.GroupVersionResource{Version: "v1", Resource: "nodes"}, "", 2*time.Second); err != nil {
		t.Fatal(err)
	}
	check(true)
}
