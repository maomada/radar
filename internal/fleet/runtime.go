package fleet

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/skyhook-io/radar/pkg/k8score"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

const (
	defaultProbeTimeout = 5 * time.Second
	defaultSyncTimeout  = 10 * time.Second
)

type RuntimeClients struct {
	Typed     kubernetes.Interface         `json:"-"`
	Dynamic   dynamic.Interface            `json:"-"`
	Discovery discovery.DiscoveryInterface `json:"-"`
	Config    *rest.Config                 `json:"-"`
}

type RuntimeFactory func(context.Context, ConnectionSpec) (RuntimeClients, error)
type IdentityProbe func(context.Context, kubernetes.Interface) (string, error)

type ClusterRuntime struct {
	clusterID    string
	connectionID string
	namespaces   []string
	typed        kubernetes.Interface
	dynamic      dynamic.Interface
	discovery    discovery.DiscoveryInterface
	config       *rest.Config
	cache        *k8score.ResourceCache
	cancel       context.CancelFunc
	statusMu     sync.RWMutex
	status       ConnectionStatus
	closeOnce    sync.Once
}

func (r *ClusterRuntime) Kubernetes() kubernetes.Interface {
	if r == nil {
		return nil
	}
	return r.typed
}

func (r *ClusterRuntime) Dynamic() dynamic.Interface {
	if r == nil {
		return nil
	}
	return r.dynamic
}

func (r *ClusterRuntime) Discovery() discovery.DiscoveryInterface {
	if r == nil {
		return nil
	}
	return r.discovery
}

func (r *ClusterRuntime) Namespaces() []string {
	if r == nil {
		return nil
	}
	return append([]string(nil), r.namespaces...)
}

func (r *ClusterRuntime) RESTConfig() *rest.Config {
	if r == nil || r.config == nil {
		return nil
	}
	return rest.CopyConfig(r.config)
}

func (r *ClusterRuntime) Cache() *k8score.ResourceCache {
	if r == nil {
		return nil
	}
	return r.cache
}

func (r *ClusterRuntime) Status() ConnectionStatus {
	if r == nil {
		return ConnectionStatus{State: StateUnavailable}
	}
	r.statusMu.RLock()
	defer r.statusMu.RUnlock()
	return cloneConnectionStatus(r.status)
}

func (r *ClusterRuntime) KindReadinessFor(kind string) k8score.KindReadiness {
	if r == nil || r.cache == nil {
		return k8score.KindUnavailable
	}
	return r.cache.KindReadinessFor(kind)
}

func (r *ClusterRuntime) Close() {
	if r == nil {
		return
	}
	r.closeOnce.Do(func() {
		if r.cancel != nil {
			r.cancel()
		}
		if r.cache != nil {
			r.cache.Stop()
		}
	})
}

type runtimeOptions struct {
	factory       RuntimeFactory
	identityProbe IdentityProbe
	probeTimeout  time.Duration
	syncTimeout   time.Duration
}

func defaultRuntimeOptions() runtimeOptions {
	return runtimeOptions{
		factory:       defaultRuntimeFactory,
		identityProbe: defaultIdentityProbe,
		probeTimeout:  defaultProbeTimeout,
		syncTimeout:   defaultSyncTimeout,
	}
}

type startupResult struct {
	runtime *ClusterRuntime
	status  ConnectionStatus
}

type collectionKind struct {
	key        string
	namespaced bool
	list       func(context.Context, kubernetes.Interface, string) error
}

type probeOutcome struct {
	status          ProbeStatus
	namespaces      []string
	namespaceProbes []NamespaceProbe
}

