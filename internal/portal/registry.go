package portal

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
)

const (
	maxRegistrationBytes = 64 << 10
	maxRegistryBytes     = 16 << 20
	staleAfter           = 90 * time.Second
)

var clusterIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

type Registration struct {
	ID              string          `json:"id"`
	Name            string          `json:"name"`
	Description     string          `json:"description"`
	Environment     string          `json:"environment"`
	Region          string          `json:"region"`
	RuntimeMode     string          `json:"runtimeMode"`
	KubeContext     string          `json:"kubeContext"`
	ClusterName     string          `json:"clusterName"`
	RadarURL        string          `json:"radarUrl"`
	RadarVersion    string          `json:"radarVersion"`
	ConnectionState string          `json:"connectionState"`
	AuthMode        string          `json:"authMode"`
	MCP             MCPRegistration `json:"mcp"`
}

type MCPRegistration struct {
	Mode        string `json:"mode"`
	Transport   string `json:"transport"`
	URL         string `json:"url,omitempty"`
	ReadOnlyURL string `json:"readOnlyUrl,omitempty"`
}

type Cluster struct {
	Registration
	RegisteredAt time.Time `json:"registeredAt"`
	LastSeen     time.Time `json:"lastSeen"`
	Status       string    `json:"status"`
}

type ClusterList struct {
	Clusters          []Cluster `json:"clusters"`
	ServerTime        time.Time `json:"serverTime"`
	StaleAfterSeconds int       `json:"staleAfterSeconds"`
}

type storedCluster struct {
	Registration
	RegisteredAt time.Time `json:"registeredAt"`
	LastSeen     time.Time `json:"lastSeen"`
}

type registryFile struct {
	Version  int                      `json:"version"`
	Clusters map[string]storedCluster `json:"clusters"`
}

type Registry struct {
	mu       sync.Mutex
	path     string
	clusters map[string]storedCluster
	now      func() time.Time
}

func OpenRegistry(path string) (*Registry, error) {
	r := &Registry{path: path, clusters: make(map[string]storedCluster), now: time.Now}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return r, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read portal registry: %w", err)
	}
	if len(data) > maxRegistryBytes {
		return nil, fmt.Errorf("portal registry exceeds %d bytes", maxRegistryBytes)
	}
	var file registryFile
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&file); err != nil {
		return nil, fmt.Errorf("decode portal registry: %w", err)
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return nil, errors.New("decode portal registry: trailing JSON data")
	}
	if file.Version != 1 || file.Clusters == nil {
		return nil, errors.New("portal registry has an unsupported or incomplete format")
	}
	for id, cluster := range file.Clusters {
		if id != cluster.ID || ValidateRegistration(cluster.Registration) != nil || cluster.RegisteredAt.IsZero() || cluster.LastSeen.IsZero() {
			return nil, fmt.Errorf("portal registry contains an invalid record for %q", id)
		}
		r.clusters[id] = cluster
	}
	return r, nil
}

func (r *Registry) Register(reg Registration) error {
	if err := ValidateRegistration(reg); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	now := r.now().UTC()
	record := storedCluster{Registration: reg, RegisteredAt: now, LastSeen: now}
	if previous, ok := r.clusters[reg.ID]; ok {
		record.RegisteredAt = previous.RegisteredAt
	}
	next := make(map[string]storedCluster, len(r.clusters)+1)
	for id, cluster := range r.clusters {
		next[id] = cluster
	}
	next[reg.ID] = record
	if err := r.save(next); err != nil {
		return err
	}
	r.clusters = next
	return nil
}

func (r *Registry) List(authorizedIDs map[string]struct{}) ClusterList {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now().UTC()
	clusters := make([]Cluster, 0, len(authorizedIDs))
	for id := range authorizedIDs {
		record, ok := r.clusters[id]
		if !ok {
			continue
		}
		status := "online"
		if now.Sub(record.LastSeen) >= staleAfter {
			status = "stale"
		}
		clusters = append(clusters, Cluster{
			Registration: record.Registration,
			RegisteredAt: record.RegisteredAt,
			LastSeen:     record.LastSeen,
			Status:       status,
		})
	}
	sort.Slice(clusters, func(i, j int) bool { return clusters[i].ID < clusters[j].ID })
	return ClusterList{Clusters: clusters, ServerTime: now, StaleAfterSeconds: int(staleAfter.Seconds())}
}

