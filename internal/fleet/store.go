package fleet

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gofrs/flock"
)

const (
	stateVersion = 1
	maxStateSize = 4 << 20
)

var errIdentityConflict = errors.New("fleet cluster identity conflicts with stored evidence")

type storedState struct {
	Version     int                `json:"version"`
	Connections []storedConnection `json:"connections"`
	Clusters    []storedCluster    `json:"clusters"`
	Fleets      []storedFleet      `json:"fleets"`
	Memberships []storedMembership `json:"memberships"`
}

type storedConnection struct {
	ID                string `json:"id"`
	Name              string `json:"name"`
	SourceFingerprint string `json:"sourceFingerprint"`
	ClusterID         string `json:"clusterId"`
	Active            bool   `json:"active"`
}

type storedCluster struct {
	ID                 string         `json:"id"`
	ConnectionID       string         `json:"connectionId"`
	Identity           IdentityStatus `json:"identity"`
	IdentityUID        string         `json:"identityUid,omitempty"`
	IdentityObservedAt time.Time      `json:"identityObservedAt,omitempty"`
	Active             bool           `json:"active"`
}

type storedFleet struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Active      bool   `json:"active"`
	Declarative bool   `json:"declarative,omitempty"`
}

type storedMembership struct {
	FleetID   string `json:"fleetId"`
	ClusterID string `json:"clusterId"`
}

type Registry struct {
	mu      sync.RWMutex
	path    string
	lock    *flock.Flock
	state   storedState
	syncDir func(string) error
	closed  bool
}

func OpenRegistry(path string) (*Registry, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, errors.New("fleet state path is invalid")
	}
	if err := ensureParent(filepath.Dir(absolute)); err != nil {
		return nil, errors.New("fleet state directory is unavailable")
	}
	lock := flock.New(absolute + ".lock")
	locked, err := lock.TryLock()
	if err != nil {
		return nil, errors.New("fleet state lock is unavailable")
	}
	if !locked {
		return nil, errors.New("fleet state is already in use")
	}
	if err := os.Chmod(absolute+".lock", 0o600); err != nil {
		_ = lock.Unlock()
		_ = lock.Close()
		return nil, errors.New("fleet state lock is unavailable")
	}
	registry := &Registry{
		path:    absolute,
		lock:    lock,
		state:   emptyState(),
		syncDir: syncParentDirectory,
	}
	if _, err := os.Stat(absolute); err == nil {
		data, readErr := readBoundedFile(absolute, maxStateSize)
		if readErr != nil {
			registry.Close()
			return nil, errors.New("fleet state cannot be read")
		}
		state, decodeErr := decodeState(data)
		if decodeErr != nil {
			registry.Close()
			return nil, errors.New("fleet state is invalid")
		}
		registry.state = state
	} else if !os.IsNotExist(err) {
		registry.Close()
		return nil, errors.New("fleet state cannot be read")
	}
	return registry, nil
}

func (r *Registry) Close() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil
	}
	r.closed = true
	lock := r.lock
	r.lock = nil
	r.mu.Unlock()
	if lock == nil {
		return nil
	}
	unlockErr := lock.Unlock()
	closeErr := lock.Close()
	if unlockErr != nil {
		return unlockErr
	}
	return closeErr
}

