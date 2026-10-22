package mcp

import (
	"context"
	"slices"
	"testing"

	corev1 "k8s.io/api/core/v1"

	"github.com/skyhook-io/radar/internal/k8s"
	pkgauth "github.com/skyhook-io/radar/pkg/auth"
)

func podNames(pods []*corev1.Pod) []string {
	var out []string
	for _, p := range pods {
		out = append(out, p.Name)
	}
	slices.Sort(out)
	return out
}

// The inventory reads Pods' images with their pull secrets, so it needs the
// caller's own list permission on Pods, not just namespace visibility (which
// a Deployment reader also gets).
func TestRuntimePods_RequirePodListPermission(t *testing.T) {
	setupFakeCacheForFilterTests(t)
	t.Cleanup(func() { getPermCache().Invalidate() })
	cache := k8s.GetResourceCache()

	if got, _ := runtimePods(context.Background(), cache, ""); !slices.Equal(podNames(got), []string{"alpha-pod", "beta-pod", "gamma-pod"}) {
		t.Fatalf("auth off: %v", podNames(got))
	}

	ctx := withRestrictedUser(t, "alice", []string{"alpha", "beta"})
	perms := getPermCache().Get("alice", nil)
	perms.SetCanI("list", "", "pods", "alpha", true)
	perms.SetCanI("list", "", "pods", "beta", false) // visible through Deployments only
	if got, denied := runtimePods(ctx, cache, ""); !slices.Equal(podNames(got), []string{"alpha-pod"}) || denied != nil {
		t.Fatalf("namespace-scoped: %v, denied %v", podNames(got), denied)
	}
	if got, denied := runtimePods(ctx, cache, "beta"); len(got) != 0 || !slices.Equal(denied, []string{"beta"}) {
		t.Fatalf("asking for beta: %v, denied %v", podNames(got), denied)
	}
	if got, denied := runtimePods(ctx, cache, "gamma"); len(got) != 0 || !slices.Equal(denied, []string{"gamma"}) {
		t.Fatalf("asking for an invisible namespace: %v, denied %v", podNames(got), denied)
	}

	ctx = pkgauth.ContextWithUser(context.Background(), &pkgauth.User{Username: "bob"})
	bob := &pkgauth.UserPermissions{AllowedNamespaces: nil}
	bob.SetCanI("list", "", "pods", "", false)
	bob.SetCanI("list", "", "pods", "alpha", false)
	bob.SetCanI("list", "", "pods", "beta", false)
	bob.SetCanI("list", "", "pods", "gamma", true)
	getPermCache().Set("bob", nil, bob)
	if got, _ := runtimePods(ctx, cache, ""); !slices.Equal(podNames(got), []string{"gamma-pod"}) {
		t.Fatalf("cluster-wide namespace access without cluster-wide pod list: %v", podNames(got))
	}
}
