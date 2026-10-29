package mcp

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/skyhook-io/radar/internal/fleet"
	"github.com/skyhook-io/radar/internal/version"
	pkgauth "github.com/skyhook-io/radar/pkg/auth"
	"github.com/skyhook-io/radar/pkg/resourceid"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/dynamic"
)

type fleetSource interface {
	Catalog() fleet.Catalog
	Statuses() []fleet.ConnectionStatus
	Runtime(string) (*fleet.ClusterRuntime, error)
}

type fleetTools struct {
	source      fleetSource
	requireAuth bool
}

type fleetSelection struct {
	FleetID    string   `json:"fleetId,omitempty" jsonschema:"Optional Fleet ID; omitted means all configured active clusters unless clusterIds is supplied"`
	ClusterIDs []string `json:"clusterIds,omitempty" jsonschema:"Explicit stable cluster IDs; when fleetId is supplied each must belong to that Fleet"`
}

type fleetSearchInput struct {
	FleetID       string   `json:"fleetId,omitempty"`
	ClusterIDs    []string `json:"clusterIds,omitempty"`
	Resource      string   `json:"resource" jsonschema:"One of pods, deployments, services, nodes, namespaces"`
	Namespace     string   `json:"namespace,omitempty"`
	NameContains  string   `json:"nameContains,omitempty"`
	LabelSelector string   `json:"labelSelector,omitempty"`
	Limit         int      `json:"limit,omitempty" jsonschema:"Maximum total returned matches, default 100, maximum 200. Collection is bounded and coverage reports omitted results"`
}

type fleetInventory struct {
	Clusters             []fleet.ConnectionStatus  `json:"clusters"`
	Fleets               []fleet.FleetRecord       `json:"fleets"`
	Members              []fleet.MembershipRecord  `json:"memberships"`
	ManagedConnectionIDs []string                  `json:"managedConnectionIds"`
	DuplicateDecisions   []fleet.DuplicateDecision `json:"duplicateDecisions"`
	Note                 string                    `json:"note"`
}

type fleetCoverage struct {
	Resource   string                   `json:"resource"`
	State      string                   `json:"state"`
	Count      *int                     `json:"count,omitempty"`
	CountScope string                   `json:"countScope"`
	Scope      string                   `json:"scope,omitempty"`
	Namespaces []fleetNamespaceCoverage `json:"namespaces,omitempty"`
}

type fleetNamespaceCoverage struct {
	Namespace string `json:"namespace"`
	State     string `json:"state"`
}

type fleetOverviewRow struct {
	ClusterID    string          `json:"clusterId"`
	ConnectionID string          `json:"connectionId"`
	ObservedAt   time.Time       `json:"observedAt"`
	Source       string          `json:"source"`
	Health       string          `json:"health"`
	Resources    []fleetCoverage `json:"resources"`
}

type fleetOverview struct {
	Clusters []fleetOverviewRow `json:"clusters"`
	Note     string             `json:"note"`
}

type fleetResource struct {
	ClusterID       string         `json:"clusterId"`
	ConnectionID    string         `json:"connectionId"`
	Ref             resourceid.Ref `json:"ref"`
	UID             string         `json:"uid"`
	APIVersion      string         `json:"apiVersion"`
	ResourceVersion string         `json:"resourceVersion"`
	ObservedAt      time.Time      `json:"observedAt"`
	Source          string         `json:"source"`
}

type fleetSearchCoverage struct {
	ClusterID    string        `json:"clusterId"`
	ConnectionID string        `json:"connectionId"`
	ObservedAt   time.Time     `json:"observedAt"`
	Source       string        `json:"source"`
	Collection   fleetCoverage `json:"collection"`
}

type fleetSearchResult struct {
	Resources []fleetResource       `json:"resources"`
	Coverage  []fleetSearchCoverage `json:"coverage"`
	Truncated bool                  `json:"truncated"`
}

type fleetResourceKind struct {
	gvr        schema.GroupVersionResource
	kind       string
	namespaced bool
}

var fleetKinds = []fleetResourceKind{
	{schema.GroupVersionResource{Version: "v1", Resource: "pods"}, "Pod", true},
	{schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}, "Deployment", true},
	{schema.GroupVersionResource{Version: "v1", Resource: "services"}, "Service", true},
	{schema.GroupVersionResource{Version: "v1", Resource: "nodes"}, "Node", false},
	{schema.GroupVersionResource{Version: "v1", Resource: "namespaces"}, "Namespace", false},
}

func NewFleetHandler(manager *fleet.Manager, requireAuth bool) http.Handler {
	if manager == nil {
		return http.NotFoundHandler()
	}
	mcpHandler, _ := NewFleetEndpoints(manager, FleetOptions{RequireAuth: requireAuth})
	return mcpHandler
}

