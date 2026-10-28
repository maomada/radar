package fleet

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode"

	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

const (
	ConfigVersion       = 1
	MaxConfigBytes      = 1 << 20
	MaxConnections      = 64
	MaxFleets           = 64
	MaxNamespaces       = 64
	MaxIdentifierLength = 64
	MaxTextLength       = 1024
)

type SourceType string

const (
	SourceKubeconfig SourceType = "kubeconfig"
	SourceInCluster  SourceType = "in-cluster"
)

type Config struct {
	Version     int              `json:"version"`
	Connections []ConnectionSpec `json:"connections"`
	Fleets      []FleetSpec      `json:"fleets"`
}

type ConnectionSpec struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Source     SourceSpec `json:"source"`
	Namespaces []string   `json:"namespaces,omitempty"`
}

type SourceSpec struct {
	Type               SourceType `json:"type"`
	Kubeconfig         string     `json:"kubeconfig,omitempty"`
	Context            string     `json:"context,omitempty"`
	hasKubeconfigField bool
	hasContextField    bool
}

type FleetSpec struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Connections []string `json:"connections"`
}

type ConnectionCandidate struct {
	SourceFile         string `json:"sourceFile"`
	RawContext         string `json:"rawContext"`
	DisplayClusterName string `json:"displayClusterName"`
}

type resolvedSource struct {
	typ         SourceType
	kubeconfig  string
	context     string
	clusterName string
	rawConfig   *clientcmdapi.Config
}

var identifierPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

