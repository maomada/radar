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
	"k8s.io/client-go/kubernetes"
)

type fleetSource interface {
	Catalog() fleet.Catalog
	Statuses() []fleet.ConnectionStatus
	Runtime(string) (*fleet.ClusterRuntime, error)
}

type fleetTools struct {
	source        fleetSource
	requireAuth   bool
	writesEnabled bool
	admins        map[string]struct{}
	operations    *fleetOperationStore
}

type fleetClientBundle struct {
	Status     fleet.ConnectionStatus
	Runtime    *fleet.ClusterRuntime
	Typed      kubernetes.Interface
	Dynamic    dynamic.Interface
	Namespaces []string
}

func (t fleetTools) clientBundle(ctx context.Context, clusterID string) (fleetClientBundle, error) {
	runtime, err := t.source.Runtime(clusterID)
	if err != nil {
		return fleetClientBundle{}, errors.New("cluster is not connected or identity is conflicting")
	}
	return t.clientBundleForRuntime(ctx, runtime)
}

func (t fleetTools) clientBundleForRuntime(ctx context.Context, runtime *fleet.ClusterRuntime) (fleetClientBundle, error) {
	if runtime == nil {
		return fleetClientBundle{}, errors.New("target client unavailable")
	}
	bundle := fleetClientBundle{Status: runtime.Status(), Runtime: runtime, Namespaces: runtime.Namespaces()}
	if user := pkgauth.UserFromContext(ctx); user != nil {
		config := runtime.RESTConfig()
		if user.Username == "" || config == nil {
			return fleetClientBundle{}, errors.New("caller credentials unavailable for target cluster")
		}
		typed, err := pkgauth.ImpersonatedClient(config, user.Username, user.Groups)
		if err != nil {
			return fleetClientBundle{}, errors.New("caller client unavailable for target cluster")
		}
		dynamicClient, err := pkgauth.ImpersonatedDynamicClient(config, user.Username, user.Groups)
		if err != nil {
			return fleetClientBundle{}, errors.New("caller client unavailable for target cluster")
		}
		bundle.Typed, bundle.Dynamic = typed, dynamicClient
		return bundle, nil
	}
	if runtime.Kubernetes() == nil || runtime.Dynamic() == nil {
		return fleetClientBundle{}, errors.New("target client unavailable")
	}
	bundle.Typed, bundle.Dynamic = runtime.Kubernetes(), runtime.Dynamic()
	return bundle, nil
}