func newFleetServer(tools fleetTools) *mcpsdk.Server {
	server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "radar-fleet", Version: version.Current}, &mcpsdk.ServerOptions{
		Instructions: "Read-only multi-cluster Radar. Cluster IDs are explicit stable targets, never the UI's current Context. Connection readiness is not cluster health. Counts cover only the caller's accessible configured scope; partial counts are lower bounds. Unavailable or denied is not zero. Duplicate candidates may represent the same cluster: do not sum them as distinct physical clusters. This increment does not infer cluster relationships or diagnose health.",
		SetCacheable: privateCacheScope,
	})
	closedWorld := false
	annotations := &mcpsdk.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closedWorld}
	mcpsdk.AddTool(server, &mcpsdk.Tool{Name: "list_clusters", Description: "List configured Fleet clusters, stable IDs, identity evidence status and collection connection status. Inventory is operator-configured metadata, not a grant of Kubernetes access.", Annotations: annotations}, tools.list)
	mcpsdk.AddTool(server, &mcpsdk.Tool{Name: "get_fleet_overview", Description: "Bounded live resource counts by cluster, with explicit coverage. Does not assert cluster health. Reads run as the caller on each target cluster.", Annotations: annotations}, tools.overview)
	mcpsdk.AddTool(server, &mcpsdk.Tool{Name: "search_fleet_resources", Description: "Search supported resource metadata across explicitly selected clusters or a Fleet. Returns provenance and per-cluster collection coverage, never raw YAML or Secret data.", Annotations: annotations}, tools.search)
	return server
}

func (t fleetTools) authorize(ctx context.Context) error {
	user := pkgauth.UserFromContext(ctx)
	if t.requireAuth && (user == nil || user.Username == "") {
		return errors.New("Fleet requires an authenticated caller")
	}
	if pkgauth.UserTokenMode() {
		return errors.New("Fleet does not forward user tokens between clusters")
	}
	return nil
}

func (t fleetTools) selectClusters(ctx context.Context, input fleetSelection) ([]fleet.ConnectionStatus, error) {
	if err := t.authorize(ctx); err != nil {
		return nil, err
	}
	if len(input.ClusterIDs) > 64 {
		return nil, errors.New("at most 64 clusterIds are supported")
	}
	statuses := t.source.Statuses()
	allowed := make(map[string]bool, len(statuses))
	if input.FleetID != "" {
		catalog := t.source.Catalog()
		if !slices.ContainsFunc(catalog.Fleets, func(f fleet.FleetRecord) bool { return f.Active && f.ID == input.FleetID }) {
			return nil, errors.New("unknown or inactive Fleet")
		}
		for _, member := range catalog.Memberships {
			if member.FleetID == input.FleetID {
				allowed[member.ClusterID] = true
			}
		}
	} else {
		for _, status := range statuses {
			allowed[status.ClusterID] = true
		}
	}
	active := make(map[string]bool, len(statuses))
	for _, status := range statuses {
		active[status.ClusterID] = true
	}
	for _, id := range input.ClusterIDs {
		if !active[id] || !allowed[id] {
			return nil, errors.New("unknown, inactive or out-of-Fleet clusterId")
		}
	}
	selected := make([]fleet.ConnectionStatus, 0, len(statuses))
	for _, status := range statuses {
		if allowed[status.ClusterID] && (len(input.ClusterIDs) == 0 || slices.Contains(input.ClusterIDs, status.ClusterID)) {
			selected = append(selected, status)
		}
	}
	return selected, nil
}

func (t fleetTools) list(ctx context.Context, _ *mcpsdk.CallToolRequest, input fleetSelection) (*mcpsdk.CallToolResult, fleetInventory, error) {
	statuses, err := t.selectClusters(ctx, input)
	if err != nil {
		return nil, fleetInventory{}, err
	}
	catalog := t.source.Catalog()
	result := fleetInventory{
		Clusters: statuses, Fleets: []fleet.FleetRecord{}, Members: []fleet.MembershipRecord{},
		ManagedConnectionIDs: append([]string{}, managedConnectionIDs(t.source.Catalog())...),
		DuplicateDecisions:   append([]fleet.DuplicateDecision{}, catalog.DuplicateDecisions...),
		Note:                 "Configured inventory; connection and identity observations are not current Kubernetes health or per-user access grants.",
	}
	for _, f := range catalog.Fleets {
		if f.Active && (input.FleetID == "" || f.ID == input.FleetID) {
			result.Fleets = append(result.Fleets, f)
		}
	}
	for _, member := range catalog.Memberships {
		if slices.ContainsFunc(result.Fleets, func(f fleet.FleetRecord) bool { return f.ID == member.FleetID }) && slices.ContainsFunc(statuses, func(s fleet.ConnectionStatus) bool { return s.ClusterID == member.ClusterID }) {
			result.Members = append(result.Members, member)
		}
	}
	return nil, result, nil
}