func startClusterRuntime(
	ctx context.Context,
	spec ConnectionSpec,
	clusterID string,
	registry *Registry,
	options runtimeOptions,
) startupResult {
	now := time.Now().UTC()
	status := ConnectionStatus{
		ConnectionID:  spec.ID,
		ClusterID:     clusterID,
		Name:          spec.Name,
		State:         StateConnecting,
		Identity:      IdentityUnresolved,
		LastAttemptAt: now,
		Kinds:         make([]KindCoverage, 0, 5),
	}
	if ctx.Err() != nil {
		status.State = StateUnavailable
		return startupResult{status: status}
	}
	clients, err := options.factory(ctx, spec)
	if err != nil || clients.Typed == nil || clients.Dynamic == nil || clients.Discovery == nil {
		status.State = StateUnavailable
		return startupResult{status: status}
	}
	if ctx.Err() != nil {
		status.State = StateUnavailable
		return startupResult{status: status}
	}
	if clients.Config != nil {
		clients.Config = rest.CopyConfig(clients.Config)
	}
	stored, hasStored := registry.identity(clusterID)
	if !hasStored {
		status.State = StateUnavailable
		return startupResult{status: status}
	}
	if stored.Identity == IdentityConflict {
		status.State = StateIdentityConflict
		status.Identity = IdentityConflict
		status.IdentityObservedAt = stored.IdentityObservedAt
		return startupResult{status: status}
	}
	probeCtx, cancelProbe := context.WithTimeout(ctx, options.probeTimeout)
	uid, identityErr := options.identityProbe(probeCtx, clients.Typed)
	cancelProbe()
	if ctx.Err() != nil {
		status.State = StateUnavailable
		status.IdentityObservedAt = stored.IdentityObservedAt
		return startupResult{status: status}
	}
	switch {
	case identityErr == nil && uid != "":
		if err := registry.RecordIdentity(clusterID, uid, now); err != nil {
			if errors.Is(err, errIdentityConflict) {
				stored, _ = registry.identity(clusterID)
				status.State = StateIdentityConflict
				status.Identity = IdentityConflict
				status.IdentityObservedAt = stored.IdentityObservedAt
			} else {
				status.State = StateUnavailable
			}
			return startupResult{status: status}
		}
		stored, _ = registry.identity(clusterID)
		status.Identity = IdentityVerified
		status.IdentityObservedAt = now
	case identityErr != nil:
		status.Identity = IdentityUnresolved
		status.IdentityObservedAt = stored.IdentityObservedAt
	default:
		status.Identity = IdentityUnresolved
		status.IdentityObservedAt = stored.IdentityObservedAt
	}
	if stored.Identity == IdentityConflict {
		status.State = StateIdentityConflict
		status.Identity = IdentityConflict
		return startupResult{status: status}
	}

	probeCtx, cancelProbe = context.WithTimeout(ctx, options.probeTimeout)
	coverage, scopes, scopeNamespaces := probeCollectionAccess(probeCtx, clients.Typed, spec.Namespaces)
	cancelProbe()
	status.Kinds = coverage
	if ctx.Err() != nil {
		status.State = StateUnavailable
		return startupResult{status: status}
	}
	enabled := false
	for _, kind := range coverage {
		if kind.Probe == ProbeAllowed {
			enabled = true
			break
		}
	}
	if !enabled {
		status.State = StateUnavailable
		return startupResult{status: status}
	}
	cache, err := k8score.NewResourceCache(k8score.CacheConfig{
		Client:                  clients.Typed,
		ResourceScopes:          scopes,
		ResourceScopeNamespaces: scopeNamespaces,
		SyncTimeout:             options.syncTimeout,
		DeferredSyncTimeout:     options.syncTimeout,
	})
	if err != nil {
		if cache != nil {
			cache.Stop()
		}
		status.State = StateUnavailable
		return startupResult{status: status}
	}
	if ctx.Err() != nil {
		cache.Stop()
		status.State = StateUnavailable
		return startupResult{status: status}
	}
	for i := range status.Kinds {
		status.Kinds[i].Readiness = readinessString(cache.KindReadinessFor(status.Kinds[i].Kind))
	}
	status.State = statusState(status)
	if status.State == StateReady {
		status.LastReadyAt = now
	}
	runtimeCtx, cancelRuntime := context.WithCancel(context.Background())
	runtime := &ClusterRuntime{
		clusterID:    clusterID,
		connectionID: spec.ID,
		namespaces:   append([]string(nil), spec.Namespaces...),
		typed:        clients.Typed,
		dynamic:      clients.Dynamic,
		discovery:    clients.Discovery,
		config:       clients.Config,
		cache:        cache,
		cancel:       cancelRuntime,
		status:       cloneConnectionStatus(status),
	}
	go func() {
		<-runtimeCtx.Done()
		runtime.Close()
	}()
	if ctx.Err() != nil {
		runtime.Close()
		status.State = StateUnavailable
		return startupResult{status: status}
	}
	return startupResult{runtime: runtime, status: status}
}