func (r *Registry) SyncConfig(config Config) (map[string]string, error) {
	if err := ValidateConfig(config); err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil, errors.New("fleet state is closed")
	}
	next := cloneStoredState(r.state)
	for i := range next.Connections {
		next.Connections[i].Active = false
	}
	for i := range next.Clusters {
		next.Clusters[i].Active = false
	}
	for i := range next.Fleets {
		if next.Fleets[i].Declarative {
			next.Fleets[i].Active = false
		}
	}
	clusterByConnection := make(map[string]string, len(config.Connections))
	clusterByID := make(map[string]storedCluster, len(next.Clusters))
	for _, cluster := range next.Clusters {
		clusterByID[cluster.ID] = cluster
	}
	connectionIndex := make(map[string]int, len(next.Connections))
	for i, connection := range next.Connections {
		connectionIndex[connection.ID] = i
	}
	for _, spec := range config.Connections {
		fingerprint := sourceFingerprint(spec.Source)
		index, exists := connectionIndex[spec.ID]
		if exists {
			record := &next.Connections[index]
			if record.SourceFingerprint != fingerprint {
				return nil, fmt.Errorf("connection %q is bound to a different source; choose a new connection ID", spec.ID)
			}
			if _, ok := clusterByID[record.ClusterID]; !ok {
				return nil, errors.New("fleet state is invalid")
			}
			record.Name = spec.Name
			record.Active = true
			clusterByConnection[spec.ID] = record.ClusterID
			continue
		}
		clusterID, err := newStableID()
		if err != nil {
			return nil, errors.New("unable to allocate a fleet cluster ID")
		}
		record := storedConnection{
			ID:                spec.ID,
			Name:              spec.Name,
			SourceFingerprint: fingerprint,
			ClusterID:         clusterID,
			Active:            true,
		}
		next.Connections = append(next.Connections, record)
		connectionIndex[spec.ID] = len(next.Connections) - 1
		cluster := storedCluster{
			ID:           clusterID,
			ConnectionID: spec.ID,
			Identity:     IdentityUnresolved,
			Active:       true,
		}
		next.Clusters = append(next.Clusters, cluster)
		clusterByID[clusterID] = cluster
		clusterByConnection[spec.ID] = clusterID
	}
	for i := range next.Clusters {
		if _, active := clusterByConnection[next.Clusters[i].ConnectionID]; active {
			next.Clusters[i].Active = true
		}
	}
	fleetIndex := make(map[string]int, len(next.Fleets))
	for i, fleet := range next.Fleets {
		fleetIndex[fleet.ID] = i
	}
	for _, spec := range config.Fleets {
		index, exists := fleetIndex[spec.ID]
		if !exists {
			next.Fleets = append(next.Fleets, storedFleet{
				ID: spec.ID, Name: spec.Name, Active: true, Declarative: true,
			})
			index = len(next.Fleets) - 1
			fleetIndex[spec.ID] = index
		} else {
			next.Fleets[index].Name = spec.Name
			next.Fleets[index].Active = true
			next.Fleets[index].Declarative = true
		}
		next.Memberships = removeFleetMemberships(next.Memberships, spec.ID)
		for _, connectionID := range spec.Connections {
			next.Memberships = append(next.Memberships, storedMembership{
				FleetID:   spec.ID,
				ClusterID: clusterByConnection[connectionID],
			})
		}
	}
	if err := validateStoredState(next); err != nil {
		return nil, errors.New("fleet state is invalid")
	}
	if err := r.persistStateLocked(next); err != nil {
		return nil, err
	}
	return clusterByConnection, nil
}

func (r *Registry) RecordIdentity(clusterID, uid string, observedAt time.Time) error {
	if uid == "" {
		return errors.New("identity evidence is empty")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return errors.New("fleet state is closed")
	}
	next := cloneStoredState(r.state)
	index := -1
	for i := range next.Clusters {
		if next.Clusters[i].ID == clusterID {
			index = i
			break
		}
	}
	if index < 0 {
		return errors.New("unknown fleet cluster")
	}
	cluster := &next.Clusters[index]
	if cluster.Identity == IdentityConflict {
		return errors.New("fleet cluster identity is already in conflict")
	}
	if cluster.IdentityUID != "" && cluster.IdentityUID != uid {
		cluster.Identity = IdentityConflict
		if err := r.persistStateLocked(next); err != nil {
			return err
		}
		return errIdentityConflict
	}
	cluster.IdentityUID = uid
	cluster.Identity = IdentityVerified
	cluster.IdentityObservedAt = observedAt.UTC()
	return r.persistStateLocked(next)
}

