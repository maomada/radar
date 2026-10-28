package fleet

import "time"

type IdentityStatus string

const (
	IdentityUnresolved IdentityStatus = "unresolved"
	IdentityVerified   IdentityStatus = "verified"
	IdentityConflict   IdentityStatus = "conflict"
)

type ConnectionState string

const (
	StateConnecting       ConnectionState = "connecting"
	StateReady            ConnectionState = "ready"
	StatePartial          ConnectionState = "partial"
	StateUnavailable      ConnectionState = "unavailable"
	StateIdentityConflict ConnectionState = "identity_conflict"
)

type ProbeStatus string

const (
	ProbeAllowed         ProbeStatus = "allowed"
	ProbeDenied          ProbeStatus = "denied"
	ProbeUnavailable     ProbeStatus = "unavailable"
	ProbeUnauthenticated ProbeStatus = "unauthenticated"
	ProbeNotProbed       ProbeStatus = "not_probed"
)

type KindCoverage struct {
	Kind       string      `json:"kind"`
	Probe      ProbeStatus `json:"probe"`
	Scope      string      `json:"scope,omitempty"`
	Namespaces []string    `json:"namespaces,omitempty"`
	Readiness  string      `json:"readiness"`
}

type ConnectionStatus struct {
	ConnectionID        string          `json:"connectionId"`
	ClusterID           string          `json:"clusterId"`
	Name                string          `json:"name"`
	State               ConnectionState `json:"state"`
	Identity            IdentityStatus  `json:"identity"`
	IdentityObservedAt  time.Time       `json:"identityObservedAt,omitempty"`
	LastAttemptAt       time.Time       `json:"lastAttemptAt,omitempty"`
	LastReadyAt         time.Time       `json:"lastReadyAt,omitempty"`
	DuplicateCandidates []string        `json:"duplicateCandidates,omitempty"`
	Kinds               []KindCoverage  `json:"kinds"`
}

type ConnectionRecord struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	ClusterID string `json:"clusterId"`
	Active    bool   `json:"active"`
}

type ClusterRecord struct {
	ID                 string         `json:"id"`
	ConnectionID       string         `json:"connectionId"`
	Active             bool           `json:"active"`
	Identity           IdentityStatus `json:"identity"`
	IdentityUID        string         `json:"identityUid,omitempty"`
	IdentityObservedAt time.Time      `json:"identityObservedAt,omitempty"`
}

type FleetRecord struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Active      bool   `json:"active"`
	Declarative bool   `json:"declarative,omitempty"`
}

type MembershipRecord struct {
	FleetID   string `json:"fleetId"`
	ClusterID string `json:"clusterId"`
}

type Catalog struct {
	Connections []ConnectionRecord `json:"connections"`
	Clusters    []ClusterRecord    `json:"clusters"`
	Fleets      []FleetRecord      `json:"fleets"`
	Memberships []MembershipRecord `json:"memberships"`
}
