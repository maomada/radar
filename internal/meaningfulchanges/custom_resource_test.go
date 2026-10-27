package meaningfulchanges

import (
	"context"
	"fmt"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/timeline"
	"github.com/skyhook-io/radar/pkg/issuesapi"
	"github.com/skyhook-io/radar/pkg/resourceid"
)

func TestClassifyCustomResourceLifecycle(t *testing.T) {
	// Recreate rows exercise classification only; custom kinds are not
	// enrolled in the recording layer's recreate stash.
	for _, tt := range []struct {
		name     string
		op       timeline.EventType
		reason   string
		diff     *timeline.DiffInfo
		category issuesapi.ChangeCategory
	}{
		{name: "add", op: timeline.EventTypeAdd, category: issuesapi.ChangeCategoryLifecycle},
		{name: "delete", op: timeline.EventTypeDelete, category: issuesapi.ChangeCategoryLifecycle},
		{name: "recreate with diff", op: timeline.EventTypeAdd, reason: timeline.ReasonRecreated, diff: &timeline.DiffInfo{
			Fields: []timeline.FieldChange{{Path: "spec.asNumber", OldValue: int64(64512), NewValue: int64(64513)}},
		}, category: issuesapi.ChangeCategorySpecConfig},
		{name: "recreate without diff", op: timeline.EventTypeAdd, reason: timeline.ReasonRecreated, category: issuesapi.ChangeCategoryLifecycle},
	} {
		t.Run(tt.name, func(t *testing.T) {
			e := timeline.TimelineEvent{Source: timeline.SourceInformer, APIVersion: "projectcalico.org/v3", Kind: "BGPPeer", EventType: tt.op, Reason: tt.reason, Diff: tt.diff}
			if got, _ := classify(e); got != tt.category {
				t.Fatalf("classify = %q, want %q", got, tt.category)
			}
			e.Source = timeline.SourceK8sEvent
			if got, _ := classify(e); got != "" {
				t.Fatalf("non-informer lifecycle = %q, want empty", got)
			}
		})
	}
}

func TestClassifyBuiltinLifecycleExclusions(t *testing.T) {
	for _, kind := range []string{"Pod", "ReplicaSet", "Job", "Node", "Endpoints", "PersistentVolumeClaim", "ServiceAccount"} {
		for _, op := range []timeline.EventType{timeline.EventTypeAdd, timeline.EventTypeDelete} {
			if got, _ := classify(timeline.TimelineEvent{Source: timeline.SourceInformer, Kind: kind, EventType: op}); got != "" {
				t.Fatalf("%s %s = %q, want excluded", kind, op, got)
			}
		}
	}
	for _, op := range []timeline.EventType{timeline.EventTypeAdd, timeline.EventTypeDelete} {
		if got, _ := classify(timeline.TimelineEvent{Source: timeline.SourceInformer, APIVersion: "storage.k8s.io/v1", Kind: "CSINode", EventType: op}); got != "" {
			t.Fatalf("CSINode %s = %q, want excluded", op, got)
		}
	}
}