func (t fleetTools) clients(ctx context.Context, clusterID string) (kubernetes.Interface, dynamic.Interface, []string, error) {
	bundle, err := t.clientBundle(ctx, clusterID)
	return bundle.Typed, bundle.Dynamic, bundle.Namespaces, err
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
	ClusterID           string          `json:"clusterId"`
	ConnectionID        string          `json:"connectionId"`
	ObservedAt          time.Time       `json:"observedAt"`
	Source              string          `json:"source"`
	Health              string          `json:"health"`
	Resources           []fleetCoverage `json:"resources"`
	AssessmentState     string          `json:"assessmentState"`
	Findings            []fleetFinding  `json:"findings"`
	AssessmentCoverage  []fleetCoverage `json:"assessmentCoverage"`
	AssessmentTruncated bool            `json:"assessmentTruncated"`
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
	instructions := "Bounded multi-cluster Radar. The default catalog contains these 10 read-only tools: list_clusters, get_fleet_overview, search_fleet_resources, get_fleet_resource, get_fleet_events, get_fleet_logs, diagnose_fleet_resource, get_fleet_relationships, get_fleet_topology, and get_fleet_audit. With Fleet writes enabled, the catalog additionally contains prepare_fleet_operation and execute_fleet_operation (12 tools total). Cluster IDs are explicit stable targets, never the UI's current Context. Connection readiness is not cluster health. Counts cover only the caller's accessible configured scope; partial counts are lower bounds. Unavailable or denied is not zero. Duplicate candidates may represent the same cluster: do not sum them as distinct physical clusters. Evidence and diagnosis are scoped observations, not whole-cluster health or ownership proof. Fleet operations require an explicit target, a reviewed dry-run preview, and one-shot confirmation; API RBAC remains authoritative."
	if tools.writesEnabled {
		instructions += " prepare_fleet_operation and execute_fleet_operation are enabled for one-cluster restart, scale, and allowlisted apply operations."
	} else {
		instructions += " Fleet writes are disabled."
	}
	server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "radar-fleet", Version: version.Current}, &mcpsdk.ServerOptions{
		Instructions: instructions,
		SetCacheable: privateCacheScope,
	})
	closedWorld := false
	annotations := &mcpsdk.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closedWorld}
	mcpsdk.AddTool(server, &mcpsdk.Tool{Name: "list_clusters", Description: "List configured Fleet clusters, stable IDs, identity evidence status and collection connection status. Inventory is operator-configured metadata, not a grant of Kubernetes access.", Annotations: annotations}, tools.list)
	mcpsdk.AddTool(server, &mcpsdk.Tool{Name: "get_fleet_overview", Description: "Bounded live resource counts by cluster, with explicit coverage. Does not assert cluster health. Reads run as the caller on each target cluster.", Annotations: annotations}, tools.overview)
	mcpsdk.AddTool(server, &mcpsdk.Tool{Name: "search_fleet_resources", Description: "Search supported resource metadata across explicitly selected clusters or a Fleet. Returns provenance and per-cluster collection coverage, never raw YAML or Secret data.", Annotations: annotations}, tools.search)
	mcpsdk.AddTool(server, &mcpsdk.Tool{Name: "get_fleet_resource", Description: "Read one allowlisted Fleet resource from an explicit cluster with sanitized structured detail and provenance. Secret access is denied.", Annotations: annotations}, tools.getFleetResource)
	mcpsdk.AddTool(server, &mcpsdk.Tool{Name: "get_fleet_events", Description: "Read bounded UID-bound events for one Fleet resource. Events are caller-scoped and partial or denied coverage is explicit.", Annotations: annotations}, tools.getFleetEvents)
	mcpsdk.AddTool(server, &mcpsdk.Tool{Name: "get_fleet_logs", Description: "Read bounded, best-effort-redacted logs for an explicitly named Pod container with UID-stale protection.", Annotations: annotations}, tools.getFleetLogs)
	mcpsdk.AddTool(server, &mcpsdk.Tool{Name: "diagnose_fleet_resource", Description: "Classify scoped observations for one allowlisted Fleet resource using Radar's canonical health policy; it does not assert a root cause or whole-cluster health.", Annotations: annotations}, tools.diagnoseFleetResource)
	mcpsdk.AddTool(server, &mcpsdk.Tool{Name: "get_fleet_relationships", Description: "Return conservative, evidence-backed Karmada identity and distribution relationships with explicit coverage and unresolved ambiguity.", Annotations: annotations}, tools.getFleetRelationships)
	mcpsdk.AddTool(server, &mcpsdk.Tool{Name: "get_fleet_topology", Description: "Return typed, bounded Fleet nodes and evidence-backed edges. Every edge references returned nodes.", Annotations: annotations}, tools.getFleetTopology)
	mcpsdk.AddTool(server, &mcpsdk.Tool{Name: "get_fleet_audit", Description: "Read the latest bounded, durable Fleet operation audit records. Non-admin callers see only their own records.", Annotations: annotations}, tools.getFleetAudit)
	if tools.writesEnabled {
		prepareAnnotations := &mcpsdk.ToolAnnotations{ReadOnlyHint: false, DestructiveHint: new(bool), OpenWorldHint: &closedWorld}
		executeAnnotations := &mcpsdk.ToolAnnotations{ReadOnlyHint: false, DestructiveHint: boolValue(true), OpenWorldHint: &closedWorld}
		mcpsdk.AddTool(server, &mcpsdk.Tool{Name: "prepare_fleet_operation", Description: "Prepare one explicit Fleet restart, scale or allowlisted apply using caller-authorized server-side dry-run. Review the returned target and summary before execute_fleet_operation.", Annotations: prepareAnnotations}, tools.prepareFleetOperation)
		mcpsdk.AddTool(server, &mcpsdk.Tool{Name: "execute_fleet_operation", Description: "Consume a one-shot confirmation for exactly one reviewed Fleet operation. This does not assert rollout convergence; never automatically retry.", Annotations: executeAnnotations}, tools.executeFleetOperation)
	}
	return server
}

func boolValue(value bool) *bool {
	return &value
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
	_, client, namespaces, err := t.clients(ctx, clusterID)
	return client, namespaces, err
}

func (t fleetTools) overview(ctx context.Context, _ *mcpsdk.CallToolRequest, input fleetSelection) (*mcpsdk.CallToolResult, fleetOverview, error) {
	selected, err := t.selectClusters(ctx, input)
	if err != nil {
		return nil, fleetOverview{}, err
	}
	result := fleetOverview{Clusters: make([]fleetOverviewRow, len(selected)), Note: "Counts and assessments are bounded to the caller-visible configured scope. Partial, denied and unavailable coverage is never treated as zero or whole-cluster health."}
	fleetParallel(ctx, len(selected), func(ctx context.Context, i int) {
		status := selected[i]
		row := fleetOverviewRow{ClusterID: status.ClusterID, ConnectionID: status.ConnectionID, ObservedAt: time.Now().UTC(), Source: "kubernetes-api", Health: "not_assessed", AssessmentState: "unknown", Findings: []fleetFinding{}, AssessmentCoverage: []fleetCoverage{}, Resources: make([]fleetCoverage, 0, len(fleetKinds))}
		client, namespaces, err := t.client(ctx, status.ClusterID)
		objects := map[string][]unstructured.Unstructured{}
		for _, kind := range fleetKinds {
			coverage := fleetCoverage{Resource: kind.gvr.Resource, State: "not_connected", CountScope: "configured-and-caller-visible"}
			if err == nil {
				items, collected := fleetCollect(ctx, client, namespaces, kind, "", "")
				coverage = collected
				switch kind.gvr.Resource {
				case "pods":
					objects["Pod"] = items
				case "deployments":
					objects["Deployment"] = items
				case "nodes":
					objects["Node"] = items
				}
			}
			row.Resources = append(row.Resources, coverage)
			if kind.gvr.Resource == "pods" || kind.gvr.Resource == "deployments" || kind.gvr.Resource == "nodes" {
				row.AssessmentCoverage = append(row.AssessmentCoverage, coverage)
			}
		}
		row.AssessmentState, row.Findings, row.AssessmentCoverage, row.AssessmentTruncated = assessFleetObjects(objects, row.AssessmentCoverage, time.Now().UTC())
		row.Health = row.AssessmentState
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
