package issues

import (
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"testing"
	"time"
)

func TestUnmanagedCollectorRetainedConditionsDoNotCreateIssues(t *testing.T) {
	for _, tc := range []struct {
		name, group, management string
		want                    int
	}{
		{"managed failure", "opentelemetry.io", "managed", 1},
		{"unmanaged old failure", "opentelemetry.io", "unmanaged", 0},
		{"foreign kind remains generic", "other.example", "unmanaged", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u := &unstructured.Unstructured{Object: map[string]any{"apiVersion": tc.group + "/v1beta1", "kind": "OpenTelemetryCollector", "metadata": map[string]any{"name": "test", "namespace": "test", "generation": int64(2)}, "spec": map[string]any{"managementState": tc.management}, "status": map[string]any{"observedGeneration": int64(2), "conditions": []any{map[string]any{"type": "Ready", "status": "False", "reason": "ReconcileError", "lastTransitionTime": time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)}}}}}
			gvr := schema.GroupVersionResource{Group: tc.group, Version: "v1beta1", Resource: "opentelemetrycollectors"}
			p := &fakeProvider{dynamic: map[schema.GroupVersionResource][]*unstructured.Unstructured{gvr: {u}}, kinds: map[schema.GroupVersionResource]string{gvr: "OpenTelemetryCollector"}, namespaced: map[schema.GroupVersionResource]bool{gvr: true}}
			if got := Compose(p, Filters{}); len(got) != tc.want {
				t.Fatalf("got %+v; want %d", got, tc.want)
			}
		})
	}
}