func (r *Registry) UpsertFleet(id, name string, clusterIDs []string) error {
	if err := validateIdentifier("fleet ID", id); err != nil {
		return err
	}
	if err := validateText("fleet name", name, MaxTextLength); err != nil {
		return err
	}
	if err := validateClusterIDs(clusterIDs); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return errors.New("fleet state is closed")
	}
	next := cloneStoredState(r.state)
	clusterSet := make(map[string]struct{}, len(next.Clusters))
	for _, cluster := range next.Clusters {
		clusterSet[cluster.ID] = struct{}{}
	}
	for _, clusterID := range clusterIDs {
		if _, ok := clusterSet[clusterID]; !ok {
			return errors.New("fleet membership references an unknown cluster")
		}
	}
	found := false
	for i := range next.Fleets {
		if next.Fleets[i].ID == id {
			next.Fleets[i].Name = name
			next.Fleets[i].Active = true
			next.Fleets[i].Declarative = false
			found = true
			break
		}
	}
	if !found {
		next.Fleets = append(next.Fleets, storedFleet{ID: id, Name: name, Active: true})
	}
	next.Memberships = removeFleetMemberships(next.Memberships, id)
	for _, clusterID := range clusterIDs {
		next.Memberships = append(next.Memberships, storedMembership{FleetID: id, ClusterID: clusterID})
	}
	return r.commitLocked(next)
}

func (r *Registry) SetMembership(fleetID string, clusterIDs []string) error {
	if err := validateIdentifier("fleet ID", fleetID); err != nil {
		return err
	}
	if err := validateClusterIDs(clusterIDs); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return errors.New("fleet state is closed")
	}
	next := cloneStoredState(r.state)
	fleetFound := false
	for _, fleet := range next.Fleets {
		if fleet.ID == fleetID {
			fleetFound = true
			break
		}
	}
	if !fleetFound {
		return errors.New("unknown fleet")
	}
	clusterSet := make(map[string]struct{}, len(next.Clusters))
	for _, cluster := range next.Clusters {
		clusterSet[cluster.ID] = struct{}{}
	}
	for _, clusterID := range clusterIDs {
		if _, ok := clusterSet[clusterID]; !ok {
			return errors.New("fleet membership references an unknown cluster")
		}
	}
	next.Memberships = removeFleetMemberships(next.Memberships, fleetID)
	for _, clusterID := range clusterIDs {
		next.Memberships = append(next.Memberships, storedMembership{FleetID: fleetID, ClusterID: clusterID})
	}
	return r.commitLocked(next)
}

func (r *Registry) DeleteFleet(fleetID string) error {
	if err := validateIdentifier("fleet ID", fleetID); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return errors.New("fleet state is closed")
	}
	next := cloneStoredState(r.state)
	found := false
	fleets := next.Fleets[:0]
	for _, fleet := range next.Fleets {
		if fleet.ID == fleetID {
			found = true
			continue
		}
		fleets = append(fleets, fleet)
	}
	if !found {
		return errors.New("unknown fleet")
	}
	next.Fleets = fleets
	next.Memberships = removeFleetMemberships(next.Memberships, fleetID)
	return r.commitLocked(next)
}