func TestRecentCustomResourceExplicitFilters(t *testing.T) {
	timeline.ResetStore()
	t.Cleanup(timeline.ResetStore)
	if err := timeline.InitStore(timeline.StoreConfig{Type: timeline.StoreTypeMemory, MaxSize: 20}); err != nil {
		t.Fatal(err)
	}
	store := timeline.GetStore()
	now := time.Now().UTC().Truncate(time.Second)
	for i, op := range []timeline.EventType{timeline.EventTypeAdd, timeline.EventTypeDelete, timeline.EventTypeAdd, timeline.EventTypeUpdate} {
		e := timeline.TimelineEvent{
			ID: string(op) + string(rune('0'+i)), Timestamp: now.Add(-time.Duration(i) * time.Minute),
			Source: timeline.SourceInformer, ClusterContext: k8s.ActiveClusterContext(),
			APIVersion: "projectcalico.org/v3", Kind: "BGPPeer", Name: "edge-peer", EventType: op,
		}
		if i == 2 {
			e.Name = "recreated-peer"
			e.Reason = timeline.ReasonRecreated
			e.Diff = &timeline.DiffInfo{Fields: []timeline.FieldChange{{Path: "spec.asNumber", OldValue: int64(64512), NewValue: int64(64513)}}, Summary: "other changes: asNumber"}
		}
		if op == timeline.EventTypeUpdate {
			e.Name = "updated-peer"
			e.Diff = &timeline.DiffInfo{Fields: []timeline.FieldChange{{Path: "spec.asNumber", OldValue: int64(64512), NewValue: int64(64513)}}, Summary: "other changes: asNumber"}
		}
		if err := store.Append(context.Background(), e); err != nil {
			t.Fatal(err)
		}
	}
	for _, tt := range []struct {
		name  string
		query Query
		want  int
	}{
		{name: "kind", query: Query{Kinds: []string{"BGPPeer"}}, want: 4},
		{name: "name", query: Query{Name: "edge-peer"}, want: 2},
		{name: "kind and name", query: Query{Kinds: []string{"BGPPeer"}, Name: "edge-peer"}, want: 2},
		{name: "unfiltered", want: 0},
		{name: "other kind", query: Query{Kinds: []string{"TidbCluster"}}, want: 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Recent(context.Background(), tt.query)
			if err != nil {
				t.Fatal(err)
			}
			if len(got.Changes) != tt.want {
				t.Fatalf("got %+v, want %d changes", got.Changes, tt.want)
			}
			for _, c := range got.Changes {
				want := issuesapi.ChangeCategoryLifecycle
				if c.Name == "recreated-peer" || c.Name == "updated-peer" {
					want = issuesapi.ChangeCategorySpecConfig
				}
				if c.ChangeCategory != want {
					t.Fatalf("change = %+v, want category %s", c, want)
				}
				if c.Name == "edge-peer" && c.ChangeType == "add" && (c.Summary != "add" || c.RankReason != lifecycleRankReason || len(c.Fields) != 0) {
					t.Fatalf("BGPPeer add output = %+v", c)
				}
			}
		})
	}
	for _, kind := range []string{"BGPPeer", "TidbCluster"} {
		if TrackedKind(kind) || TrackedKindForGroup(kind, "example.com") {
			t.Fatalf("custom kind %s must remain ineligible for negative correlation claims", kind)
		}
	}
}

func TestRecentOwnedAndCollidingCustomResources(t *testing.T) {
	for _, resource := range []struct{ kind, apiVersion string }{
		{"CertificateRequest", "cert-manager.io/v1"},
		{"NodeClaim", "karpenter.sh/v1"},
		{"NetworkPolicy", "crd.projectcalico.org/v1"},
		{"NetworkPolicy", "projectcalico.org/v3"},
		{"Service", "serving.knative.dev/v1"},
		{"Secret", "example.com/v1"},
		{"ConfigMap", "example.com/v1"},
	} {
		for _, owned := range []bool{false, true} {
			t.Run(resource.kind+"/"+resource.apiVersion+"/owned="+fmt.Sprint(owned), func(t *testing.T) {
				timeline.ResetStore()
				t.Cleanup(timeline.ResetStore)
				if err := timeline.InitStore(timeline.StoreConfig{Type: timeline.StoreTypeMemory, MaxSize: 20}); err != nil {
					t.Fatal(err)
				}
				store := timeline.GetStore()
				for i, op := range []timeline.EventType{timeline.EventTypeAdd, timeline.EventTypeUpdate, timeline.EventTypeDelete} {
					e := timeline.TimelineEvent{
						ID: string(op), Timestamp: time.Now().Add(-time.Duration(i+1) * time.Minute),
						Source: timeline.SourceInformer, ClusterContext: k8s.ActiveClusterContext(),
						Kind: resource.kind, APIVersion: resource.apiVersion, Namespace: "shop", Name: "target", EventType: op,
					}
					if owned {
						e.Owner = &timeline.OwnerInfo{Kind: "Controller", Name: "parent", UID: "parent-uid"}
					}
					if op == timeline.EventTypeUpdate {
						e.Diff = &timeline.DiffInfo{Fields: []timeline.FieldChange{
							{Path: "spec.mode", OldValue: "old", NewValue: "new"},
							{Path: "metadata.generation", OldValue: int64(1), NewValue: int64(2)},
						}}
					}
					if err := store.Append(context.Background(), e); err != nil {
						t.Fatal(err)
					}
				}
				for _, q := range []Query{{Kinds: []string{resource.kind}}, {Name: "target"}, {Kinds: []string{resource.kind}, Name: "target"}, {}} {
					got, err := Recent(context.Background(), q)
					if err != nil {
						t.Fatal(err)
					}
					want := 3
					if len(q.Kinds) == 0 && q.Name == "" {
						want = 0
					}
					if len(got.Changes) != want {
						t.Fatalf("query %+v: got %+v, want %d changes", q, got.Changes, want)
					}
					for _, c := range got.Changes {
						wantCategory := issuesapi.ChangeCategoryLifecycle
						if c.ChangeType == "update" {
							wantCategory = issuesapi.ChangeCategorySpecConfig
						}
						if c.APIVersion != resource.apiVersion || c.ChangeCategory != wantCategory {
							t.Fatalf("change = %+v, want group %s and category %s", c, resource.apiVersion, wantCategory)
						}
					}
				}
			})
		}
	}
}