func (s *SourceSpec) UnmarshalJSON(data []byte) error {
	var fields struct {
		Type       SourceType      `json:"type"`
		Kubeconfig json.RawMessage `json:"kubeconfig"`
		Context    json.RawMessage `json:"context"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&fields); err != nil {
		return sanitizeJSONError(err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("invalid source configuration")
	}
	s.Type = fields.Type
	s.hasKubeconfigField = fields.Kubeconfig != nil
	s.hasContextField = fields.Context != nil
	if s.hasKubeconfigField {
		if bytes.Equal(bytes.TrimSpace(fields.Kubeconfig), []byte("null")) ||
			json.Unmarshal(fields.Kubeconfig, &s.Kubeconfig) != nil {
			return errors.New("invalid kubeconfig source path")
		}
	}
	if s.hasContextField {
		if bytes.Equal(bytes.TrimSpace(fields.Context), []byte("null")) ||
			json.Unmarshal(fields.Context, &s.Context) != nil {
			return errors.New("invalid kubeconfig context")
		}
	}
	return nil
}

func LoadConfig(path string) (Config, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return Config{}, errors.New("fleet config path is invalid")
	}
	data, err := readBoundedFile(absolute, MaxConfigBytes)
	if err != nil {
		return Config{}, fmt.Errorf("read fleet config %q: %w", filepath.Base(absolute), err)
	}
	config, err := ParseConfig(data)
	if err != nil {
		return Config{}, err
	}
	return resolveConfigPaths(config, filepath.Dir(absolute))
}

func ParseConfig(data []byte) (Config, error) {
	if len(data) > MaxConfigBytes {
		return Config{}, fmt.Errorf("fleet config exceeds %d bytes", MaxConfigBytes)
	}
	var config Config
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&config); err != nil {
		return Config{}, fmt.Errorf("invalid fleet config: %w", sanitizeJSONError(err))
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return Config{}, errors.New("invalid fleet config: trailing JSON data")
		}
		return Config{}, fmt.Errorf("invalid fleet config: %w", sanitizeJSONError(err))
	}
	if err := ValidateConfig(config); err != nil {
		return Config{}, err
	}
	return config, nil
}

func ValidateConfig(config Config) error {
	if config.Version != ConfigVersion {
		return fmt.Errorf("unsupported fleet config version %d", config.Version)
	}
	if config.Connections == nil || config.Fleets == nil {
		return errors.New("fleet config must contain connections and fleets arrays")
	}
	if len(config.Connections) > MaxConnections {
		return fmt.Errorf("fleet config has more than %d connections", MaxConnections)
	}
	if len(config.Fleets) > MaxFleets {
		return fmt.Errorf("fleet config has more than %d fleets", MaxFleets)
	}
	connectionIDs := make(map[string]struct{}, len(config.Connections))
	for _, connection := range config.Connections {
		if err := validateIdentifier("connection ID", connection.ID); err != nil {
			return err
		}
		if _, exists := connectionIDs[connection.ID]; exists {
			return fmt.Errorf("duplicate connection ID %q", connection.ID)
		}
		connectionIDs[connection.ID] = struct{}{}
		if err := validateText("connection name", connection.Name, MaxTextLength); err != nil {
			return err
		}
		if err := validateSource(connection.Source); err != nil {
			return fmt.Errorf("connection %q: %w", connection.ID, err)
		}
		if len(connection.Namespaces) > MaxNamespaces {
			return fmt.Errorf("connection %q has more than %d namespaces", connection.ID, MaxNamespaces)
		}
		seenNamespaces := make(map[string]struct{}, len(connection.Namespaces))
		for _, namespace := range connection.Namespaces {
			if _, exists := seenNamespaces[namespace]; exists {
				return fmt.Errorf("connection %q has duplicate namespace %q", connection.ID, namespace)
			}
			seenNamespaces[namespace] = struct{}{}
			if errs := validation.IsDNS1123Label(namespace); len(errs) > 0 {
				return fmt.Errorf("connection %q has invalid namespace %q", connection.ID, namespace)
			}
		}
	}
	fleetIDs := make(map[string]struct{}, len(config.Fleets))
	for _, fleet := range config.Fleets {
		if err := validateIdentifier("fleet ID", fleet.ID); err != nil {
			return err
		}
		if _, exists := fleetIDs[fleet.ID]; exists {
			return fmt.Errorf("duplicate fleet ID %q", fleet.ID)
		}
		fleetIDs[fleet.ID] = struct{}{}
		if err := validateText("fleet name", fleet.Name, MaxTextLength); err != nil {
			return err
		}
		if len(fleet.Connections) > MaxConnections {
			return fmt.Errorf("fleet %q has too many connections", fleet.ID)
		}
		seenConnections := make(map[string]struct{}, len(fleet.Connections))
		for _, connectionID := range fleet.Connections {
			if _, exists := seenConnections[connectionID]; exists {
				return fmt.Errorf("fleet %q has duplicate connection %q", fleet.ID, connectionID)
			}
			if _, exists := connectionIDs[connectionID]; !exists {
				return fmt.Errorf("fleet %q references unknown connection %q", fleet.ID, connectionID)
			}
			seenConnections[connectionID] = struct{}{}
		}
	}
	return nil
}

func validateSource(source SourceSpec) error {
	switch source.Type {
	case SourceKubeconfig:
		if err := validateText("kubeconfig path", source.Kubeconfig, MaxTextLength); err != nil {
			return err
		}
		if err := validateText("kubeconfig context", source.Context, MaxTextLength); err != nil {
			return errors.New("kubeconfig source requires an explicit context")
		}
		if source.Context == "" {
			return errors.New("kubeconfig source requires an explicit context")
		}
	case SourceInCluster:
		if source.hasKubeconfigField || source.hasContextField || source.Kubeconfig != "" || source.Context != "" {
			return errors.New("in-cluster source cannot include kubeconfig or context")
		}
	default:
		return fmt.Errorf("unsupported source type %q", source.Type)
	}
	return nil
}

func validateIdentifier(label, value string) error {
	if value == "" {
		return fmt.Errorf("%s must not be empty", label)
	}
	if len(value) > MaxIdentifierLength || !identifierPattern.MatchString(value) {
		return fmt.Errorf("%s %q is invalid", label, value)
	}
	return nil
}

func validateText(label, value string, max int) error {
	if value == "" {
		return fmt.Errorf("%s must not be empty", label)
	}
	if len(value) > max {
		return fmt.Errorf("%s exceeds %d characters", label, max)
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return fmt.Errorf("%s contains a control character", label)
		}
	}
	return nil
}

func resolveConfigPaths(config Config, baseDir string) (Config, error) {
	resolved := cloneConfig(config)
	for i := range resolved.Connections {
		source := &resolved.Connections[i].Source
		if source.Type != SourceKubeconfig || filepath.IsAbs(source.Kubeconfig) {
			continue
		}
		path, err := filepath.Abs(filepath.Join(baseDir, source.Kubeconfig))
		if err != nil {
			return Config{}, fmt.Errorf("connection %q has invalid kubeconfig path", resolved.Connections[i].ID)
		}
		source.Kubeconfig = filepath.Clean(path)
	}
	return resolved, nil
}

func cloneConfig(config Config) Config {
	clone := Config{Version: config.Version}
	clone.Connections = make([]ConnectionSpec, len(config.Connections))
	for i, connection := range config.Connections {
		clone.Connections[i] = connection
		clone.Connections[i].Namespaces = append([]string(nil), connection.Namespaces...)
	}
	clone.Fleets = make([]FleetSpec, len(config.Fleets))
	for i, fleet := range config.Fleets {
		clone.Fleets[i] = fleet
		clone.Fleets[i].Connections = append([]string(nil), fleet.Connections...)
	}
	return clone
}

func DiscoverContexts(paths []string) ([]ConnectionCandidate, error) {
	candidates := make([]ConnectionCandidate, 0)
	for _, path := range paths {
		absolute, err := filepath.Abs(path)
		if err != nil {
			return nil, errors.New("kubeconfig path is invalid")
		}
		raw, err := clientcmd.LoadFromFile(absolute)
		if err != nil {
			return nil, fmt.Errorf("load kubeconfig %q: invalid configuration", filepath.Base(absolute))
		}
		names := make([]string, 0, len(raw.Contexts))
		for name := range raw.Contexts {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			context := raw.Contexts[name]
			if context == nil {
				continue
			}
			candidates = append(candidates, ConnectionCandidate{
				SourceFile:         absolute,
				RawContext:         name,
				DisplayClusterName: context.Cluster,
			})
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].SourceFile != candidates[j].SourceFile {
			return candidates[i].SourceFile < candidates[j].SourceFile
		}
		return candidates[i].RawContext < candidates[j].RawContext
	})
	return candidates, nil
}

func resolveSource(spec ConnectionSpec) (resolvedSource, error) {
	if err := validateSource(spec.Source); err != nil {
		return resolvedSource{}, err
	}
	switch spec.Source.Type {
	case SourceInCluster:
		return resolvedSource{typ: SourceInCluster}, nil
	case SourceKubeconfig:
		raw, err := clientcmd.LoadFromFile(spec.Source.Kubeconfig)
		if err != nil {
			return resolvedSource{}, fmt.Errorf("connection %q kubeconfig is invalid", spec.ID)
		}
		contextConfig, ok := raw.Contexts[spec.Source.Context]
		if !ok || contextConfig == nil {
			return resolvedSource{}, fmt.Errorf("connection %q context is unavailable", spec.ID)
		}
		return resolvedSource{
			typ:         SourceKubeconfig,
			kubeconfig:  spec.Source.Kubeconfig,
			context:     spec.Source.Context,
			clusterName: contextConfig.Cluster,
			rawConfig:   raw,
		}, nil
	default:
		return resolvedSource{}, errors.New("unsupported connection source")
	}
}

func readBoundedFile(path string, max int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > max {
		return nil, fmt.Errorf("file exceeds %d bytes", max)
	}
	return data, nil
}

func sanitizeJSONError(err error) error {
	if err == nil {
		return nil
	}
	message := err.Error()
	if strings.Contains(strings.ToLower(message), "unknown field") {
		return errors.New("unknown field")
	}
	return errors.New(message)
}
