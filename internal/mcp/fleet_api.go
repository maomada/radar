package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/skyhook-io/radar/internal/fleet"
	pkgauth "github.com/skyhook-io/radar/pkg/auth"
)

const fleetAPIRequestLimit = 1 << 20

type FleetOptions struct {
	RequireAuth      bool
	AdminUsers       []string
	LocalConnections bool
	Candidates       fleet.CandidateProvider
}

type fleetService struct {
	tools   fleetTools
	options FleetOptions
	admins  map[string]struct{}
	manager *fleet.Manager
}

func NewFleetEndpoints(manager *fleet.Manager, options FleetOptions) (http.Handler, http.Handler) {
	if manager == nil {
		return http.NotFoundHandler(), http.NotFoundHandler()
	}
	admins := make(map[string]struct{}, len(options.AdminUsers))
	for _, username := range options.AdminUsers {
		if username = strings.TrimSpace(username); username != "" {
			admins[username] = struct{}{}
		}
	}
	service := &fleetService{
		manager: manager, options: options, admins: admins,
		tools: fleetTools{source: manager, requireAuth: options.RequireAuth},
	}
	return handlerForServer(newFleetServer(service.tools)), service
}

func (s *fleetService) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/capabilities":
		s.capabilities(w, r)
	case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/query/"):
		s.query(w, r, strings.TrimPrefix(r.URL.Path, "/query/"))
	case r.Method == http.MethodGet && r.URL.Path == "/candidates":
		s.candidates(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/connections":
		s.addConnection(w, r)
	case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/connections/"):
		s.removeConnection(w, r, strings.TrimPrefix(r.URL.Path, "/connections/"))
	case r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, "/fleets/"):
		s.upsertFleet(w, r, strings.TrimPrefix(r.URL.Path, "/fleets/"))
	case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/fleets/"):
		s.deleteFleet(w, r, strings.TrimPrefix(r.URL.Path, "/fleets/"))
	case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/clusters/") && strings.HasSuffix(r.URL.Path, "/refresh"):
		clusterID := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/clusters/"), "/refresh")
		s.refresh(w, r, clusterID)
	case r.Method == http.MethodPost && r.URL.Path == "/duplicates":
		s.duplicate(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (s *fleetService) readAuthorized(ctxUser *pkgauth.User) error {
	if s.tools.requireAuth && (ctxUser == nil || ctxUser.Username == "") {
		return errors.New("Fleet requires an authenticated caller")
	}
	if pkgauth.UserTokenMode() {
		return errors.New("Fleet does not forward user tokens between clusters")
	}
	return nil
}

func (s *fleetService) managementAuthorized(r *http.Request) error {
	user := pkgauth.UserFromContext(r.Context())
	if err := s.readAuthorized(user); err != nil {
		return err
	}
	if !s.options.RequireAuth {
		return nil
	}
	if user == nil {
		return errors.New("Fleet administration requires an authenticated caller")
	}
	if _, ok := s.admins[user.Username]; !ok {
		return errors.New("Fleet administration requires an approved Fleet admin")
	}
	return nil
}

func (s *fleetService) capabilities(w http.ResponseWriter, r *http.Request) {
	if err := s.readAuthorized(pkgauth.UserFromContext(r.Context())); err != nil {
		writeFleetError(w, http.StatusUnauthorized, err)
		return
	}
	canManage := !s.options.RequireAuth
	if s.options.RequireAuth {
		if user := pkgauth.UserFromContext(r.Context()); user != nil {
			_, canManage = s.admins[user.Username]
		}
	}
	writeFleetJSON(w, http.StatusOK, map[string]any{
		"enabled": true, "canManage": canManage,
		"localConnections": s.options.LocalConnections, "writesEnabled": false,
	})
}

func (s *fleetService) query(w http.ResponseWriter, r *http.Request, name string) {
	if err := s.readAuthorized(pkgauth.UserFromContext(r.Context())); err != nil {
		writeFleetError(w, http.StatusUnauthorized, err)
		return
	}
	var output any
	var err error
	switch name {
	case "list_clusters":
		var input fleetSelection
		err = decodeFleetJSON(r, &input)
		if err == nil {
			_, output, err = s.tools.list(r.Context(), nil, input)
		}
	case "get_fleet_overview":
		var input fleetSelection
		err = decodeFleetJSON(r, &input)
		if err == nil {
			_, output, err = s.tools.overview(r.Context(), nil, input)
		}
	case "search_fleet_resources":
		var input fleetSearchInput
		err = decodeFleetJSON(r, &input)
		if err == nil {
			_, output, err = s.tools.search(r.Context(), nil, input)
		}
	case "get_fleet_resource":
		var input fleetEvidenceInput
		err = decodeFleetJSON(r, &input)
		if err == nil {
			_, output, err = s.tools.getFleetResource(r.Context(), nil, input)
		}
	case "get_fleet_events":
		var input fleetEventsInput
		err = decodeFleetJSON(r, &input)
		if err == nil {
			_, output, err = s.tools.getFleetEvents(r.Context(), nil, input)
		}
	case "get_fleet_logs":
		var input fleetLogsInput
		err = decodeFleetJSON(r, &input)
		if err == nil {
			_, output, err = s.tools.getFleetLogs(r.Context(), nil, input)
		}
	case "diagnose_fleet_resource":
		var input fleetEvidenceInput
		err = decodeFleetJSON(r, &input)
		if err == nil {
			_, output, err = s.tools.diagnoseFleetResource(r.Context(), nil, input)
		}
	case "get_fleet_relationships":
		var input fleetSelection
		err = decodeFleetJSON(r, &input)
		if err == nil {
			_, output, err = s.tools.getFleetRelationships(r.Context(), nil, input)
		}
	case "get_fleet_topology":
		var input fleetSelection
		err = decodeFleetJSON(r, &input)
		if err == nil {
			_, output, err = s.tools.getFleetTopology(r.Context(), nil, input)
		}
	default:
		err = errors.New("unsupported Fleet query tool")
	}
	if err != nil {
		writeFleetError(w, http.StatusBadRequest, err)
		return
	}
	writeFleetJSON(w, http.StatusOK, output)
}

func (s *fleetService) candidates(w http.ResponseWriter, r *http.Request) {
	if err := s.managementAuthorized(r); err != nil {
		writeFleetError(w, http.StatusForbidden, err)
		return
	}
	result := struct {
		Candidates []fleetCandidate `json:"candidates"`
		Note       string           `json:"note"`
	}{Candidates: []fleetCandidate{}, Note: "Candidates are trusted server-side kubeconfig references; credentials are never returned."}
	if s.options.Candidates != nil {
		candidates, err := s.options.Candidates()
		if err != nil {
			writeFleetError(w, http.StatusServiceUnavailable, errors.New("Fleet candidates are unavailable"))
			return
		}
		for _, candidate := range candidates {
			result.Candidates = append(result.Candidates, fleetCandidate{
				ID: candidate.ID, Name: candidate.Name, Context: candidate.Context,
				SourceLabel: candidate.SourceLabel,
			})
		}
	}
	writeFleetJSON(w, http.StatusOK, result)
}

func (s *fleetService) addConnection(w http.ResponseWriter, r *http.Request) {
	if !s.options.LocalConnections {
		writeFleetError(w, http.StatusConflict, errors.New("local Fleet connections are disabled"))
		return
	}
	if err := s.managementAuthorized(r); err != nil {
		writeFleetError(w, http.StatusForbidden, err)
		return
	}
	var request struct {
		ID          string `json:"id"`
		Name        string `json:"name"`
		CandidateID string `json:"candidateId"`
	}
	if err := decodeFleetJSON(r, &request); err != nil {
		writeFleetError(w, http.StatusBadRequest, err)
		return
	}
	clusterID, err := s.manager.AddManagedConnection(request.ID, request.Name, request.CandidateID)
	if err != nil {
		writeFleetError(w, http.StatusConflict, err)
		return
	}
	writeFleetJSON(w, http.StatusOK, map[string]any{"ok": true, "clusterId": clusterID})
}

func (s *fleetService) removeConnection(w http.ResponseWriter, r *http.Request, id string) {
	if !s.options.LocalConnections {
		writeFleetError(w, http.StatusConflict, errors.New("local Fleet connections are disabled"))
		return
	}
	if err := s.managementAuthorized(r); err != nil {
		writeFleetError(w, http.StatusForbidden, err)
		return
	}
	if err := s.manager.RemoveManagedConnection(id); err != nil {
		writeFleetError(w, http.StatusConflict, err)
		return
	}
	writeFleetJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *fleetService) upsertFleet(w http.ResponseWriter, r *http.Request, id string) {
	if err := s.managementAuthorized(r); err != nil {
		writeFleetError(w, http.StatusForbidden, err)
		return
	}
	for _, record := range s.manager.Catalog().Fleets {
		if record.ID == id && record.Declarative {
			writeFleetError(w, http.StatusConflict, errors.New("declarative Fleets are immutable; edit fleet configuration"))
			return
		}
	}
	var request struct {
		Name       string   `json:"name"`
		ClusterIDs []string `json:"clusterIds"`
	}
	if err := decodeFleetJSON(r, &request); err != nil {
		writeFleetError(w, http.StatusBadRequest, err)
		return
	}
	if err := s.manager.UpsertFleet(id, request.Name, request.ClusterIDs); err != nil {
		writeFleetError(w, http.StatusConflict, err)
		return
	}
	writeFleetJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *fleetService) deleteFleet(w http.ResponseWriter, r *http.Request, id string) {
	if err := s.managementAuthorized(r); err != nil {
		writeFleetError(w, http.StatusForbidden, err)
		return
	}
	for _, record := range s.manager.Catalog().Fleets {
		if record.ID == id && record.Declarative {
			writeFleetError(w, http.StatusConflict, errors.New("declarative Fleets are immutable; edit fleet configuration"))
			return
		}
	}
	if err := s.manager.DeleteFleet(id); err != nil {
		writeFleetError(w, http.StatusConflict, err)
		return
	}
	writeFleetJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *fleetService) refresh(w http.ResponseWriter, r *http.Request, clusterID string) {
	if err := s.managementAuthorized(r); err != nil {
		writeFleetError(w, http.StatusForbidden, err)
		return
	}
	if err := s.manager.Refresh(context.Background(), clusterID); err != nil {
		writeFleetError(w, http.StatusConflict, err)
		return
	}
	writeFleetJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *fleetService) duplicate(w http.ResponseWriter, r *http.Request) {
	if err := s.managementAuthorized(r); err != nil {
		writeFleetError(w, http.StatusForbidden, err)
		return
	}
	var request fleet.DuplicateDecision
	if err := decodeFleetJSON(r, &request); err != nil {
		writeFleetError(w, http.StatusBadRequest, err)
		return
	}
	if err := s.manager.SetDuplicateDecision(request.ClusterID, request.OtherClusterID, request.Decision); err != nil {
		writeFleetError(w, http.StatusConflict, err)
		return
	}
	writeFleetJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

type fleetCandidate struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Context     string `json:"context"`
	SourceLabel string `json:"sourceLabel"`
}

func decodeFleetJSON(r *http.Request, target any) error {
	data, err := io.ReadAll(io.LimitReader(r.Body, fleetAPIRequestLimit+1))
	if err != nil || len(data) > fleetAPIRequestLimit {
		return errors.New("invalid Fleet request body")
	}
	data = bytes.TrimSpace(data)
	if len(data) == 0 || data[0] != '{' {
		return errors.New("invalid Fleet request body")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return errors.New("invalid Fleet request body")
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return errors.New("invalid Fleet request body")
	}
	return nil
}

func writeFleetJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeFleetError(w http.ResponseWriter, status int, err error) {
	writeFleetJSON(w, status, map[string]string{"error": err.Error()})
}