func TestRecentExplicitFiltersStillExcludeManagedBuiltins(t *testing.T) {
	timeline.ResetStore()
	t.Cleanup(timeline.ResetStore)
	if err := timeline.InitStore(timeline.StoreConfig{Type: timeline.StoreTypeMemory, MaxSize: 20}); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"Deployment", "Service", "NetworkPolicy", "Pod", "ReplicaSet"} {
		apiVersion, _ := resourceid.BuiltinAPIVersion(kind)
		if err := timeline.GetStore().Append(context.Background(), timeline.TimelineEvent{
			ID: kind, Timestamp: time.Now().Add(-time.Minute), Source: timeline.SourceInformer,
			ClusterContext: k8s.ActiveClusterContext(), Kind: kind, APIVersion: apiVersion, Name: "target",
			EventType: timeline.EventTypeUpdate, Owner: &timeline.OwnerInfo{Kind: "Controller", Name: "parent"},
			Diff: &timeline.DiffInfo{Fields: []timeline.FieldChange{{Path: "spec.replicas", OldValue: 1, NewValue: 2}}},
		}); err != nil {
			t.Fatal(err)
		}
		for _, q := range []Query{{Kinds: []string{kind}}, {Name: "target"}, {Kinds: []string{kind}, Name: "target"}, {}} {
			got, err := Recent(context.Background(), q)
			if err != nil || len(got.Changes) != 0 {
				t.Fatalf("query %+v: got %+v, err %v; managed builtins must stay excluded", q, got, err)
			}
		}
	}
}

func TestRecentCustomResourceOrderOnlyIsSpecConfig(t *testing.T) {
	timeline.ResetStore()
	t.Cleanup(timeline.ResetStore)
	if err := timeline.InitStore(timeline.StoreConfig{Type: timeline.StoreTypeMemory, MaxSize: 20}); err != nil {
		t.Fatal(err)
	}
	old := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "networking.istio.io/v1", "kind": "VirtualService",
		"spec": map[string]any{"http": []any{map[string]any{"name": "specific"}, map[string]any{"name": "fallback"}}},
	}}
	old.SetGeneration(1)
	new := old.DeepCopy()
	new.SetGeneration(2)
	routes := new.Object["spec"].(map[string]any)["http"].([]any)
	routes[0], routes[1] = routes[1], routes[0]
	if err := timeline.GetStore().Append(context.Background(), timeline.TimelineEvent{
		ID: "order", Timestamp: time.Now().Add(-time.Minute), Source: timeline.SourceInformer,
		ClusterContext: k8s.ActiveClusterContext(), Kind: "VirtualService", APIVersion: old.GetAPIVersion(), Name: "target",
		EventType: timeline.EventTypeUpdate, Diff: k8s.ComputeDiff("VirtualService", old, new),
	}); err != nil {
		t.Fatal(err)
	}
	got, err := Recent(context.Background(), Query{Kinds: []string{"VirtualService"}, Name: "target"})
	if err != nil || len(got.Changes) != 1 {
		t.Fatalf("got %+v, err %v; want order-only change", got, err)
	}
	c := got.Changes[0]
	if c.ChangeCategory != issuesapi.ChangeCategorySpecConfig || len(c.Fields) != 2 || c.Fields[0].Path != "spec (order or structure changed)" || c.Fields[1].Path != "metadata.generation" {
		t.Fatalf("order-only change = %+v", c)
	}
}

func TestRecentExplicitManagedBuiltinChurnDoesNotCrowdOrSaturate(t *testing.T) {
	timeline.ResetStore()
	t.Cleanup(timeline.ResetStore)
	if err := timeline.InitStore(timeline.StoreConfig{Type: timeline.StoreTypeMemory, MaxSize: 2 * NamedFetchBound}); err != nil {
		t.Fatal(err)
	}
	store := timeline.GetStore()
	for i := 0; i < NamedFetchBound+2; i++ {
		e := timeline.TimelineEvent{
			ID: fmt.Sprint(i), Timestamp: time.Now().Add(-time.Duration(i+1) * time.Second),
			Source: timeline.SourceInformer, ClusterContext: k8s.ActiveClusterContext(),
			Kind: "Service", APIVersion: "v1", Name: "target", EventType: timeline.EventTypeUpdate,
			Owner: &timeline.OwnerInfo{Kind: "Controller", Name: "parent"},
			Diff:  &timeline.DiffInfo{Fields: []timeline.FieldChange{{Path: "spec.type", OldValue: "ClusterIP", NewValue: "NodePort"}}},
		}
		if i > NamedFetchBound {
			e.Owner = nil
		}
		if err := store.Append(context.Background(), e); err != nil {
			t.Fatal(err)
		}
	}
	got, err := Recent(context.Background(), Query{Kinds: []string{"Service"}, Name: "target"})
	if err != nil || len(got.Changes) != 1 || got.FetchSaturated {
		t.Fatalf("got %+v, err %v; managed builtin churn must not hide the unowned row or saturate", got, err)
	}
}