func (r *Registry) Snapshot() Catalog {
	r.mu.RLock()
	defer r.mu.RUnlock()
	catalog := Catalog{
		Connections: make([]ConnectionRecord, 0, len(r.state.Connections)),
		Clusters:    make([]ClusterRecord, 0, len(r.state.Clusters)),
		Fleets:      make([]FleetRecord, 0, len(r.state.Fleets)),
		Memberships: make([]MembershipRecord, 0, len(r.state.Memberships)),
	}
	for _, connection := range r.state.Connections {
		catalog.Connections = append(catalog.Connections, ConnectionRecord{
			ID: connection.ID, Name: connection.Name, ClusterID: connection.ClusterID, Active: connection.Active,
		})
	}
	for _, cluster := range r.state.Clusters {
		catalog.Clusters = append(catalog.Clusters, ClusterRecord{
			ID: cluster.ID, ConnectionID: cluster.ConnectionID, Active: cluster.Active,
			Identity: cluster.Identity, IdentityUID: cluster.IdentityUID,
			IdentityObservedAt: cluster.IdentityObservedAt,
		})
	}
	for _, fleet := range r.state.Fleets {
		catalog.Fleets = append(catalog.Fleets, FleetRecord{
			ID: fleet.ID, Name: fleet.Name, Active: fleet.Active, Declarative: fleet.Declarative,
		})
	}
	for _, membership := range r.state.Memberships {
		catalog.Memberships = append(catalog.Memberships, MembershipRecord{
			FleetID: membership.FleetID, ClusterID: membership.ClusterID,
		})
	}
	sort.Slice(catalog.Connections, func(i, j int) bool { return catalog.Connections[i].ID < catalog.Connections[j].ID })
	sort.Slice(catalog.Clusters, func(i, j int) bool { return catalog.Clusters[i].ID < catalog.Clusters[j].ID })
	sort.Slice(catalog.Fleets, func(i, j int) bool { return catalog.Fleets[i].ID < catalog.Fleets[j].ID })
	sort.Slice(catalog.Memberships, func(i, j int) bool {
		if catalog.Memberships[i].FleetID != catalog.Memberships[j].FleetID {
			return catalog.Memberships[i].FleetID < catalog.Memberships[j].FleetID
		}
		return catalog.Memberships[i].ClusterID < catalog.Memberships[j].ClusterID
	})
	return catalog
}

func (r *Registry) identity(clusterID string) (storedCluster, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, cluster := range r.state.Clusters {
		if cluster.ID == clusterID {
			return cluster, true
		}
	}
	return storedCluster{}, false
}

func (r *Registry) commitLocked(next storedState) error {
	if err := validateStoredState(next); err != nil {
		return errors.New("fleet state is invalid")
	}
	return r.persistStateLocked(next)
}

func (r *Registry) persistStateLocked(next storedState) error {
	committed, err := r.writeStateLocked(next)
	if committed {
		r.state = next
	}
	if err != nil {
		return errors.New("fleet state could not be persisted")
	}
	return nil
}

func (r *Registry) writeStateLocked(state storedState) (bool, error) {
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return false, err
	}
	dir := filepath.Dir(r.path)
	temp, err := os.CreateTemp(dir, ".fleet-state-*")
	if err != nil {
		return false, err
	}
	tempName := temp.Name()
	cleanup := func() {
		_ = temp.Close()
		_ = os.Remove(tempName)
	}
	if err := temp.Chmod(0o600); err != nil {
		cleanup()
		return false, err
	}
	if _, err := temp.Write(append(data, '\n')); err != nil {
		cleanup()
		return false, err
	}
	if err := temp.Sync(); err != nil {
		cleanup()
		return false, err
	}
	if err := temp.Close(); err != nil {
		_ = os.Remove(tempName)
		return false, err
	}
	if err := os.Rename(tempName, r.path); err != nil {
		_ = os.Remove(tempName)
		return false, err
	}
	// Rename commits the write; a directory-sync error leaves durability uncertain, not rollbackable.
	if err := r.syncDir(dir); err != nil {
		return true, err
	}
	return true, nil
}

func decodeState(data []byte) (storedState, error) {
	var state storedState
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&state); err != nil {
		return storedState{}, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return storedState{}, errors.New("trailing state data")
	}
	if err := validateStoredState(state); err != nil {
		return storedState{}, err
	}
	return state, nil
}