func defaultRuntimeFactory(_ context.Context, spec ConnectionSpec) (RuntimeClients, error) {
	source, err := resolveSource(spec)
	if err != nil {
		return RuntimeClients{}, err
	}
	var config *rest.Config
	switch source.typ {
	case SourceKubeconfig:
		clientConfig := clientcmd.NewNonInteractiveClientConfig(
			*source.rawConfig,
			source.context,
			&clientcmd.ConfigOverrides{CurrentContext: source.context},
			nil,
		)
		config, err = clientConfig.ClientConfig()
	case SourceInCluster:
		config, err = rest.InClusterConfig()
	default:
		err = errors.New("unsupported connection source")
	}
	if err != nil || config == nil {
		return RuntimeClients{}, errors.New("connection client configuration is unavailable")
	}
	config = rest.CopyConfig(config)
	typed, err := kubernetes.NewForConfig(config)
	if err != nil {
		return RuntimeClients{}, errors.New("typed Kubernetes client is unavailable")
	}
	dynamicClient, err := dynamic.NewForConfig(config)
	if err != nil {
		return RuntimeClients{}, errors.New("dynamic Kubernetes client is unavailable")
	}
	discoveryClient, err := discovery.NewDiscoveryClientForConfig(config)
	if err != nil {
		return RuntimeClients{}, errors.New("discovery Kubernetes client is unavailable")
	}
	return RuntimeClients{
		Typed: typed, Dynamic: dynamicClient, Discovery: discoveryClient, Config: config,
	}, nil
}

func defaultIdentityProbe(ctx context.Context, client kubernetes.Interface) (string, error) {
	namespace, err := client.CoreV1().Namespaces().Get(ctx, "kube-system", metav1.GetOptions{})
	if err != nil {
		return "", err
	}
	return string(namespace.UID), nil
}

func probeCollectionAccess(ctx context.Context, client kubernetes.Interface, namespaces []string) ([]KindCoverage, map[string]k8score.ResourceScope, map[string][]string) {
	kinds := []collectionKind{
		{key: k8score.Namespaces, list: func(ctx context.Context, client kubernetes.Interface, _ string) error {
			_, err := client.CoreV1().Namespaces().List(ctx, metav1.ListOptions{Limit: 1})
			return err
		}},
		{key: k8score.Nodes, list: func(ctx context.Context, client kubernetes.Interface, _ string) error {
			_, err := client.CoreV1().Nodes().List(ctx, metav1.ListOptions{Limit: 1})
			return err
		}},
		{key: k8score.Pods, namespaced: true, list: func(ctx context.Context, client kubernetes.Interface, namespace string) error {
			_, err := client.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{Limit: 1})
			return err
		}},
		{key: k8score.Deployments, namespaced: true, list: func(ctx context.Context, client kubernetes.Interface, namespace string) error {
			_, err := client.AppsV1().Deployments(namespace).List(ctx, metav1.ListOptions{Limit: 1})
			return err
		}},
		{key: k8score.Services, namespaced: true, list: func(ctx context.Context, client kubernetes.Interface, namespace string) error {
			_, err := client.CoreV1().Services(namespace).List(ctx, metav1.ListOptions{Limit: 1})
			return err
		}},
	}
	scopes := make(map[string]k8score.ResourceScope, len(kinds))
	scopeNamespaces := make(map[string][]string)
	coverage := make([]KindCoverage, 0, len(kinds))
	for _, kind := range kinds {
		outcome := probeOutcome{status: ProbeNotProbed}
		if kind.namespaced && len(namespaces) > 0 {
			outcome = probeNamespaced(ctx, client, kind, namespaces)
		} else {
			outcome.status = classifyProbeError(kind.list(ctx, client, ""))
			if outcome.status == ProbeAllowed {
				scopes[kind.key] = k8score.ResourceScope{Enabled: true}
			}
		}
		if outcome.status == ProbeAllowed && kind.namespaced && len(outcome.namespaces) > 0 {
			scopes[kind.key] = k8score.ResourceScope{
				Enabled:   true,
				Namespace: outcome.namespaces[0],
			}
			scopeNamespaces[kind.key] = append([]string(nil), outcome.namespaces...)
		}
		coverage = append(coverage, KindCoverage{
			Kind:            kind.key,
			Probe:           outcome.status,
			Scope:           scopeFor(outcome, kind.namespaced),
			Namespaces:      append([]string(nil), outcome.namespaces...),
			NamespaceProbes: append([]NamespaceProbe(nil), outcome.namespaceProbes...),
			Readiness:       "unavailable",
		})
	}
	return coverage, scopes, scopeNamespaces
}

