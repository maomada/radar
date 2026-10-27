package portalagent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/skyhook-io/radar/internal/portal"
)

const (
	heartbeatInterval = 30 * time.Second
	requestTimeout    = 5 * time.Second
)

type Config struct {
	PortalURL   string
	Token       string
	ClusterID   string
	ClusterName string
	Description string
	Environment string
	Region      string
	RadarURL    string
	MCPBaseURL  string
	AllowHTTP   bool
}

type Snapshot struct {
	RadarVersion    string
	ConnectionState string
	AuthMode        string
	RuntimeMode     string
	KubeContext     string
	ClusterName     string
	MCPEnabled      bool
	MCPReadOnly     bool
}

type Reporter struct {
	config   Config
	snapshot func() Snapshot
	client   *http.Client
	endpoint string
	interval time.Duration
}

func ConfigFromEnv() (Config, bool, error) {
	enabled := false
	for _, name := range []string{
		"RADAR_PORTAL_URL", "RADAR_PORTAL_TOKEN", "RADAR_PORTAL_CLUSTER_ID", "RADAR_PORTAL_CLUSTER_NAME",
		"RADAR_PORTAL_DESCRIPTION", "RADAR_PORTAL_ENVIRONMENT", "RADAR_PORTAL_REGION", "RADAR_PORTAL_RADAR_URL",
		"RADAR_PORTAL_MCP_BASE_URL", "RADAR_PORTAL_ALLOW_HTTP",
	} {
		enabled = enabled || os.Getenv(name) != ""
	}
	if !enabled {
		return Config{}, false, nil
	}
	allowHTTP := false
	if value := os.Getenv("RADAR_PORTAL_ALLOW_HTTP"); value != "" {
		parsed, err := strconv.ParseBool(value)
		if err != nil {
			return Config{}, false, errors.New("RADAR_PORTAL_ALLOW_HTTP must be a boolean")
		}
		allowHTTP = parsed
	}
	config := Config{
		PortalURL:   os.Getenv("RADAR_PORTAL_URL"),
		Token:       os.Getenv("RADAR_PORTAL_TOKEN"),
		ClusterID:   os.Getenv("RADAR_PORTAL_CLUSTER_ID"),
		ClusterName: os.Getenv("RADAR_PORTAL_CLUSTER_NAME"),
		Description: os.Getenv("RADAR_PORTAL_DESCRIPTION"),
		Environment: os.Getenv("RADAR_PORTAL_ENVIRONMENT"),
		Region:      os.Getenv("RADAR_PORTAL_REGION"),
		RadarURL:    os.Getenv("RADAR_PORTAL_RADAR_URL"),
		MCPBaseURL:  os.Getenv("RADAR_PORTAL_MCP_BASE_URL"),
		AllowHTTP:   allowHTTP,
	}
	if config.MCPBaseURL == "" {
		config.MCPBaseURL = config.RadarURL
	}
	if err := validateConfig(config); err != nil {
		return Config{}, false, err
	}
	return config, true, nil
}