func validateStoredState(state storedState) error {
	if state.Version != stateVersion {
		return errors.New("unsupported state version")
	}
	if state.Connections == nil || state.Clusters == nil || state.Fleets == nil || state.Memberships == nil {
		return errors.New("state arrays are required")
	}
	if len(state.Connections) > MaxConnections || len(state.Clusters) > MaxConnections || len(state.Fleets) > MaxFleets ||
		len(state.Memberships) > MaxConnections*MaxFleets {
		return errors.New("state exceeds configured limits")
	}
	clusterIDs := make(map[string]struct{}, len(state.Clusters))
	clusterByID := make(map[string]storedCluster, len(state.Clusters))
	for _, cluster := range state.Clusters {
		if !validStableID(cluster.ID) || cluster.ConnectionID == "" {
			return errors.New("invalid cluster record")
		}
		if _, exists := clusterIDs[cluster.ID]; exists {
			return errors.New("duplicate cluster ID")
		}
		clusterIDs[cluster.ID] = struct{}{}
		clusterByID[cluster.ID] = cluster
		if cluster.Identity != IdentityUnresolved && cluster.Identity != IdentityVerified && cluster.Identity != IdentityConflict {
			return errors.New("invalid cluster identity state")
		}
		if (cluster.Identity == IdentityVerified || cluster.Identity == IdentityConflict) && cluster.IdentityUID == "" {
			return errors.New("verified cluster is missing identity evidence")
		}
		if len(cluster.IdentityUID) > 256 {
			return errors.New("cluster identity evidence exceeds limits")
		}
	}
	connectionIDs := make(map[string]struct{}, len(state.Connections))
	clusterByConnection := make(map[string]string, len(state.Connections))
	connectionByCluster := make(map[string]storedConnection, len(state.Connections))
	for _, connection := range state.Connections {
		if err := validateIdentifier("connection ID", connection.ID); err != nil {
			return err
		}
		if err := validateText("connection name", connection.Name, MaxTextLength); err != nil {
			return err
		}
		if _, exists := connectionIDs[connection.ID]; exists {
			return errors.New("duplicate connection ID")
		}
		connectionIDs[connection.ID] = struct{}{}
		if !validStableID(connection.ClusterID) {
			return errors.New("invalid connection cluster ID")
		}
		if !validFingerprint(connection.SourceFingerprint) {
			return errors.New("invalid source reference fingerprint")
		}
		if _, exists := clusterIDs[connection.ClusterID]; !exists {
			return errors.New("connection references unknown cluster")
		}
		if _, exists := connectionByCluster[connection.ClusterID]; exists {
			return errors.New("multiple connections reference one cluster")
		}
		cluster := clusterByID[connection.ClusterID]
		if cluster.ConnectionID != connection.ID || cluster.Active != connection.Active {
			return errors.New("connection cluster relationship is invalid")
		}
		clusterByConnection[connection.ID] = connection.ClusterID
		connectionByCluster[connection.ClusterID] = connection
	}
	for _, cluster := range state.Clusters {
		connection, exists := connectionByCluster[cluster.ID]
		if !exists || clusterByConnection[cluster.ConnectionID] != cluster.ID ||
			connection.ID != cluster.ConnectionID || connection.Active != cluster.Active {
			return errors.New("cluster connection relationship is invalid")
		}
	}
	fleetIDs := make(map[string]struct{}, len(state.Fleets))
	for _, fleet := range state.Fleets {
		if err := validateIdentifier("fleet ID", fleet.ID); err != nil {
			return err
		}
		if err := validateText("fleet name", fleet.Name, MaxTextLength); err != nil {
			return err
		}
		if _, exists := fleetIDs[fleet.ID]; exists {
			return errors.New("duplicate fleet ID")
		}
		fleetIDs[fleet.ID] = struct{}{}
	}
	membershipKeys := make(map[string]struct{}, len(state.Memberships))
	for _, membership := range state.Memberships {
		if _, fleetExists := fleetIDs[membership.FleetID]; !fleetExists {
			return errors.New("membership references unknown fleet")
		}
		if _, clusterExists := clusterIDs[membership.ClusterID]; !clusterExists {
			return errors.New("membership references unknown cluster")
		}
		key := membership.FleetID + "\x00" + membership.ClusterID
		if _, exists := membershipKeys[key]; exists {
			return errors.New("duplicate membership")
		}
		membershipKeys[key] = struct{}{}
	}
	return nil
}