func probeNamespaced(ctx context.Context, client kubernetes.Interface, kind collectionKind, namespaces []string) probeOutcome {
	outcome := probeOutcome{status: ProbeDenied}
	sawUnavailable := false
	sawUnauthenticated := false
	for _, namespace := range namespaces {
		status := classifyProbeError(kind.list(ctx, client, namespace))
		outcome.namespaceProbes = append(outcome.namespaceProbes, NamespaceProbe{
			Namespace: namespace,
			Outcome:   status,
		})
		if status == ProbeAllowed {
			outcome.namespaces = append(outcome.namespaces, namespace)
			continue
		}
		if status == ProbeUnauthenticated {
			sawUnauthenticated = true
		} else if status == ProbeUnavailable {
			sawUnavailable = true
		}
	}
	switch {
	case len(outcome.namespaces) > 0:
		outcome.status = ProbeAllowed
	case sawUnauthenticated:
		outcome.status = ProbeUnauthenticated
	case sawUnavailable:
		outcome.status = ProbeUnavailable
	}
	return outcome
}

func classifyProbeError(err error) ProbeStatus {
	switch {
	case err == nil:
		return ProbeAllowed
	case apierrors.IsUnauthorized(err):
		return ProbeUnauthenticated
	case apierrors.IsForbidden(err):
		return ProbeDenied
	default:
		return ProbeUnavailable
	}
}

func scopeFor(outcome probeOutcome, namespaced bool) string {
	if outcome.status != ProbeAllowed {
		return ""
	}
	if namespaced {
		if len(outcome.namespaces) > 0 {
			return "namespaces"
		}
	}
	return "cluster"
}

func statusState(status ConnectionStatus) ConnectionState {
	allowed := 0
	ready := 0
	for _, kind := range status.Kinds {
		if kind.Probe != ProbeAllowed {
			continue
		}
		allowed++
		if kind.Readiness == readinessString(k8score.KindReady) && namespaceCoverageComplete(kind.NamespaceProbes) {
			ready++
		}
	}
	if allowed == 0 {
		return StateUnavailable
	}
	if status.Identity == IdentityConflict {
		return StateIdentityConflict
	}
	if status.Identity != IdentityVerified || ready != allowed || len(status.Kinds) != allowed {
		return StatePartial
	}
	return StateReady
}

func namespaceCoverageComplete(probes []NamespaceProbe) bool {
	for _, probe := range probes {
		if probe.Outcome != ProbeAllowed {
			return false
		}
	}
	return true
}

func readinessString(readiness k8score.KindReadiness) string {
	switch readiness {
	case k8score.KindReady:
		return "ready"
	case k8score.KindPending:
		return "pending"
	case k8score.KindFailed:
		return "failed"
	default:
		return "unavailable"
	}
}

func cloneConnectionStatus(status ConnectionStatus) ConnectionStatus {
	clone := status
	clone.DuplicateCandidates = append([]string(nil), status.DuplicateCandidates...)
	clone.Kinds = make([]KindCoverage, len(status.Kinds))
	for i, kind := range status.Kinds {
		clone.Kinds[i] = kind
		clone.Kinds[i].Namespaces = append([]string(nil), kind.Namespaces...)
		clone.Kinds[i].NamespaceProbes = append([]NamespaceProbe(nil), kind.NamespaceProbes...)
	}
	return clone
}

func sortConnectionStatuses(statuses []ConnectionStatus) {
	sort.Slice(statuses, func(i, j int) bool {
		if statuses[i].ClusterID != statuses[j].ClusterID {
			return statuses[i].ClusterID < statuses[j].ClusterID
		}
		return statuses[i].ConnectionID < statuses[j].ConnectionID
	})
}
