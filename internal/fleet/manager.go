package fleet

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"
)

type ManagerOption func(*runtimeOptions)

func WithRuntimeFactory(factory RuntimeFactory) ManagerOption {
	return func(options *runtimeOptions) {
		if factory != nil {
			options.factory = factory
		}
	}
}

func WithIdentityProbe(probe IdentityProbe) ManagerOption {
	return func(options *runtimeOptions) {
		if probe != nil {
			options.identityProbe = probe
		}
	}
}

func WithCandidateProvider(provider CandidateProvider) ManagerOption {
	return func(options *runtimeOptions) {
		options.candidates = provider
	}
}

func WithManagedNamespaces(namespaces []string) ManagerOption {
	return func(options *runtimeOptions) {
		options.managedNamespaces = append([]string(nil), namespaces...)
	}
}

func WithManagedNamespaceProvider(provider func() []string) ManagerOption {
	return func(options *runtimeOptions) {
		options.managedNamespaceProvider = provider
	}
}

func WithProbeTimeout(timeout time.Duration) ManagerOption {
	return func(options *runtimeOptions) {
		if timeout > 0 {
			options.probeTimeout = timeout
		}
	}
}

func WithCacheSyncTimeout(timeout time.Duration) ManagerOption {
	return func(options *runtimeOptions) {
		if timeout > 0 {
			options.syncTimeout = timeout
		}
	}
}

type Manager struct {
	mu                sync.RWMutex
	registry          *Registry
	config            Config
	clusterIDs        map[string]string
	specs             map[string]ConnectionSpec
	statuses          map[string]ConnectionStatus
	runtimes          map[string]*ClusterRuntime
	auditStore        *AuditStore
	options           runtimeOptions
	startSem          chan struct{}
	startCancel       context.CancelFunc
	startWG           sync.WaitGroup
	generation        uint64
	starting          bool
	closed            bool
	clusterGeneration map[string]uint64
	runtimeCancels    map[string]context.CancelFunc
	closeDone         chan struct{}
	closeErr          error
}

func Open(configPath, statePath string, options ...ManagerOption) (*Manager, error) {
	config, err := LoadConfig(configPath)
	if err != nil {
		return nil, err
	}
	return New(config, statePath, options...)
}

func New(config Config, statePath string, options ...ManagerOption) (*Manager, error) {
	if err := ValidateConfig(config); err != nil {
		return nil, err
	}
	runtimeOptions := defaultRuntimeOptions()
	for _, option := range options {
		if option != nil {
			option(&runtimeOptions)
		}
	}
	registry, err := OpenRegistry(statePath)
	if err != nil {
		return nil, err
	}
	clusterIDs, err := registry.SyncConfig(config)
	if err != nil {
		_ = registry.Close()
		return nil, err
	}
	managed := registry.ManagedConnections()
	manager := &Manager{
		registry:          registry,
		auditStore:        newAuditStore(registry.path + ".audit.jsonl"),
		config:            cloneConfig(config),
		clusterIDs:        clusterIDs,
		specs:             make(map[string]ConnectionSpec, len(config.Connections)),
		statuses:          make(map[string]ConnectionStatus, len(config.Connections)),
		runtimes:          make(map[string]*ClusterRuntime),
		options:           runtimeOptions,
		startSem:          make(chan struct{}, 4),
		closeDone:         make(chan struct{}),
		clusterGeneration: make(map[string]uint64),
		runtimeCancels:    make(map[string]context.CancelFunc),
	}
	for _, spec := range config.Connections {
		manager.specs[spec.ID] = cloneConnectionSpec(spec)
		clusterID := clusterIDs[spec.ID]
		manager.statuses[clusterID] = ConnectionStatus{
			ConnectionID: spec.ID,
			ClusterID:    clusterID,
			Name:         spec.Name,
			State:        StateConnecting,
			Identity:     IdentityUnresolved,
			Kinds:        make([]KindCoverage, 0, 5),
		}
	}
	for _, spec := range managed {
		spec.Managed = true
		clusterID := clusterIDs[spec.ID]
		if clusterID == "" {
			for _, connection := range registry.Snapshot().Connections {
				if connection.ID == spec.ID {
					clusterID = connection.ClusterID
					break
				}
			}
		}
		if clusterID == "" {
			continue
		}
		manager.clusterIDs[spec.ID] = clusterID
		manager.specs[spec.ID] = cloneConnectionSpec(spec)
		manager.statuses[clusterID] = ConnectionStatus{
			ConnectionID: spec.ID, ClusterID: clusterID, Name: spec.Name,
			State: StateConnecting, Identity: IdentityUnresolved, Kinds: make([]KindCoverage, 0, 5),
		}
	}
	manager.refreshDuplicateCandidatesLocked()
	return manager, nil
}