func syncParentDirectory(dir string) error {
	dirFile, err := os.Open(dir)
	if err != nil {
		return err
	}
	syncErr := dirFile.Sync()
	closeErr := dirFile.Close()
	if syncErr != nil {
		return syncErr
	}
	return closeErr
}

func emptyState() storedState {
	return storedState{
		Version:     stateVersion,
		Connections: []storedConnection{},
		Clusters:    []storedCluster{},
		Fleets:      []storedFleet{},
		Memberships: []storedMembership{},
	}
}

func cloneStoredState(state storedState) storedState {
	clone := state
	clone.Connections = append(make([]storedConnection, 0, len(state.Connections)), state.Connections...)
	clone.Clusters = append(make([]storedCluster, 0, len(state.Clusters)), state.Clusters...)
	clone.Fleets = append(make([]storedFleet, 0, len(state.Fleets)), state.Fleets...)
	clone.Memberships = append(make([]storedMembership, 0, len(state.Memberships)), state.Memberships...)
	return clone
}

func removeFleetMemberships(memberships []storedMembership, fleetID string) []storedMembership {
	out := memberships[:0]
	for _, membership := range memberships {
		if membership.FleetID != fleetID {
			out = append(out, membership)
		}
	}
	return out
}

func validateClusterIDs(clusterIDs []string) error {
	seen := make(map[string]struct{}, len(clusterIDs))
	for _, clusterID := range clusterIDs {
		if !validStableID(clusterID) {
			return errors.New("invalid fleet cluster ID")
		}
		if _, exists := seen[clusterID]; exists {
			return errors.New("duplicate fleet cluster ID")
		}
		seen[clusterID] = struct{}{}
	}
	return nil
}

func sourceFingerprint(source SourceSpec) string {
	path := source.Kubeconfig
	if path != "" {
		if absolute, err := filepath.Abs(path); err == nil {
			path = filepath.Clean(absolute)
		}
	}
	sum := sha256.Sum256([]byte(string(source.Type) + "\x00" + path + "\x00" + source.Context))
	return hex.EncodeToString(sum[:])
}

func newStableID() (string, error) {
	var raw [16]byte
	if _, err := io.ReadFull(rand.Reader, raw[:]); err != nil {
		return "", err
	}
	raw[6] = (raw[6] & 0x0f) | 0x40
	raw[8] = (raw[8] & 0x3f) | 0x80
	return fmt.Sprintf("%s-%s-%s-%s-%s",
		hex.EncodeToString(raw[0:4]),
		hex.EncodeToString(raw[4:6]),
		hex.EncodeToString(raw[6:8]),
		hex.EncodeToString(raw[8:10]),
		hex.EncodeToString(raw[10:16]),
	), nil
}

func validStableID(value string) bool {
	if len(value) != 36 {
		return false
	}
	for i, r := range value {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if r != '-' {
				return false
			}
			continue
		}
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			return false
		}
	}
	return true
}

func validFingerprint(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil && strings.ToLower(value) == value
}

func ensureParent(dir string) error {
	info, err := os.Stat(dir)
	if err == nil {
		if !info.IsDir() {
			return errors.New("state parent is not a directory")
		}
		return nil
	}
	if !os.IsNotExist(err) {
		return err
	}
	return os.MkdirAll(dir, 0o700)
}

func (r *Registry) String() string {
	if r == nil {
		return "<nil>"
	}
	return strings.TrimSpace(r.path)
}