func managedConnectionIDs(catalog fleet.Catalog) []string {
	ids := make([]string, 0)
	for _, connection := range catalog.Connections {
		if connection.Managed && connection.Active {
			ids = append(ids, connection.ID)
		}
	}
	slices.Sort(ids)
	return ids
}

func (t fleetTools) client(ctx context.Context, clusterID string) (dynamic.Interface, []string, error) {
	runtime, err := t.source.Runtime(clusterID)
	if err != nil {
		return nil, nil, errors.New("cluster is not connected or identity is conflicting")
	}
	if user := pkgauth.UserFromContext(ctx); user != nil {
		if user.Username == "" || runtime.RESTConfig() == nil {
			return nil, nil, errors.New("caller credentials unavailable for target cluster")
		}
		client, err := pkgauth.ImpersonatedDynamicClient(runtime.RESTConfig(), user.Username, user.Groups)
		if err != nil {
			return nil, nil, errors.New("caller client unavailable for target cluster")
		}
		return client, runtime.Namespaces(), nil
	}
	if runtime.Dynamic() == nil {
		return nil, nil, errors.New("target client unavailable")
	}
	return runtime.Dynamic(), runtime.Namespaces(), nil
}

func (t fleetTools) overview(ctx context.Context, _ *mcpsdk.CallToolRequest, input fleetSelection) (*mcpsdk.CallToolResult, fleetOverview, error) {
	selected, err := t.selectClusters(ctx, input)
	if err != nil {
		return nil, fleetOverview{}, err
	}
	result := fleetOverview{Clusters: make([]fleetOverviewRow, len(selected)), Note: "Counts are bounded per resource and configured namespace scope, not fleet-wide totals. Partial counts are lower bounds; denied/unavailable counts are omitted. Health and cross-cluster relationships are not assessed."}
	fleetParallel(ctx, len(selected), func(ctx context.Context, i int) {
		status := selected[i]
		row := fleetOverviewRow{ClusterID: status.ClusterID, ConnectionID: status.ConnectionID, ObservedAt: time.Now().UTC(), Source: "kubernetes-api", Health: "not_assessed", Resources: make([]fleetCoverage, 0, len(fleetKinds))}
		client, namespaces, err := t.client(ctx, status.ClusterID)
		for _, kind := range fleetKinds {
			coverage := fleetCoverage{Resource: kind.gvr.Resource, State: "not_connected", CountScope: "configured-and-caller-visible"}
			if err == nil {
				_, coverage = fleetCollect(ctx, client, namespaces, kind, "", "")
			}
			row.Resources = append(row.Resources, coverage)
		}
		row.ObservedAt = time.Now().UTC()
		result.Clusters[i] = row
	})
	return nil, result, nil
}

func (t fleetTools) search(ctx context.Context, _ *mcpsdk.CallToolRequest, input fleetSearchInput) (*mcpsdk.CallToolResult, fleetSearchResult, error) {
	var kind fleetResourceKind
	for _, candidate := range fleetKinds {
		if candidate.gvr.Resource == input.Resource {
			kind = candidate
		}
	}
	if kind.kind == "" {
		return nil, fleetSearchResult{}, errors.New("unsupported resource; use pods, deployments, services, nodes or namespaces")
	}
	if input.Namespace != "" && (!kind.namespaced || len(validation.IsDNS1123Label(input.Namespace)) > 0) {
		return nil, fleetSearchResult{}, errors.New("invalid namespace for this resource")
	}
	if len(input.LabelSelector) > 2048 || len(input.NameContains) > 253 || input.Limit < 0 || input.Limit > 200 {
		return nil, fleetSearchResult{}, errors.New("search exceeds selector, name or result limit")
	}
	if _, err := labels.Parse(input.LabelSelector); err != nil {
		return nil, fleetSearchResult{}, errors.New("invalid labelSelector")
	}
	if input.Limit == 0 {
		input.Limit = 100
	}
	selected, err := t.selectClusters(ctx, fleetSelection{FleetID: input.FleetID, ClusterIDs: input.ClusterIDs})
	if err != nil {
		return nil, fleetSearchResult{}, err
	}
	rows := make([][]fleetResource, len(selected))
	result := fleetSearchResult{Resources: []fleetResource{}, Coverage: make([]fleetSearchCoverage, len(selected))}
	fleetParallel(ctx, len(selected), func(ctx context.Context, i int) {
		status := selected[i]
		coverage := fleetCoverage{Resource: kind.gvr.Resource, State: "not_connected", CountScope: "configured-and-caller-visible-before-name-filter"}
		client, namespaces, err := t.client(ctx, status.ClusterID)
		observedAt := time.Now().UTC()
		if err == nil {
			items, collected := fleetCollect(ctx, client, namespaces, kind, input.Namespace, input.LabelSelector)
			coverage = collected
			coverage.CountScope = "configured-and-caller-visible-before-name-filter"
			observedAt = time.Now().UTC()
			for _, item := range items {
				if !strings.Contains(item.GetName(), input.NameContains) {
					continue
				}
				rows[i] = append(rows[i], fleetResource{ClusterID: status.ClusterID, ConnectionID: status.ConnectionID,
					Ref: resourceid.NewRef(kind.gvr.Group, kind.kind, item.GetNamespace(), item.GetName()), UID: string(item.GetUID()),
					APIVersion: kind.gvr.GroupVersion().String(), ResourceVersion: item.GetResourceVersion(), ObservedAt: observedAt, Source: "kubernetes-api"})
			}
		}
		result.Coverage[i] = fleetSearchCoverage{ClusterID: status.ClusterID, ConnectionID: status.ConnectionID, ObservedAt: observedAt, Source: "kubernetes-api", Collection: coverage}
	})
	for _, matches := range rows {
		for _, match := range matches {
			if len(result.Resources) == input.Limit {
				result.Truncated = true
				break
			}
			result.Resources = append(result.Resources, match)
		}
	}
	return nil, result, nil
}