func (m *Manager) AuditStore() (*AuditStore, error) {
	if m == nil {
		return nil, errors.New("fleet audit is unavailable")
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.closed || m.auditStore == nil {
		return nil, errors.New("fleet audit is unavailable")
	}
	return m.auditStore, nil
}

func (m *Manager) Start(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	m.mu.RLock()
	registry := m.registry
	provider := m.options.candidates
	closed, starting := m.closed, m.starting
	m.mu.RUnlock()
	if closed {
		return errors.New("fleet manager is closed")
	}
	if starting {
		return errors.New("fleet manager is already starting")
	}
	managed := []ConnectionSpec{}
	if registry != nil && registry.hasManagedReferences() {
		candidates := []ConnectionCandidate{}
		discoveryAvailable := false
		if provider != nil {
			resolved, err := provider()
			if err == nil {
				candidates = resolved
				discoveryAvailable = true
			}
		}
		if !discoveryAvailable {
			_ = registry.ReconcileManaged(func() ([]ConnectionCandidate, error) {
				return []ConnectionCandidate{}, nil
			})
		} else {
			if err := registry.ReconcileManaged(func() ([]ConnectionCandidate, error) {
				return candidates, nil
			}); err != nil {
				return err
			}
			managed = registry.ManagedConnections()
		}
	} else if registry != nil {
		managed = registry.ManagedConnections()
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return errors.New("fleet manager is closed")
	}
	if m.starting {
		m.mu.Unlock()
		return errors.New("fleet manager is already starting")
	}
	if registry == nil {
		m.mu.Unlock()
		return errors.New("fleet manager is closed")
	}
	activeManaged := make(map[string]struct{}, len(managed))
	catalog := registry.Snapshot()
	clusterByConnection := make(map[string]string, len(catalog.Connections))
	for _, connection := range catalog.Connections {
		clusterByConnection[connection.ID] = connection.ClusterID
	}
	oldRuntimes := make([]*ClusterRuntime, 0)
	for _, spec := range managed {
		spec.Managed = true
		activeManaged[spec.ID] = struct{}{}
		clusterID := clusterByConnection[spec.ID]
		if clusterID == "" {
			continue
		}
		m.clusterIDs[spec.ID] = clusterID
		m.specs[spec.ID] = cloneConnectionSpec(spec)
		if _, exists := m.statuses[clusterID]; !exists {
			m.statuses[clusterID] = ConnectionStatus{
				ConnectionID: spec.ID, ClusterID: clusterID, Name: spec.Name,
				State: StateConnecting, Identity: IdentityUnresolved, Kinds: make([]KindCoverage, 0, 5),
			}
		}
	}
	for connectionID, spec := range m.specs {
		if !spec.Managed {
			continue
		}
		if _, active := activeManaged[connectionID]; active {
			continue
		}
		clusterID := m.clusterIDs[connectionID]
		if cancel := m.runtimeCancels[clusterID]; cancel != nil {
			cancel()
			delete(m.runtimeCancels, clusterID)
		}
		if runtime := m.runtimes[clusterID]; runtime != nil {
			oldRuntimes = append(oldRuntimes, runtime)
			delete(m.runtimes, clusterID)
		}
		delete(m.specs, connectionID)
		delete(m.clusterIDs, connectionID)
		delete(m.statuses, clusterID)
	}
	if m.startCancel != nil {
		m.startCancel()
	}
	for clusterID, cancel := range m.runtimeCancels {
		cancel()
		delete(m.runtimeCancels, clusterID)
	}
	m.starting = true
	m.generation++
	generation := m.generation
	startCtx, cancel := context.WithCancel(ctx)
	m.startCancel = cancel
	for clusterID := range m.statuses {
		m.clusterGeneration[clusterID] = generation
	}
	entries := make([]startEntry, 0, len(m.specs))
	for connectionID, spec := range m.specs {
		entries = append(entries, startEntry{
			connectionID: connectionID,
			clusterID:    m.clusterIDs[connectionID],
			spec:         cloneConnectionSpec(spec),
		})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].clusterID < entries[j].clusterID })
	for _, entry := range entries {
		if runtime := m.runtimes[entry.clusterID]; runtime != nil {
			oldRuntimes = append(oldRuntimes, runtime)
			delete(m.runtimes, entry.clusterID)
		}
		status := m.statuses[entry.clusterID]
		status.State = StateConnecting
		status.Identity = IdentityUnresolved
		status.LastAttemptAt = time.Now().UTC()
		status.DuplicateCandidates = nil
		m.statuses[entry.clusterID] = status
	}
	m.startWG.Add(len(entries))
	m.mu.Unlock()
	for _, runtime := range oldRuntimes {
		runtime.Close()
	}
	for _, entry := range entries {
		entry := entry
		go m.startEntry(startCtx, generation, entry)
	}
	go m.finishGeneration(generation)
	return nil
}