func TestRecentExplicitManagedBuiltinScanIsBounded(t *testing.T) {
	timeline.ResetStore()
	t.Cleanup(timeline.ResetStore)
	if err := timeline.InitStore(timeline.StoreConfig{Type: timeline.StoreTypeMemory, MaxSize: 10 * NamedFetchBound}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10*NamedFetchBound; i++ {
		if err := timeline.GetStore().Append(context.Background(), timeline.TimelineEvent{
			ID: fmt.Sprint(i), Timestamp: time.Now().Add(-time.Duration(i) * time.Second),
			Source: timeline.SourceInformer, ClusterContext: k8s.ActiveClusterContext(),
			Kind: "Pod", APIVersion: "v1", Name: "target", EventType: timeline.EventTypeUpdate,
		}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := Recent(context.Background(), Query{Kinds: []string{"Pod"}, Name: "target"})
	if err != nil || len(got.Changes) != 0 || !got.FetchSaturated {
		t.Fatalf("got %+v, err %v; capped raw scan must report saturation even when all rows are excluded", got, err)
	}
}

func TestRecentCollidingResourceIdentitiesStaySeparate(t *testing.T) {
	timeline.ResetStore()
	t.Cleanup(timeline.ResetStore)
	if err := timeline.InitStore(timeline.StoreConfig{Type: timeline.StoreTypeMemory, MaxSize: 20}); err != nil {
		t.Fatal(err)
	}
	for _, apiVersion := range []string{"v1", "serving.knative.dev/v1"} {
		e := timeline.TimelineEvent{
			ID:        apiVersion,
			Timestamp: time.Now().UTC().Truncate(time.Minute).Add(-time.Minute),
			Source:    timeline.SourceInformer, ClusterContext: k8s.ActiveClusterContext(),
			Kind: "Service", APIVersion: apiVersion, Namespace: "shop", Name: "target", EventType: timeline.EventTypeDelete,
		}
		if err := timeline.GetStore().Append(context.Background(), e); err != nil {
			t.Fatal(err)
		}
	}
	if err := timeline.GetStore().Append(context.Background(), timeline.TimelineEvent{
		ID: "recreated", Timestamp: time.Now(), Source: timeline.SourceInformer,
		ClusterContext: k8s.ActiveClusterContext(), Kind: "Service", APIVersion: "v1", Namespace: "shop", Name: "target",
		EventType: timeline.EventTypeAdd, Reason: timeline.ReasonRecreated,
	}); err != nil {
		t.Fatal(err)
	}
	got, err := Recent(context.Background(), Query{Kinds: []string{"Service"}, Name: "target"})
	if err != nil || len(got.Changes) != 2 {
		t.Fatalf("got %+v, err %v; want core recreate and custom delete", got, err)
	}
	for _, c := range got.Changes {
		if c.APIVersion == "v1" && c.ChangeType != "add" || c.APIVersion == "serving.knative.dev/v1" && c.ChangeType != "delete" {
			t.Fatalf("colliding history coalesced across groups: %+v", c)
		}
	}
}

func TestDedupeEventsWithoutIDsKeepsDistinctGroups(t *testing.T) {
	stamp := time.Now()
	var events []timeline.TimelineEvent
	for _, apiVersion := range []string{"v1", "serving.knative.dev/v1", "serving.knative.dev/v2"} {
		events = append(events, timeline.TimelineEvent{
			Timestamp: stamp, Kind: "Service", Namespace: "shop", Name: "target", APIVersion: apiVersion, EventType: timeline.EventTypeDelete,
		})
	}
	got := dedupeEvents(events)
	if len(got) != 2 || got[0].APIVersion != "v1" || got[1].APIVersion != "serving.knative.dev/v1" {
		t.Fatalf("deduped events = %+v, want two distinct groups with versions sharing identity", got)
	}
}
