package insights

import (
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"testing"

	gitopstree "github.com/skyhook-io/radar/pkg/gitops/tree"
)

type recoveredResolver struct{ fakeResolver }

func (recoveredResolver) GetLive(string, string, string, string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "ConfigMap", "metadata": map[string]any{"name": "values", "namespace": "prod"}}}
}

func TestArgoPositiveLiveReadClearsEarlierMissingFlag(t *testing.T) {
	app := argoApp(map[string]any{"resources": []any{map[string]any{"kind": "ConfigMap", "namespace": "prod", "name": "values", "status": "OutOfSync"}}})
	tree := &gitopstree.ResourceTree{Nodes: []gitopstree.Node{{Ref: gitopstree.ResourceRef{Kind: "ConfigMap", Namespace: "prod", Name: "values"}, Missing: true}}}
	changes := argoResourceChanges(app, tree, &recoveredResolver{})
	if len(changes) != 1 || changes[0].Missing || !changes[0].HasLive {
		t.Fatalf("positive live read did not clear absence: %+v", changes)
	}
}

func TestArgoChangesCarryAbsenceIndependentlyOfHealth(t *testing.T) {
	app := argoApp(map[string]any{"resources": []any{map[string]any{"kind": "ConfigMap", "namespace": "prod", "name": "values", "status": "OutOfSync"}}})
	for _, health := range []string{"", "Unknown", "Healthy", "Missing"} {
		for _, missing := range []bool{true, false} {
			tree := &gitopstree.ResourceTree{Nodes: []gitopstree.Node{{Ref: gitopstree.ResourceRef{Kind: "ConfigMap", Namespace: "prod", Name: "values"}, Health: health, Missing: missing}}}
			changes := argoResourceChanges(app, tree, nil)
			if len(changes) != 1 || changes[0].Missing != missing || changes[0].HasLive == missing || changes[0].Health != health {
				t.Fatalf("health %q, missing %v: %+v", health, missing, changes)
			}
		}
	}
}