// Refresh removes the selected runtime from routing until its replacement is verified.
func (m *Manager) Refresh(ctx context.Context, clusterID string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return errors.New("fleet manager is closed")
	}
	if m.starting {
		m.mu.Unlock()
		return errors.New("fleet manager startup is in progress")
	}
	status, exists := m.statuses[clusterID]
	if !exists {
		m.mu.Unlock()
		return errors.New("unknown or inactive fleet cluster")
	}
	var entry startEntry
	for connectionID, spec := range m.specs {
		if m.clusterIDs[connectionID] == clusterID {
			entry = startEntry{connectionID: connectionID, clusterID: clusterID, spec: cloneConnectionSpec(spec)}
			break
		}
	}
	if entry.connectionID == "" {
		m.mu.Unlock()
		return errors.New("unknown or inactive fleet cluster")
	}
	m.generation++
	generation := m.generation
	startCtx, cancel := context.WithCancel(ctx)
	m.clusterGeneration[clusterID] = generation
	m.runtimeCancels[clusterID] = cancel
	m.starting = true
	status.State = StateConnecting
	status.Identity = IdentityUnresolved
	status.LastAttemptAt = time.Now().UTC()
	status.DuplicateCandidates = nil
	m.statuses[clusterID] = status
	oldRuntime := m.runtimes[clusterID]
	delete(m.runtimes, clusterID)
	m.startWG.Add(1)
	m.mu.Unlock()
	if oldRuntime != nil {
		oldRuntime.Close()
	}
	go m.startEntry(startCtx, generation, entry)
	go m.finishGeneration(generation)
	return nil
}

func (m *Manager) Close() error {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	if m.closed {
		done := m.closeDone
		m.mu.Unlock()
		if done != nil {
			<-done
		}
		m.mu.RLock()
		err := m.closeErr
		m.mu.RUnlock()
		return err
	}
	m.closed = true
	m.generation++
	cancel := m.startCancel
	m.startCancel = nil
	runtimeCancels := make([]context.CancelFunc, 0, len(m.runtimeCancels))
	for _, runtimeCancel := range m.runtimeCancels {
		runtimeCancels = append(runtimeCancels, runtimeCancel)
	}
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	for _, runtimeCancel := range runtimeCancels {
		runtimeCancel()
	}
	m.startWG.Wait()
	m.mu.Lock()
	runtimes := make([]*ClusterRuntime, 0, len(m.runtimes))
	for clusterID, runtime := range m.runtimes {
		runtimes = append(runtimes, runtime)
		delete(m.runtimes, clusterID)
	}
	for clusterID, status := range m.statuses {
		status.State = StateUnavailable
		m.statuses[clusterID] = status
	}
	registry := m.registry
	m.registry = nil
	m.mu.Unlock()
	for _, runtime := range runtimes {
		runtime.Close()
	}
	var closeErr error
	if registry != nil {
		closeErr = registry.Close()
	}
	m.mu.Lock()
	m.closeErr = closeErr
	if m.closeDone != nil {
		close(m.closeDone)
		m.closeDone = nil
	}
	m.mu.Unlock()
	return closeErr
}

func (m *Manager) Catalog() Catalog {
	if m == nil {
		return Catalog{}
	}
	m.mu.RLock()
	registry := m.registry
	m.mu.RUnlock()
	if registry == nil {
		return Catalog{}
	}
	return registry.Snapshot()
}

func (m *Manager) Statuses() []ConnectionStatus {
	if m == nil {
		return nil
	}
	m.mu.RLock()
	statuses := make([]ConnectionStatus, 0, len(m.statuses))
	for _, status := range m.statuses {
		statuses = append(statuses, cloneConnectionStatus(status))
	}
	m.mu.RUnlock()
	sortConnectionStatuses(statuses)
	return statuses
}