func fleetParallel(ctx context.Context, count int, run func(context.Context, int)) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	jobs := make(chan int, count)
	for i := 0; i < count; i++ {
		jobs <- i
	}
	close(jobs)
	var workers sync.WaitGroup
	for worker := 0; worker < min(count, 4); worker++ {
		workers.Go(func() {
			for i := range jobs {
				run(ctx, i)
			}
		})
	}
	workers.Wait()
}

func fleetCollect(ctx context.Context, client dynamic.Interface, configured []string, kind fleetResourceKind, namespace, selector string) ([]unstructured.Unstructured, fleetCoverage) {
	coverage := fleetCoverage{Resource: kind.gvr.Resource, State: "unavailable", CountScope: "configured-and-caller-visible", Scope: "cluster"}
	namespaces := []string{""}
	if kind.namespaced {
		if namespace != "" {
			coverage.Scope = "namespaces"
			if len(configured) > 0 && !slices.Contains(configured, namespace) {
				coverage.State = "out_of_scope"
				coverage.Namespaces = []fleetNamespaceCoverage{{Namespace: namespace, State: "out_of_scope"}}
				return nil, coverage
			}
			namespaces = []string{namespace}
		} else if len(configured) > 0 {
			namespaces = slices.Clone(configured)
			coverage.Scope = "namespaces"
		}
	}
	const collectionLimit = 500
	items := make([]unstructured.Unstructured, 0)
	succeeded, denied, failed := 0, 0, 0
	partial := false
	for i, ns := range namespaces {
		if len(items) >= collectionLimit {
			partial = true
			for _, skipped := range namespaces[i:] {
				coverage.Namespaces = append(coverage.Namespaces, fleetNamespaceCoverage{Namespace: skipped, State: "not_collected"})
			}
			break
		}
		page, err := client.Resource(kind.gvr).Namespace(ns).List(ctx, metav1.ListOptions{Limit: int64(collectionLimit - len(items)), LabelSelector: selector})
		if err != nil {
			state := "unavailable"
			if apierrors.IsForbidden(err) {
				denied++
				state = "denied"
			} else {
				failed++
				if apierrors.IsUnauthorized(err) {
					state = "unauthenticated"
				}
			}
			if ns != "" {
				coverage.Namespaces = append(coverage.Namespaces, fleetNamespaceCoverage{Namespace: ns, State: state})
			}
			continue
		}
		succeeded++
		remaining := collectionLimit - len(items)
		state := "complete"
		if page.GetContinue() != "" || len(page.Items) > remaining {
			partial = true
			state = "partial"
		}
		if ns != "" {
			coverage.Namespaces = append(coverage.Namespaces, fleetNamespaceCoverage{Namespace: ns, State: state})
		}
		items = append(items, page.Items[:min(len(page.Items), remaining)]...)
	}
	if succeeded > 0 {
		count := len(items)
		coverage.Count = &count
		coverage.State = "complete"
		if partial || failed > 0 || denied > 0 {
			coverage.State = "partial"
		}
	} else if denied > 0 && failed == 0 {
		coverage.State = "denied"
	}
	return items, coverage
}
