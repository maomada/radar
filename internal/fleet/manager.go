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
	mu          sync.RWMutex
	registry    *Registry
	config      Config
	clusterIDs  map[string]string
	specs       map[string]ConnectionSpec
	statuses    map[string]ConnectionStatus
	runtimes    map[string]*ClusterRuntime
	options     runtimeOptions
	startSem    chan struct{}
	startCancel context.CancelFunc
	startWG     sync.WaitGroup
	generation  uint64
	starting    bool
	closed      bool
	closeDone   chan struct{}
	closeErr    error
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
	manager := &Manager{
		registry:   registry,
		config:     cloneConfig(config),
		clusterIDs: clusterIDs,
		specs:      make(map[string]ConnectionSpec, len(config.Connections)),
		statuses:   make(map[string]ConnectionStatus, len(config.Connections)),
		runtimes:   make(map[string]*ClusterRuntime),
		options:    runtimeOptions,
		startSem:   make(chan struct{}, 4),
		closeDone:  make(chan struct{}),
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
	manager.refreshDuplicateCandidatesLocked()
	return manager, nil
}

func (m *Manager) Start(ctx context.Context) error {
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
		return errors.New("fleet manager is already starting")
	}
	if m.startCancel != nil {
		m.startCancel()
	}
	m.starting = true
	m.generation++
	generation := m.generation
	startCtx, cancel := context.WithCancel(ctx)
	m.startCancel = cancel
	entries := make([]startEntry, 0, len(m.specs))
	for connectionID, spec := range m.specs {
		entries = append(entries, startEntry{
			connectionID: connectionID,
			clusterID:    m.clusterIDs[connectionID],
			spec:         cloneConnectionSpec(spec),
		})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].clusterID < entries[j].clusterID })
	oldRuntimes := make([]*ClusterRuntime, 0, len(entries))
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
	if m.startCancel != nil {
		m.startCancel()
	}
	m.generation++
	generation := m.generation
	startCtx, cancel := context.WithCancel(ctx)
	m.startCancel = cancel
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
	m.mu.Unlock()
	if cancel != nil {
		cancel()
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

type startEntry struct {
	connectionID string
	clusterID    string
	spec         ConnectionSpec
}

func (m *Manager) startEntry(ctx context.Context, generation uint64, entry startEntry) {
	defer m.startWG.Done()
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
	if m.closed || generation != m.generation {
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