func New(config Config, snapshot func() Snapshot) (*Reporter, error) {
	if snapshot == nil {
		return nil, errors.New("portal snapshot function is required")
	}
	if err := validateConfig(config); err != nil {
		return nil, err
	}
	endpoint := strings.TrimRight(config.PortalURL, "/") + "/api/registrations/" + config.ClusterID
	return &Reporter{
		config: config, snapshot: snapshot, endpoint: endpoint, interval: heartbeatInterval,
		client: &http.Client{
			Timeout: requestTimeout,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}, nil
}

func validateConfig(config Config) error {
	portalURL, err := url.Parse(config.PortalURL)
	if err != nil || portalURL.Host == "" || portalURL.User != nil || portalURL.RawQuery != "" ||
		portalURL.ForceQuery || portalURL.Fragment != "" ||
		(portalURL.Path != "" && portalURL.Path != "/") ||
		strings.Contains(config.PortalURL, "?") || strings.Contains(config.PortalURL, "#") ||
		(portalURL.Scheme != "https" && !(portalURL.Scheme == "http" && config.AllowHTTP)) ||
		strings.Contains(config.PortalURL, "\\") || hasControl(config.PortalURL) {
		return errors.New("invalid portal URL or insecure HTTP configuration")
	}
	if len(config.Token) < 32 || !validID(config.ClusterID) {
		return errors.New("portal registration identity is invalid")
	}
	if strings.TrimSpace(config.ClusterName) == "" || len(config.ClusterName) > 200 ||
		len(config.Description) > 1000 || len(config.Environment) > 200 || len(config.Region) > 200 ||
		hasControl(config.ClusterName) || hasControl(config.Description) ||
		hasControl(config.Environment) || hasControl(config.Region) {
		return errors.New("portal registration metadata is invalid")
	}
	if !validPublicURL(config.RadarURL) || !validPublicURL(config.MCPBaseURL) {
		return errors.New("invalid Radar public URL")
	}
	return nil
}

func validID(value string) bool {
	if value == "" || len(value) > 63 {
		return false
	}
	for i, r := range value {
		if i == 0 && (r == '-') {
			return false
		}
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' {
			return false
		}
	}
	return true
}

func validPublicURL(value string) bool {
	if value == "" || strings.Contains(value, "\\") || hasControl(value) {
		return false
	}
	parsed, err := url.Parse(value)
	return err == nil && (parsed.Scheme == "https" || parsed.Scheme == "http") &&
		parsed.Hostname() != "" && parsed.User == nil && parsed.RawQuery == "" &&
		!parsed.ForceQuery && parsed.Fragment == "" &&
		!strings.Contains(value, "?") && !strings.Contains(value, "#")
}

func hasControl(value string) bool {
	for _, r := range value {
		if unicode.IsControl(r) {
			return true
		}
	}
	return false
}

func (r *Reporter) Run(ctx context.Context, onError func(error)) {
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	for {
		if err := r.Register(ctx); err != nil && onError != nil && ctx.Err() == nil {
			onError(err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (r *Reporter) Register(ctx context.Context) error {
	registration := r.registration(r.snapshot())
	if portal.ValidateRegistration(registration) != nil {
		return errors.New("portal registration metadata is invalid")
	}
	body, err := json.Marshal(registration)
	if err != nil {
		return errors.New("portal registration could not be encoded")
	}
	requestCtx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodPut, r.endpoint, bytes.NewReader(body))
	if err != nil {
		return errors.New("portal registration request could not be created")
	}
	request.Header.Set("Authorization", "Bearer "+r.config.Token)
	request.Header.Set("Content-Type", "application/json")
	response, err := r.client.Do(request)
	if err != nil {
		return errors.New("portal registration request failed")
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("portal registration returned HTTP %d", response.StatusCode)
	}
	return nil
}

func (r *Reporter) registration(snapshot Snapshot) portal.Registration {
	base := strings.TrimRight(r.config.MCPBaseURL, "/")
	registration := portal.Registration{
		ID: r.config.ClusterID, Name: r.config.ClusterName, Description: r.config.Description,
		Environment: r.config.Environment, Region: r.config.Region,
		RuntimeMode: snapshot.RuntimeMode, KubeContext: snapshot.KubeContext, ClusterName: snapshot.ClusterName,
		RadarURL:     r.config.RadarURL,
		RadarVersion: snapshot.RadarVersion, ConnectionState: snapshot.ConnectionState,
		AuthMode: snapshot.AuthMode,
		MCP:      portal.MCPRegistration{Transport: "streamable-http", Mode: "disabled"},
	}
	switch {
	case !snapshot.MCPEnabled:
	case snapshot.MCPReadOnly:
		registration.MCP.Mode = "read-only"
		registration.MCP.ReadOnlyURL = base + "/mcp-readonly"
	default:
		registration.MCP.Mode = "full"
		registration.MCP.URL = base + "/mcp"
		registration.MCP.ReadOnlyURL = base + "/mcp-readonly"
	}
	return registration
}