func (m *Manager) Status(clusterID string) (ConnectionStatus, error) {
	if m == nil {
		return ConnectionStatus{}, errors.New("fleet manager is unavailable")
	}
	m.mu.RLock()
	status, ok := m.statuses[clusterID]
	m.mu.RUnlock()
	if !ok {
		return ConnectionStatus{}, errors.New("unknown or inactive fleet cluster")
	}
	return cloneConnectionStatus(status), nil
}

func (m *Manager) Runtime(clusterID string) (*ClusterRuntime, error) {
	if m == nil {
		return nil, errors.New("fleet manager is unavailable")
	}
	m.mu.RLock()
	if _, ok := m.statuses[clusterID]; !ok {
		m.mu.RUnlock()
		return nil, errors.New("unknown or inactive fleet cluster")
	}
	runtime := m.runtimes[clusterID]
	status := m.statuses[clusterID]
	m.mu.RUnlock()
	if runtime == nil || status.State == StateIdentityConflict || status.State == StateUnavailable || status.State == StateConnecting {
		return nil, errors.New("fleet cluster is not routable")
	}
	return runtime, nil
}

// WithVerifiedRuntime pins a verified runtime until callback returns. The callback must not call Manager methods.
func (m *Manager) WithVerifiedRuntime(
	ctx context.Context,
	clusterID string,
	expectedRuntime *ClusterRuntime,
	callback func(context.Context, *ClusterRuntime) error,
) error {
	if m == nil || callback == nil {
		return errors.New("fleet runtime is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.closed || m.registry == nil {
		return errors.New("fleet runtime is unavailable")
	}
	status, ok := m.statuses[clusterID]
	runtime := m.runtimes[clusterID]
	if !ok || runtime == nil || status.State == StateConnecting || status.State == StateUnavailable ||
		status.State == StateIdentityConflict || status.Identity != IdentityVerified ||
		runtime.clusterID != clusterID || status.ClusterID != clusterID ||
		status.ConnectionID != runtime.connectionID || (expectedRuntime != nil && runtime != expectedRuntime) {
		return errors.New("fleet runtime is not verified and routable")
	}
	runtimeStatus := runtime.Status()
	if runtimeStatus.State == StateConnecting || runtimeStatus.State == StateUnavailable ||
		runtimeStatus.State == StateIdentityConflict || runtimeStatus.Identity != IdentityVerified ||
		runtimeStatus.ConnectionID != runtime.connectionID || runtimeStatus.ClusterID != clusterID {
		return errors.New("fleet runtime is not verified and routable")
	}
	stored, exists := m.registry.identity(clusterID)
	if !exists || !stored.Active || stored.ConnectionID != runtime.connectionID ||
		stored.Identity != IdentityVerified || stored.IdentityUID == "" {
		return errors.New("fleet runtime identity is unavailable")
	}
	probeCtx, cancelProbe := context.WithTimeout(ctx, defaultProbeTimeout)
	uid, err := m.options.identityProbe(probeCtx, runtime.Kubernetes())
	cancelProbe()
	if err != nil || uid == "" || uid != stored.IdentityUID {
		return errors.New("fleet runtime identity could not be verified")
	}
	callbackCtx, cancelCallback := context.WithTimeout(ctx, 20*time.Second)
	defer cancelCallback()
	return callback(callbackCtx, runtime)
}

func (m *Manager) UpsertFleet(id, name string, clusterIDs []string) error {
	m.mu.RLock()
	registry := m.registry
	m.mu.RUnlock()
	if registry == nil {
		return errors.New("fleet manager is closed")
	}
	return registry.UpsertFleet(id, name, clusterIDs)
}

func (m *Manager) SetMembership(fleetID string, clusterIDs []string) error {
	m.mu.RLock()
	registry := m.registry
	m.mu.RUnlock()
	if registry == nil {
		return errors.New("fleet manager is closed")
	}
	return registry.SetMembership(fleetID, clusterIDs)
}

func (m *Manager) DeleteFleet(fleetID string) error {
	m.mu.RLock()
	registry := m.registry
	m.mu.RUnlock()
	if registry == nil {
		return errors.New("fleet manager is closed")
	}
	return registry.DeleteFleet(fleetID)
}

func (m *Manager) ManagedConnectionIDs() []string {
	if m == nil {
		return []string{}
	}
	catalog := m.Catalog()
	ids := make([]string, 0)
	for _, connection := range catalog.Connections {
		if connection.Managed && connection.Active {
			ids = append(ids, connection.ID)
		}
	}
	sort.Strings(ids)
	return ids
}

func (m *Manager) AddManagedConnection(id, name, candidateID string) (string, error) {
	if m == nil {
		return "", errors.New("fleet manager is unavailable")
	}
	if m.options.candidates == nil {
		return "", errors.New("local Fleet connections are disabled")
	}
	candidates, err := m.options.candidates()
	if err != nil {
		return "", errors.New("fleet candidates are unavailable")
	}
	var match *ConnectionCandidate
	for i := range candidates {
		if candidates[i].ID == candidateID {
			match = &candidates[i]
			break
		}
	}
	if match == nil || match.SourceFile == "" || match.RawContext == "" {
		return "", errors.New("candidate is no longer trusted")
	}
	namespaces := m.options.managedNamespaces
	if m.options.managedNamespaceProvider != nil {
		namespaces = m.options.managedNamespaceProvider()
	}
	spec := ConnectionSpec{
		ID: id, Name: name, Managed: true,
		Source:     SourceSpec{Type: SourceKubeconfig, Kubeconfig: match.SourceFile, Context: match.RawContext},
		Namespaces: append([]string(nil), namespaces...),
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed || m.registry == nil {
		return "", errors.New("fleet manager is closed")
	}
	if m.starting {
		return "", errors.New("fleet runtime startup is in progress")
	}
	if _, exists := m.specs[id]; exists {
		return "", errors.New("connection ID is already in use")
	}
	clusterID, err := m.registry.AddManagedConnection(spec)
	if err != nil {
		for _, connection := range m.registry.Snapshot().Connections {
			if connection.ID != id || !connection.Managed || !connection.Active {
				continue
			}
			clusterID = connection.ClusterID
			m.clusterIDs[id] = clusterID
			m.specs[id] = cloneConnectionSpec(spec)
			m.statuses[clusterID] = ConnectionStatus{
				ConnectionID: id, ClusterID: clusterID, Name: name,
				State: StateUnavailable, Identity: IdentityUnresolved,
				LastAttemptAt: time.Now().UTC(), Kinds: make([]KindCoverage, 0, 5),
			}
			return clusterID, err
		}
		return "", err
	}
	m.clusterIDs[id] = clusterID
	m.specs[id] = cloneConnectionSpec(spec)
	m.statuses[clusterID] = ConnectionStatus{
		ConnectionID: id, ClusterID: clusterID, Name: name,
		State: StateConnecting, Identity: IdentityUnresolved, Kinds: make([]KindCoverage, 0, 5),
	}
	m.generation++
	generation := m.generation
	m.clusterGeneration[clusterID] = generation
	ctx, cancel := context.WithCancel(context.Background())
	m.runtimeCancels[clusterID] = cancel
	entry := startEntry{connectionID: id, clusterID: clusterID, spec: cloneConnectionSpec(spec)}
	m.startWG.Add(1)
	go m.startEntry(ctx, generation, entry)
	return clusterID, nil
}

func (m *Manager) RemoveManagedConnection(id string) error {
	if m == nil {
		return errors.New("fleet manager is unavailable")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed || m.registry == nil {
		return errors.New("fleet manager is closed")
	}
	clusterID, ok := m.clusterIDs[id]
	spec := m.specs[id]
	if !ok || !spec.Managed {
		return errors.New("unknown managed connection")
	}
	err := m.registry.RemoveManagedConnection(id)
	if err != nil {
		committed := false
		for _, connection := range m.registry.Snapshot().Connections {
			if connection.ID == id && connection.Managed && !connection.Active {
				committed = true
				break
			}
		}
		if !committed {
			return err
		}
	}
	if cancel := m.runtimeCancels[clusterID]; cancel != nil {
		cancel()
		delete(m.runtimeCancels, clusterID)
	}
	m.clusterGeneration[clusterID]++
	if runtime := m.runtimes[clusterID]; runtime != nil {
		runtime.Close()
		delete(m.runtimes, clusterID)
	}
	delete(m.specs, id)
	delete(m.clusterIDs, id)
	delete(m.statuses, clusterID)
	return err
}

func (m *Manager) SetDuplicateDecision(clusterID, otherClusterID, decision string) error {
	if m == nil {
		return errors.New("fleet manager is unavailable")
	}
	m.mu.RLock()
	registry := m.registry
	m.mu.RUnlock()
	if registry == nil {
		return errors.New("fleet manager is closed")
	}
	return registry.SetDuplicateDecision(clusterID, otherClusterID, decision)
}

type startEntry struct {
	connectionID string
	clusterID    string
	spec         ConnectionSpec
}

func (m *Manager) startEntry(ctx context.Context, generation uint64, entry startEntry) {
	defer m.startWG.Done()
	defer m.clearRuntimeCancel(entry.clusterID, generation)
	select {
	case m.startSem <- struct{}{}:
	case <-ctx.Done():
		m.publishStatus(generation, entry.clusterID, ConnectionStatus{
			ConnectionID:  entry.connectionID,
			ClusterID:     entry.clusterID,
			Name:          entry.spec.Name,
			State:         StateUnavailable,
			Identity:      IdentityUnresolved,
			LastAttemptAt: time.Now().UTC(),
		}, nil)
		return
	}
	defer func() { <-m.startSem }()
	result := startClusterRuntime(ctx, entry.spec, entry.clusterID, m.registry, m.options)
	if ctx.Err() != nil {
		if result.runtime != nil {
			result.runtime.Close()
		}
		result.runtime = nil
		result.status.State = StateUnavailable
	}
	m.publishStatus(generation, entry.clusterID, result.status, result.runtime)
}

func (m *Manager) clearRuntimeCancel(clusterID string, generation uint64) {
	m.mu.Lock()
	if m.clusterGeneration[clusterID] == generation {
		delete(m.runtimeCancels, clusterID)
	}
	m.mu.Unlock()
}

func (m *Manager) finishGeneration(generation uint64) {
	m.startWG.Wait()
	m.mu.Lock()
	if m.generation == generation {
		m.starting = false
	}
	m.mu.Unlock()
}

func (m *Manager) publishStatus(generation uint64, clusterID string, status ConnectionStatus, runtime *ClusterRuntime) {
	m.mu.Lock()
	if m.closed || generation != m.clusterGeneration[clusterID] {
		m.mu.Unlock()
		if runtime != nil {
			runtime.Close()
		}
		return
	}
	oldRuntime := m.runtimes[clusterID]
	delete(m.runtimes, clusterID)
	if previous, exists := m.statuses[clusterID]; exists {
		if status.IdentityObservedAt.IsZero() {
			status.IdentityObservedAt = previous.IdentityObservedAt
		}
		if status.LastReadyAt.IsZero() {
			status.LastReadyAt = previous.LastReadyAt
		}
	}
	m.statuses[clusterID] = cloneConnectionStatus(status)
	published := runtime != nil && status.State != StateIdentityConflict && status.State != StateUnavailable
	if published {
		m.runtimes[clusterID] = runtime
	}
	m.refreshDuplicateCandidatesLocked()
	m.mu.Unlock()
	if oldRuntime != nil && oldRuntime != runtime {
		oldRuntime.Close()
	}
	if runtime != nil && !published {
		runtime.Close()
	}
}

func (m *Manager) refreshDuplicateCandidatesLocked() {
	if m.registry == nil {
		return
	}
	catalog := m.registry.Snapshot()
	byUID := make(map[string][]string)
	for _, cluster := range catalog.Clusters {
		if !cluster.Active || cluster.Identity != IdentityVerified || cluster.IdentityUID == "" {
			continue
		}
		byUID[cluster.IdentityUID] = append(byUID[cluster.IdentityUID], cluster.ID)
	}
	for _, ids := range byUID {
		sort.Strings(ids)
	}
	for clusterID, status := range m.statuses {
		status.DuplicateCandidates = nil
		for _, cluster := range catalog.Clusters {
			if cluster.ID != clusterID || cluster.IdentityUID == "" {
				continue
			}
			for _, candidateID := range byUID[cluster.IdentityUID] {
				if candidateID != clusterID {
					status.DuplicateCandidates = append(status.DuplicateCandidates, candidateID)
				}
			}
		}
		sort.Strings(status.DuplicateCandidates)
		m.statuses[clusterID] = status
		if runtime := m.runtimes[clusterID]; runtime != nil {
			runtime.statusMu.Lock()
			runtime.status.DuplicateCandidates = append([]string(nil), status.DuplicateCandidates...)
			runtime.statusMu.Unlock()
		}
	}
}

func cloneConnectionSpec(spec ConnectionSpec) ConnectionSpec {
	clone := spec
	clone.Namespaces = append([]string(nil), spec.Namespaces...)
	return clone
}
