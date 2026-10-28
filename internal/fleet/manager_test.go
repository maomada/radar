package fleet

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	discoveryfake "k8s.io/client-go/discovery/fake"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes"
	kubernetesfake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func TestManagerPeerFailureIsolationAndExplicitRuntime(t *testing.T) {
	config := testConfig(
		ConnectionSpec{ID: "good", Name: "Good", Source: SourceSpec{Type: SourceInCluster}},
		ConnectionSpec{ID: "bad", Name: "Bad", Source: SourceSpec{Type: SourceInCluster}},
	)
	goodClients := testRuntimeClients("good-uid")
	factory := func(_ context.Context, spec ConnectionSpec) (RuntimeClients, error) {
		if spec.ID == "bad" {
			return RuntimeClients{}, errors.New("synthetic unavailable")
		}
		return goodClients, nil
	}
	manager, err := New(config, filepath.Join(t.TempDir(), "state.json"),
		WithRuntimeFactory(factory), WithCacheSyncTimeout(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	if err := manager.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	goodID := manager.clusterIDs["good"]
	badID := manager.clusterIDs["bad"]
	good := waitForStatus(t, manager, goodID)
	bad := waitForStatus(t, manager, badID)
	if good.State != StateReady {
		t.Fatalf("good status = %#v", good)
	}
	if bad.State != StateUnavailable {
		t.Fatalf("bad status = %#v", bad)
	}
	if _, err := manager.Runtime(goodID); err != nil {
		t.Fatalf("Runtime(good): %v", err)
	}
	if _, err := manager.Runtime(badID); err == nil {
		t.Fatal("Runtime(bad) unexpectedly succeeded")
	}
}

func TestManagerSameUIDConnectionsRemainSeparateDuplicateCandidates(t *testing.T) {
	config := testConfig(
		ConnectionSpec{ID: "one", Name: "One", Source: SourceSpec{Type: SourceInCluster}},
		ConnectionSpec{ID: "two", Name: "Two", Source: SourceSpec{Type: SourceInCluster}},
	)
	factory := func(_ context.Context, _ ConnectionSpec) (RuntimeClients, error) {
		return testRuntimeClients("cloned-uid"), nil
	}
	manager, err := New(config, filepath.Join(t.TempDir(), "state.json"),
		WithRuntimeFactory(factory), WithCacheSyncTimeout(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	if err := manager.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	one := waitForStatus(t, manager, manager.clusterIDs["one"])
	two := waitForStatus(t, manager, manager.clusterIDs["two"])
	if len(one.DuplicateCandidates) != 1 || len(two.DuplicateCandidates) != 1 {
		t.Fatalf("duplicate candidates one=%#v two=%#v", one, two)
	}
	if one.ClusterID == two.ClusterID {
		t.Fatal("same UID caused automatic cluster merge")
	}
	oneRuntime, err := manager.Runtime(one.ClusterID)
	if err != nil {
		t.Fatal(err)
	}
	twoRuntime, err := manager.Runtime(two.ClusterID)
	if err != nil {
		t.Fatal(err)
	}
	if oneRuntime == twoRuntime || oneRuntime.Cache() == twoRuntime.Cache() {
		t.Fatal("equal UIDs shared a runtime or cache")
	}
}

func TestManagerUIDConflictFailsClosed(t *testing.T) {
	config := testConfig(ConnectionSpec{ID: "prod", Name: "Production", Source: SourceSpec{Type: SourceInCluster}})
	var mu sync.RWMutex
	uid := "uid-one"
	factory := func(_ context.Context, _ ConnectionSpec) (RuntimeClients, error) {
		mu.RLock()
		current := uid
		mu.RUnlock()
		return testRuntimeClients(current), nil
	}
	manager, err := New(config, filepath.Join(t.TempDir(), "state.json"),
		WithRuntimeFactory(factory), WithCacheSyncTimeout(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	if err := manager.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	clusterID := manager.clusterIDs["prod"]
	if status := waitForStatus(t, manager, clusterID); status.State != StateReady {
		t.Fatalf("initial status = %#v", status)
	}
	mu.Lock()
	uid = "uid-two"
	mu.Unlock()
	if err := manager.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	status := waitForState(t, manager, clusterID, StateIdentityConflict)
	if status.Identity != IdentityConflict {
		t.Fatalf("conflict status = %#v", status)
	}
	if _, err := manager.Runtime(clusterID); err == nil {
		t.Fatal("conflicting runtime unexpectedly routable")
	}
}

func TestManagerRefreshReplacesOnlySelectedRuntime(t *testing.T) {
	config := testConfig(
		ConnectionSpec{ID: "one", Name: "One", Source: SourceSpec{Type: SourceInCluster}},
		ConnectionSpec{ID: "two", Name: "Two", Source: SourceSpec{Type: SourceInCluster}},
	)
	var calls atomic.Int64
	factory := func(_ context.Context, spec ConnectionSpec) (RuntimeClients, error) {
		calls.Add(1)
		return testRuntimeClients("uid-" + spec.ID), nil
	}
	manager, err := New(config, filepath.Join(t.TempDir(), "state.json"),
		WithRuntimeFactory(factory), WithCacheSyncTimeout(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	if err := manager.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	oneID := manager.clusterIDs["one"]
	twoID := manager.clusterIDs["two"]
	if status := waitForStatus(t, manager, oneID); status.State != StateReady {
		t.Fatalf("one initial status = %#v", status)
	}
	if status := waitForStatus(t, manager, twoID); status.State != StateReady {
		t.Fatalf("two initial status = %#v", status)
	}
	waitForStartup(t, manager)
	oneBefore, err := manager.Runtime(oneID)
	if err != nil {
		t.Fatal(err)
	}
	twoBefore, err := manager.Runtime(twoID)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Refresh(context.Background(), oneID); err != nil {
		t.Fatal(err)
	}
	if status := waitForStatus(t, manager, oneID); status.State != StateReady {
		t.Fatalf("one refreshed status = %#v", status)
	}
	oneAfter, err := manager.Runtime(oneID)
	if err != nil {
		t.Fatal(err)
	}
	twoAfter, err := manager.Runtime(twoID)
	if err != nil {
		t.Fatal(err)
	}
	if oneBefore == oneAfter {
		t.Fatal("refresh reused the previous runtime")
	}
	if twoBefore != twoAfter {
		t.Fatal("refresh replaced an unrelated runtime")
	}
	if calls.Load() != 3 {
		t.Fatalf("runtime factory calls = %d, want 3", calls.Load())
	}
}

func TestCollectionProbeHardClampsExplicitNamespaces(t *testing.T) {
	client := kubernetesfake.NewSimpleClientset()
	client.PrependReactor("list", "pods", func(action k8stesting.Action) (bool, k8sruntime.Object, error) {
		list := action.(k8stesting.ListAction)
		if list.GetNamespace() == "" {
			return true, nil, apierrors.NewForbidden(corev1.Resource("pods"), "", errors.New("cluster list denied"))
		}
		return false, nil, nil
	})
	coverage, scopes, namespaces := probeCollectionAccess(context.Background(), client, []string{"team-a"})
	for _, kind := range coverage {
		if kind.Kind != "pods" {
			continue
		}
		if kind.Probe != ProbeAllowed || kind.Scope != "namespaces" {
			t.Fatalf("pods coverage = %#v", kind)
		}
	}
	if scopes["pods"].Namespace != "team-a" || len(namespaces["pods"]) != 1 {
		t.Fatalf("pods scope = %#v namespaces=%#v", scopes["pods"], namespaces["pods"])
	}
	for _, action := range client.Actions() {
		if action.GetVerb() == "list" && action.GetResource().Resource == "pods" && action.(k8stesting.ListAction).GetNamespace() == "" {
			t.Fatal("explicit namespace probe attempted a cluster-wide pod list")
		}
	}
}

func TestCollectionProbeSeparatesDeniedAndUnavailableKinds(t *testing.T) {
	client := kubernetesfake.NewSimpleClientset()
	client.PrependReactor("list", "services", func(k8stesting.Action) (bool, k8sruntime.Object, error) {
		return true, nil, apierrors.NewForbidden(corev1.Resource("services"), "", errors.New("denied"))
	})
	client.PrependReactor("list", "nodes", func(k8stesting.Action) (bool, k8sruntime.Object, error) {
		return true, nil, errors.New("synthetic transport failure")
	})
	client.PrependReactor("list", "pods", func(k8stesting.Action) (bool, k8sruntime.Object, error) {
		return true, nil, apierrors.NewUnauthorized("synthetic unauthenticated")
	})
	coverage, _, _ := probeCollectionAccess(context.Background(), client, nil)
	outcomes := make(map[string]ProbeStatus, len(coverage))
	for _, kind := range coverage {
		outcomes[kind.Kind] = kind.Probe
	}
	if outcomes["services"] != ProbeDenied || outcomes["nodes"] != ProbeUnavailable || outcomes["pods"] != ProbeUnauthenticated {
		t.Fatalf("probe outcomes = %v", outcomes)
	}
	clusterWide, _, _ := probeCollectionAccess(context.Background(), kubernetesfake.NewSimpleClientset(), nil)
	for _, kind := range clusterWide {
		if kind.Kind == "pods" && kind.Scope != "cluster" {
			t.Fatalf("unscoped namespaced coverage = %#v", kind)
		}
	}
}

func TestDistinctUnresolvedConnectionsKeepSeparateCachesAndSameNamedPods(t *testing.T) {
	config := testConfig(
		ConnectionSpec{ID: "one", Name: "One", Source: SourceSpec{Type: SourceInCluster}, Namespaces: []string{"default", "denied"}},
		ConnectionSpec{ID: "two", Name: "Two", Source: SourceSpec{Type: SourceInCluster}, Namespaces: []string{"default", "denied"}},
	)
	clients := map[string]RuntimeClients{
		"one": testRuntimeClientsWithPod("uid-one", "cluster-one"),
		"two": testRuntimeClientsWithPod("uid-two", "cluster-two"),
	}
	for _, bundle := range clients {
		client := bundle.Typed.(*kubernetesfake.Clientset)
		client.PrependReactor("list", "pods", func(action k8stesting.Action) (bool, k8sruntime.Object, error) {
			if action.(k8stesting.ListAction).GetNamespace() == "denied" {
				return true, nil, apierrors.NewForbidden(corev1.Resource("pods"), "", errors.New("synthetic denied"))
			}
			return false, nil, nil
		})
	}
	manager, err := New(config, filepath.Join(t.TempDir(), "state.json"),
		WithRuntimeFactory(func(_ context.Context, spec ConnectionSpec) (RuntimeClients, error) {
			return clients[spec.ID], nil
		}),
		WithIdentityProbe(func(context.Context, kubernetes.Interface) (string, error) {
			return "", errors.New("synthetic identity probe failure")
		}),
		WithCacheSyncTimeout(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	if err := manager.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	oneStatus := waitForStatus(t, manager, manager.clusterIDs["one"])
	twoStatus := waitForStatus(t, manager, manager.clusterIDs["two"])
	if oneStatus.Identity != IdentityUnresolved || twoStatus.Identity != IdentityUnresolved {
		t.Fatalf("unresolved statuses one=%#v two=%#v", oneStatus, twoStatus)
	}
	oneRuntime, err := manager.Runtime(oneStatus.ClusterID)
	if err != nil {
		t.Fatal(err)
	}
	twoRuntime, err := manager.Runtime(twoStatus.ClusterID)
	if err != nil {
		t.Fatal(err)
	}
	if oneRuntime.Cache() == twoRuntime.Cache() {
		t.Fatal("separate connections shared a resource cache")
	}
	clamp := oneRuntime.Namespaces()
	if len(clamp) != 2 || clamp[0] != "default" || clamp[1] != "denied" {
		t.Fatalf("runtime namespace clamp = %v", clamp)
	}
	clamp[0] = "widened"
	if oneRuntime.Namespaces()[0] != "default" {
		t.Fatal("Namespaces returned mutable runtime state")
	}
	onePod, err := oneRuntime.Kubernetes().CoreV1().Pods("default").Get(context.Background(), "shared-name", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	twoPod, err := twoRuntime.Kubernetes().CoreV1().Pods("default").Get(context.Background(), "shared-name", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if onePod.Labels["cluster"] != "cluster-one" || twoPod.Labels["cluster"] != "cluster-two" {
		t.Fatalf("same-named pod clients crossed: one=%v two=%v", onePod.Labels, twoPod.Labels)
	}
}

func TestManagerStartConcurrencyIsBounded(t *testing.T) {
	connections := make([]ConnectionSpec, 8)
	for i := range connections {
		connections[i] = ConnectionSpec{
			ID:     fmt.Sprintf("conn-%d", i),
			Name:   fmt.Sprintf("Connection %d", i),
			Source: SourceSpec{Type: SourceInCluster},
		}
	}
	var active atomic.Int32
	var maximum atomic.Int32
	entered := make(chan struct{}, len(connections))
	release := make(chan struct{})
	factory := func(context.Context, ConnectionSpec) (RuntimeClients, error) {
		count := active.Add(1)
		for {
			old := maximum.Load()
			if count <= old || maximum.CompareAndSwap(old, count) {
				break
			}
		}
		entered <- struct{}{}
		<-release
		active.Add(-1)
		return testRuntimeClients("uid"), nil
	}
	manager, err := New(testConfig(connections...), filepath.Join(t.TempDir(), "state.json"),
		WithRuntimeFactory(factory), WithCacheSyncTimeout(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	if err := manager.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			close(release)
			t.Fatal("startup did not fill the bounded worker pool")
		}
	}
	if got := maximum.Load(); got > 4 {
		close(release)
		t.Fatalf("maximum concurrent starts = %d, want <= 4", got)
	}
	close(release)
}

func TestManagerCancellationDoesNotPublishLateRuntime(t *testing.T) {
	config := testConfig(ConnectionSpec{ID: "slow", Name: "Slow", Source: SourceSpec{Type: SourceInCluster}})
	started := make(chan struct{})
	factory := func(ctx context.Context, _ ConnectionSpec) (RuntimeClients, error) {
		close(started)
		<-ctx.Done()
		return RuntimeClients{}, ctx.Err()
	}
	manager, err := New(config, filepath.Join(t.TempDir(), "state.json"), WithRuntimeFactory(factory))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	if err := manager.Start(ctx); err != nil {
		t.Fatal(err)
	}
	<-started
	cancel()
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Runtime(manager.clusterIDs["slow"]); err == nil {
		t.Fatal("runtime published after cancellation and close")
	}
}

func TestManagerConcurrentLookupAndShutdown(t *testing.T) {
	config := testConfig(ConnectionSpec{ID: "slow", Name: "Slow", Source: SourceSpec{Type: SourceInCluster}})
	started := make(chan struct{})
	factory := func(ctx context.Context, _ ConnectionSpec) (RuntimeClients, error) {
		close(started)
		<-ctx.Done()
		return RuntimeClients{}, ctx.Err()
	}
	manager, err := New(config, filepath.Join(t.TempDir(), "state.json"), WithRuntimeFactory(factory))
	if err != nil {
		t.Fatal(err)
	}
	clusterID := manager.clusterIDs["slow"]
	if err := manager.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	<-started
	var lookups sync.WaitGroup
	for i := 0; i < 4; i++ {
		lookups.Add(1)
		go func() {
			defer lookups.Done()
			for j := 0; j < 100; j++ {
				_ = manager.Catalog()
				_ = manager.Statuses()
				_, _ = manager.Status(clusterID)
				_, _ = manager.Runtime(clusterID)
			}
		}()
	}
	var closes sync.WaitGroup
	for i := 0; i < 2; i++ {
		closes.Add(1)
		go func() {
			defer closes.Done()
			_ = manager.Close()
		}()
	}
	lookups.Wait()
	closes.Wait()
	if _, err := manager.Runtime(clusterID); err == nil {
		t.Fatal("runtime became available after concurrent shutdown")
	}
}

func testRuntimeClients(uid string) RuntimeClients {
	typed := kubernetesfake.NewSimpleClientset(&corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: "kube-system", UID: types.UID(uid)},
	})
	return runtimeClientsForTyped(typed)
}

func testRuntimeClientsWithPod(uid, cluster string) RuntimeClients {
	typed := kubernetesfake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "kube-system", UID: types.UID(uid)}},
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name: "shared-name", Namespace: "default",
				Labels: map[string]string{"cluster": cluster},
			},
		},
	)
	return runtimeClientsForTyped(typed)
}

func runtimeClientsForTyped(typed *kubernetesfake.Clientset) RuntimeClients {
	return RuntimeClients{
		Typed:     typed,
		Dynamic:   dynamicfake.NewSimpleDynamicClient(k8sruntime.NewScheme()),
		Discovery: &discoveryfake.FakeDiscovery{Fake: &k8stesting.Fake{}},
	}
}

func waitForStatus(t *testing.T, manager *Manager, clusterID string) ConnectionStatus {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		status, err := manager.Status(clusterID)
		if err != nil {
			t.Fatal(err)
		}
		if status.State != StateConnecting {
			return status
		}
		time.Sleep(10 * time.Millisecond)
	}
	status, _ := manager.Status(clusterID)
	t.Fatalf("status remained connecting: %#v", status)
	return ConnectionStatus{}
}

func waitForState(t *testing.T, manager *Manager, clusterID string, want ConnectionState) ConnectionStatus {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		status, err := manager.Status(clusterID)
		if err != nil {
			t.Fatal(err)
		}
		if status.State == want {
			return status
		}
		time.Sleep(10 * time.Millisecond)
	}
	status, _ := manager.Status(clusterID)
	t.Fatalf("status = %#v, want %s", status, want)
	return ConnectionStatus{}
}

func waitForStartup(t *testing.T, manager *Manager) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		manager.mu.RLock()
		starting := manager.starting
		manager.mu.RUnlock()
		if !starting {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("manager startup did not finish")
}