func (r *Registry) save(clusters map[string]storedCluster) error {
	data, err := json.Marshal(registryFile{Version: 1, Clusters: clusters})
	if err != nil {
		return fmt.Errorf("encode portal registry: %w", err)
	}
	if len(data) > maxRegistryBytes {
		return fmt.Errorf("portal registry exceeds %d bytes", maxRegistryBytes)
	}
	dir := filepath.Dir(r.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create portal registry directory: %w", err)
	}
	temp, err := os.CreateTemp(dir, ".registry-*.tmp")
	if err != nil {
		return fmt.Errorf("create portal registry temporary file: %w", err)
	}
	tempName := temp.Name()
	defer os.Remove(tempName)
	if err := temp.Chmod(0o600); err != nil {
		temp.Close()
		return fmt.Errorf("secure portal registry temporary file: %w", err)
	}
	if _, err := temp.Write(data); err != nil {
		temp.Close()
		return fmt.Errorf("write portal registry: %w", err)
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return fmt.Errorf("sync portal registry: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("close portal registry: %w", err)
	}
	if err := os.Rename(tempName, r.path); err != nil {
		return fmt.Errorf("replace portal registry: %w", err)
	}
	directory, err := os.Open(dir)
	if err == nil {
		_ = directory.Sync()
		_ = directory.Close()
	}
	return nil
}

func ValidateRegistration(reg Registration) error {
	if !clusterIDPattern.MatchString(reg.ID) {
		return errors.New("invalid cluster id")
	}
	for name, value := range map[string]string{
		"name": reg.Name, "description": reg.Description, "environment": reg.Environment,
		"region": reg.Region, "radarVersion": reg.RadarVersion,
		"kubeContext": reg.KubeContext, "clusterName": reg.ClusterName,
	} {
		limit := 200
		switch name {
		case "description":
			limit = 1000
		case "kubeContext", "clusterName":
			limit = 1024
		}
		unsafe := hasUnsafeText(value)
		if name == "kubeContext" || name == "clusterName" {
			unsafe = hasControlCharacters(value)
		}
		if len(value) > limit || unsafe {
			return fmt.Errorf("invalid %s", name)
		}
	}
	if reg.RuntimeMode != "local" && reg.RuntimeMode != "in-cluster" {
		return errors.New("invalid runtimeMode")
	}
	if strings.TrimSpace(reg.Name) == "" || strings.TrimSpace(reg.RadarVersion) == "" {
		return errors.New("name and radarVersion are required")
	}
	if err := validatePublicURL(reg.RadarURL); err != nil {
		return errors.New("invalid radarUrl")
	}
	if reg.ConnectionState != "connected" && reg.ConnectionState != "connecting" && reg.ConnectionState != "disconnected" {
		return errors.New("invalid connectionState")
	}
	if reg.AuthMode != "none" && reg.AuthMode != "proxy" && reg.AuthMode != "oidc" {
		return errors.New("invalid authMode")
	}
	if reg.MCP.Transport != "streamable-http" {
		return errors.New("invalid mcp.transport")
	}
	switch reg.MCP.Mode {
	case "full":
		if reg.MCP.URL == "" || reg.MCP.ReadOnlyURL == "" ||
			!isEndpoint(reg.MCP.URL, "/mcp") || !isEndpoint(reg.MCP.ReadOnlyURL, "/mcp-readonly") {
			return errors.New("invalid mcp endpoints for full mode")
		}
	case "read-only":
		if reg.MCP.URL != "" || reg.MCP.ReadOnlyURL == "" || !isEndpoint(reg.MCP.ReadOnlyURL, "/mcp-readonly") {
			return errors.New("invalid mcp endpoints for read-only mode")
		}
	case "disabled":
		if reg.MCP.URL != "" || reg.MCP.ReadOnlyURL != "" {
			return errors.New("disabled mcp must not include endpoints")
		}
	default:
		return errors.New("invalid mcp.mode")
	}
	return nil
}

func isEndpoint(candidate, suffix string) bool {
	if err := validatePublicURL(candidate); err != nil {
		return false
	}
	candidateURL, _ := url.Parse(candidate)
	return strings.HasSuffix(candidateURL.Path, suffix)
}

func validatePublicURL(value string) error {
	if value == "" || hasUnsafeText(value) || strings.Contains(value, "\\") {
		return errors.New("invalid URL")
	}
	parsed, err := url.Parse(value)
	if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") ||
		parsed.Host == "" || parsed.Hostname() == "" || parsed.User != nil ||
		parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" ||
		strings.Contains(value, "?") || strings.Contains(value, "#") {
		return errors.New("invalid URL")
	}
	return nil
}

func hasUnsafeText(value string) bool {
	return hasControlCharacters(value) || strings.Contains(value, "\\")
}

func hasControlCharacters(value string) bool {
	for _, r := range value {
		if unicode.IsControl(r) {
			return true
		}
	}
	return false
}
