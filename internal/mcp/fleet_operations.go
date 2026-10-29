package mcp

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/skyhook-io/radar/internal/fleet"
	pkgauth "github.com/skyhook-io/radar/pkg/auth"
	"github.com/skyhook-io/radar/pkg/resourceid"
	"gopkg.in/yaml.v3"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/dynamic"
	sigsyaml "sigs.k8s.io/yaml"
)

const (
	fleetOperationTTL        = 5 * time.Minute
	fleetOperationMaxPending = 64
	fleetOperationPerActor   = 8
	fleetManifestLimit       = 256 << 10
)

type fleetOperationInput struct {
	ClusterID string `json:"clusterId"`
	Action    string `json:"action"`
	Resource  string `json:"resource"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	Replicas  *int   `json:"replicas,omitempty"`
	Manifest  string `json:"manifest,omitempty"`
}

type fleetExecuteOperationInput struct {
	ClusterID    string `json:"clusterId"`
	Confirmation string `json:"confirmation"`
}

type fleetOperationPreview struct {
	OperationID    string        `json:"operationId"`
	ClusterID      string        `json:"clusterId"`
	ConnectionID   string        `json:"connectionId"`
	Action         string        `json:"action"`
	Target         fleetResource `json:"target"`
	Confirmation   string        `json:"confirmation"`
	ExpiresAt      time.Time     `json:"expiresAt"`
	Summary        []string      `json:"summary"`
	DryRun         bool          `json:"dryRun"`
	ManifestDigest string        `json:"manifestDigest,omitempty"`
	Limitations    []string      `json:"limitations"`
}

type fleetOperationResult struct {
	OperationID string   `json:"operationId"`
	ClusterID   string   `json:"clusterId"`
	State       string   `json:"state"`
	Summary     string   `json:"summary"`
	Limitations []string `json:"limitations"`
}

type fleetAuditInput struct {
	ClusterID string `json:"clusterId,omitempty"`
	Limit     int    `json:"limit,omitempty"`
}

type fleetAuditResult struct {
	Records     []fleet.AuditRecord `json:"records"`
	Truncated   bool                `json:"truncated"`
	Limitations []string            `json:"limitations"`
}

type fleetOperationStore struct {
	mu      sync.Mutex
	pending map[string]fleetPendingOperation
}

type fleetPendingOperation struct {
	operationID  string
	clusterID    string
	connectionID string
	action       string
	kind         evidenceKind
	namespace    string
	name         string
	replicas     *int
	manifest     map[string]any
	applyPayload []byte
	patchPayload []byte
	manifestHash string
	uid          string
	resourceVer  string
	absent       bool
	runtime      *fleet.ClusterRuntime
	actor        string
	groups       []string
	expiresAt    time.Time
	target       fleetResource
	summary      []string
}

func newFleetOperationStore() *fleetOperationStore {
	return &fleetOperationStore{pending: make(map[string]fleetPendingOperation)}
}

func (s *fleetOperationStore) add(confirmation string, operation fleetPendingOperation) error {
	digest := confirmationDigest(confirmation)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanupExpiredLocked(time.Now())
	actorCount := 0
	for _, pending := range s.pending {
		if pending.actor == operation.actor {
			actorCount++
		}
	}
	if len(s.pending) >= fleetOperationMaxPending || actorCount >= fleetOperationPerActor {
		return errors.New("Fleet operation preview capacity is full")
	}
	s.pending[digest] = operation
	return nil
}

func (s *fleetOperationStore) consume(clusterID, confirmation, actor string, groups []string, now time.Time) (fleetPendingOperation, error) {
	if s == nil || confirmation == "" || len(confirmation) > 128 {
		return fleetPendingOperation{}, errors.New("Fleet operation confirmation is invalid or expired")
	}
	digest := confirmationDigest(confirmation)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanupExpiredLocked(now)
	pending, ok := s.pending[digest]
	if !ok || pending.clusterID != clusterID || pending.actor != actor || !equalStrings(pending.groups, groups) {
		return fleetPendingOperation{}, errors.New("Fleet operation confirmation is invalid or expired")
	}
	delete(s.pending, digest)
	return pending, nil
}

func (s *fleetOperationStore) cleanupExpiredLocked(now time.Time) {
	for key, pending := range s.pending {
		if !pending.expiresAt.After(now) {
			delete(s.pending, key)
		}
	}
}

func confirmationDigest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func (t fleetTools) prepareFleetOperation(ctx context.Context, _ *mcpsdk.CallToolRequest, input fleetOperationInput) (*mcpsdk.CallToolResult, fleetOperationPreview, error) {
	preview, err := t.prepareOperation(ctx, input)
	return nil, preview, err
}

func (t fleetTools) executeFleetOperation(ctx context.Context, _ *mcpsdk.CallToolRequest, input fleetExecuteOperationInput) (*mcpsdk.CallToolResult, fleetOperationResult, error) {
	result, err := t.executeOperation(ctx, input)
	return nil, result, err
}

func (t fleetTools) getFleetAudit(ctx context.Context, _ *mcpsdk.CallToolRequest, input fleetAuditInput) (*mcpsdk.CallToolResult, fleetAuditResult, error) {
	if err := t.authorize(ctx); err != nil {
		return nil, fleetAuditResult{}, err
	}
	if input.Limit < 0 || input.Limit > 100 {
		return nil, fleetAuditResult{}, errors.New("Fleet audit limit must be between 1 and 100")
	}
	limit := input.Limit
	if limit == 0 {
		limit = 100
	}
	if input.ClusterID != "" && (len(input.ClusterID) > 128 || len(validation.IsDNS1123Subdomain(input.ClusterID)) > 0) {
		return nil, fleetAuditResult{}, errors.New("invalid Fleet audit clusterId")
	}
	store, err := t.auditStore()
	if err != nil {
		return nil, fleetAuditResult{}, errors.New("Fleet audit is unavailable")
	}
	records, truncated, err := store.Read(100)
	if err != nil {
		return nil, fleetAuditResult{}, errors.New("Fleet audit history is unavailable or invalid")
	}
	user := pkgauth.UserFromContext(ctx)
	actor, _, _ := fleetActor(user)
	_, isAdmin := t.admins[userName(user)]
	isAdmin = isAdmin || !t.requireAuth
	filtered := make([]fleet.AuditRecord, 0, len(records))
	for _, record := range records {
		if input.ClusterID != "" && record.ClusterID != input.ClusterID {
			continue
		}
		if !isAdmin && record.Actor != actor {
			continue
		}
		filtered = append(filtered, record)
	}
	if len(filtered) > limit {
		filtered = filtered[len(filtered)-limit:]
		truncated = true
	}
	if filtered == nil {
		filtered = []fleet.AuditRecord{}
	}
	limitations := []string{}
	if truncated {
		limitations = append(limitations, "Only the latest bounded audit records are available; earlier records may be omitted.")
	}
	return nil, fleetAuditResult{Records: filtered, Truncated: truncated, Limitations: limitations}, nil
}

func (t fleetTools) prepareOperation(ctx context.Context, input fleetOperationInput) (fleetOperationPreview, error) {
	if !t.writesEnabled {
		return fleetOperationPreview{}, errors.New("Fleet writes are disabled")
	}
	if err := t.authorize(ctx); err != nil {
		return fleetOperationPreview{}, err
	}
	if t.operations == nil {
		return fleetOperationPreview{}, errors.New("Fleet operations are unavailable")
	}
	kind, replicas, manifest, manifestJSON, digest, err := validateFleetOperationInput(input)
	if err != nil {
		return fleetOperationPreview{}, err
	}
	actor, groups, err := fleetActor(pkgauth.UserFromContext(ctx))
	if err != nil {
		return fleetOperationPreview{}, err
	}
	operationID, err := randomHex(16)
	if err != nil {
		return fleetOperationPreview{}, errors.New("Fleet operation could not be prepared")
	}
	confirmation, err := randomHex(32)
	if err != nil {
		return fleetOperationPreview{}, errors.New("Fleet operation could not be prepared")
	}
	expiresAt := time.Now().UTC().Add(fleetOperationTTL)
	var preview fleetOperationPreview
	err = t.source.(*fleet.Manager).WithVerifiedRuntime(ctx, input.ClusterID, nil, func(pinCtx context.Context, runtime *fleet.ClusterRuntime) error {
		bundle, err := t.clientBundleForRuntime(pinCtx, runtime)
		if err != nil || bundle.Status.ConnectionID == "" || !fleetNamespaceAllowed(bundle.Namespaces, input.Namespace) {
			return errors.New("Fleet operation target is unavailable or outside configured scope")
		}
		target := fleetResource{
			ClusterID: input.ClusterID, ConnectionID: bundle.Status.ConnectionID,
			Ref:        resourceid.NewRef(kind.GVR.Group, kind.Kind, input.Namespace, input.Name),
			ObservedAt: time.Now().UTC(), Source: "kubernetes-api",
		}
		resource := bundle.Dynamic.Resource(kind.GVR).Namespace(input.Namespace)
		before, getErr := resource.Get(pinCtx, input.Name, metav1.GetOptions{})
		absent := apierrors.IsNotFound(getErr)
		if getErr != nil && !absent {
			return errors.New("Fleet operation target could not be read")
		}
		if !absent {
			if before.GetUID() == "" || before.GetResourceVersion() == "" {
				return errors.New("Fleet operation target identity is incomplete")
			}
			target.UID = string(before.GetUID())
			target.ResourceVersion = before.GetResourceVersion()
			target.APIVersion = kind.GVR.GroupVersion().String()
		}
		plan := fleetPendingOperation{
			operationID: operationID, clusterID: input.ClusterID, connectionID: bundle.Status.ConnectionID,
			action: input.Action, kind: kind, namespace: input.Namespace, name: input.Name,
			replicas: replicas, manifest: manifest, manifestHash: digest, uid: target.UID,
			resourceVer: target.ResourceVersion, absent: absent, runtime: runtime, actor: actor,
			groups: append([]string(nil), groups...), expiresAt: expiresAt, target: target,
		}
		switch input.Action {
		case "restart":
			plan.patchPayload, err = restartPatch(before, absent, time.Now().UTC())
			if err != nil {
				return errors.New("Fleet operation target does not support restart")
			}
			plan.summary = []string{fmt.Sprintf("Restart %s %s/%s", kind.Kind, input.Namespace, input.Name), "Pod template annotation: spec.template.metadata.annotations.kubectl.kubernetes.io/restartedAt"}
		case "scale":
			plan.patchPayload, err = scalePatch(before, absent, *replicas)
			if err != nil {
				return errors.New("Fleet operation target does not support scale")
			}
			plan.summary = []string{fmt.Sprintf("Scale %s %s/%s to %d replicas", kind.Kind, input.Namespace, input.Name, *replicas), "Scale subresource: spec.replicas"}
		case "apply":
			if absent {
				plan.applyPayload = append([]byte(nil), manifestJSON...)
			} else {
				plan.applyPayload, err = manifestWithPreconditions(manifest, target.UID, target.ResourceVersion)
				if err != nil {
					return errors.New("Fleet operation manifest is invalid")
				}
			}
			paths := safeManifestPaths(manifest)
			plan.summary = []string{fmt.Sprintf("Apply %s %s/%s", kind.Kind, input.Namespace, input.Name), "Manifest fields: " + strings.Join(paths, ", ")}
		}
		if err := dryRunFleetOperation(pinCtx, bundle.Dynamic, plan); err != nil {
			return errors.New("Kubernetes API denied or rejected the operation dry-run")
		}
		if err := t.operations.add(confirmation, plan); err != nil {
			return err
		}
		preview = fleetOperationPreview{
			OperationID: operationID, ClusterID: input.ClusterID, ConnectionID: bundle.Status.ConnectionID,
			Action: input.Action, Target: target, Confirmation: confirmation, ExpiresAt: expiresAt,
			Summary: append([]string{}, plan.summary...), DryRun: true,
			ManifestDigest: digest, Limitations: []string{"Dry-run is authorization and validation evidence, not a guarantee the later request will succeed."},
		}
		return nil
	})
	if err != nil {
		return fleetOperationPreview{}, err
	}
	return preview, nil
}

func (t fleetTools) executeOperation(ctx context.Context, input fleetExecuteOperationInput) (fleetOperationResult, error) {
	if !t.writesEnabled {
		return fleetOperationResult{}, errors.New("Fleet writes are disabled")
	}
	if err := t.authorize(ctx); err != nil {
		return fleetOperationResult{}, err
	}
	actor, groups, err := fleetActor(pkgauth.UserFromContext(ctx))
	if err != nil {
		return fleetOperationResult{}, err
	}
	plan, err := t.operations.consume(input.ClusterID, input.Confirmation, actor, groups, time.Now())
	if err != nil {
		return fleetOperationResult{}, err
	}
	result := fleetOperationResult{
		OperationID: plan.operationID, ClusterID: plan.clusterID,
		Limitations: []string{"Applied means the Kubernetes API acknowledged the mutation; it does not assert rollout convergence."},
	}
	var dispatched bool
	err = t.source.(*fleet.Manager).WithVerifiedRuntime(ctx, plan.clusterID, plan.runtime, func(pinCtx context.Context, runtime *fleet.ClusterRuntime) error {
		bundle, err := t.clientBundleForRuntime(pinCtx, runtime)
		if err != nil || bundle.Status.ConnectionID != plan.connectionID || !fleetNamespaceAllowed(bundle.Namespaces, plan.namespace) {
			return errors.New("Fleet operation preview is stale or unavailable")
		}
		resource := bundle.Dynamic.Resource(plan.kind.GVR).Namespace(plan.namespace)
		live, getErr := resource.Get(pinCtx, plan.name, metav1.GetOptions{})
		if plan.absent {
			if !apierrors.IsNotFound(getErr) {
				return errors.New("Fleet operation preview is stale or unavailable")
			}
		} else if getErr != nil || string(live.GetUID()) != plan.uid || live.GetResourceVersion() != plan.resourceVer {
			return errors.New("Fleet operation preview is stale or unavailable")
		}
		audit, err := t.auditStore()
		if err != nil {
			return errors.New("Fleet audit is unavailable; no operation was attempted")
		}
		if err := audit.Append(operationAuditRecord(plan, "intent", "")); err != nil {
			return errors.New("Fleet audit is unavailable; no operation was attempted")
		}
		dispatched = true
		mutationErr := performFleetMutation(pinCtx, bundle.Dynamic, plan)
		outcome := "applied"
		result.State = "applied"
		result.Summary = "Kubernetes API acknowledged the mutation."
		if mutationErr != nil {
			outcome = "outcome_unknown"
			result.State = outcome
			result.Summary = "Mutation outcome could not be confirmed."
			if isExplicitMutationRejection(mutationErr) {
				outcome = "rejected"
				result.State = outcome
				result.Summary = "Kubernetes API rejected the mutation."
			}
		}
		if auditErr := audit.Append(operationAuditRecord(plan, "result", outcome)); auditErr != nil {
			result.State = "outcome_unknown"
			result.Summary = "Mutation may have occurred; its audit result could not be persisted."
			result.Limitations = append(result.Limitations, "Durable outcome audit failed after mutation dispatch.")
		}
		return nil
	})
	if err != nil {
		if dispatched {
			return fleetOperationResult{
				OperationID: plan.operationID, ClusterID: plan.clusterID, State: "outcome_unknown",
				Summary:     "Mutation outcome could not be confirmed.",
				Limitations: []string{"The operation was dispatched, but its outcome could not be safely confirmed."},
			}, nil
		}
		return fleetOperationResult{}, err
	}
	return result, nil
}

func validateFleetOperationInput(input fleetOperationInput) (evidenceKind, *int, map[string]any, []byte, string, error) {
	kind, ok := evidenceKindFor(input.Resource)
	if !ok || !operationResourceAllowed(kind) || input.ClusterID == "" || len(input.ClusterID) > 128 ||
		len(validation.IsDNS1123Subdomain(input.ClusterID)) > 0 || len(validation.IsDNS1123Label(input.Namespace)) > 0 ||
		len(validation.IsDNS1123Subdomain(input.Name)) > 0 {
		return evidenceKind{}, nil, nil, nil, "", errors.New("Fleet operation target is invalid or unsupported")
	}
	switch input.Action {
	case "restart":
		if kind.Kind != "Deployment" && kind.Kind != "StatefulSet" && kind.Kind != "DaemonSet" {
			return evidenceKind{}, nil, nil, nil, "", errors.New("restart supports Deployments, StatefulSets and DaemonSets")
		}
		if input.Replicas != nil || input.Manifest != "" {
			return evidenceKind{}, nil, nil, nil, "", errors.New("restart input contains unsupported fields")
		}
		return kind, nil, nil, nil, "", nil
	case "scale":
		if kind.Kind != "Deployment" && kind.Kind != "StatefulSet" {
			return evidenceKind{}, nil, nil, nil, "", errors.New("scale supports Deployments and StatefulSets")
		}
		if input.Replicas == nil || *input.Replicas < 0 || *input.Replicas > 10000 || input.Manifest != "" {
			return evidenceKind{}, nil, nil, nil, "", errors.New("replicas must be an integer from 0 to 10000")
		}
		replicas := *input.Replicas
		return kind, &replicas, nil, nil, "", nil
	case "apply":
		if input.Replicas != nil || input.Manifest == "" || len(input.Manifest) > fleetManifestLimit {
			return evidenceKind{}, nil, nil, nil, "", errors.New("apply requires one manifest string of at most 256 KiB")
		}
		object, manifestJSON, err := parseFleetManifest(input.Manifest, kind, input.Namespace, input.Name)
		if err != nil {
			return evidenceKind{}, nil, nil, nil, "", errors.New("Fleet manifest is invalid or unsupported")
		}
		digest := sha256.Sum256(manifestJSON)
		return kind, nil, object, manifestJSON, hex.EncodeToString(digest[:]), nil
	default:
		return evidenceKind{}, nil, nil, nil, "", errors.New("unsupported Fleet operation")
	}
}

func operationResourceAllowed(kind evidenceKind) bool {
	switch kind.Kind {
	case "Pod", "Service", "ReplicaSet", "Deployment", "StatefulSet", "DaemonSet":
		return true
	default:
		return false
	}
}

func parseFleetManifest(source string, kind evidenceKind, namespace, name string) (map[string]any, []byte, error) {
	if len(source) == 0 || len(source) > fleetManifestLimit {
		return nil, nil, errors.New("invalid manifest size")
	}
	decoder := yaml.NewDecoder(strings.NewReader(source))
	var document yaml.Node
	if err := decoder.Decode(&document); err != nil || document.Kind != yaml.DocumentNode || len(document.Content) != 1 {
		return nil, nil, errors.New("invalid YAML document")
	}
	if err := validateYAMLNode(document.Content[0]); err != nil {
		return nil, nil, err
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, nil, errors.New("manifest must contain exactly one document")
	}
	encoded, err := sigsyaml.YAMLToJSON([]byte(source))
	if err != nil || len(encoded) > fleetManifestLimit {
		return nil, nil, errors.New("invalid manifest object")
	}
	var object map[string]any
	if err := json.Unmarshal(encoded, &object); err != nil || object == nil {
		return nil, nil, errors.New("manifest must be one object")
	}
	manifestKind, _ := object["kind"].(string)
	apiVersion, _ := object["apiVersion"].(string)
	if manifestKind != kind.Kind || apiVersion != kind.GVR.GroupVersion().String() ||
		strings.HasSuffix(manifestKind, "List") || object["status"] != nil || object["managedFields"] != nil {
		return nil, nil, errors.New("unsupported manifest kind or fields")
	}
	metadata, ok := object["metadata"].(map[string]any)
	if !ok {
		return nil, nil, errors.New("manifest metadata is required")
	}
	if metadata["generateName"] != nil || metadata["managedFields"] != nil {
		return nil, nil, errors.New("generated names and managedFields are unsupported")
	}
	manifestName, _ := metadata["name"].(string)
	manifestNamespace, _ := metadata["namespace"].(string)
	if manifestName != name || manifestNamespace != namespace {
		return nil, nil, errors.New("manifest target does not match explicit target")
	}
	canonical, err := json.Marshal(object)
	if err != nil {
		return nil, nil, err
	}
	return object, canonical, nil
}

func validateYAMLNode(node *yaml.Node) error {
	if node == nil || node.Kind == yaml.AliasNode {
		return errors.New("YAML aliases are unsupported")
	}
	if node.Kind == yaml.MappingNode {
		seen := make(map[string]struct{}, len(node.Content)/2)
		for i := 0; i+1 < len(node.Content); i += 2 {
			key := node.Content[i]
			if key.Kind != yaml.ScalarNode || key.Tag != "!!str" {
				return errors.New("manifest map keys must be strings")
			}
			if _, exists := seen[key.Value]; exists {
				return errors.New("duplicate YAML key")
			}
			seen[key.Value] = struct{}{}
		}
	}
	for _, child := range node.Content {
		if err := validateYAMLNode(child); err != nil {
			return err
		}
	}
	return nil
}

func manifestWithPreconditions(manifest map[string]any, uid, resourceVersion string) ([]byte, error) {
	copyObject := runtime.DeepCopyJSONValue(manifest).(map[string]any)
	metadata, ok := copyObject["metadata"].(map[string]any)
	if !ok {
		return nil, errors.New("manifest metadata is unavailable")
	}
	metadata["uid"] = uid
	metadata["resourceVersion"] = resourceVersion
	return json.Marshal(copyObject)
}

func restartPatch(before *unstructured.Unstructured, absent bool, timestamp time.Time) ([]byte, error) {
	if absent || before == nil {
		return nil, errors.New("workload is absent")
	}
	operations := []map[string]any{
		{"op": "test", "path": "/metadata/uid", "value": string(before.GetUID())},
		{"op": "test", "path": "/metadata/resourceVersion", "value": before.GetResourceVersion()},
	}
	restartedAt := timestamp.UTC().Format(time.RFC3339Nano)
	templateMetadata, found, _ := unstructured.NestedMap(before.Object, "spec", "template", "metadata")
	if !found {
		operations = append(operations, map[string]any{"op": "add", "path": "/spec/template/metadata", "value": map[string]any{"annotations": map[string]any{"kubectl.kubernetes.io/restartedAt": restartedAt}}})
	} else if _, found := templateMetadata["annotations"]; !found {
		operations = append(operations, map[string]any{"op": "add", "path": "/spec/template/metadata/annotations", "value": map[string]any{"kubectl.kubernetes.io/restartedAt": restartedAt}})
	} else {
		operations = append(operations, map[string]any{"op": "add", "path": "/spec/template/metadata/annotations/kubectl.kubernetes.io~1restartedAt", "value": restartedAt})
	}
	return json.Marshal(operations)
}

func scalePatch(before *unstructured.Unstructured, absent bool, replicas int) ([]byte, error) {
	if absent || before == nil || before.GetUID() == "" || before.GetResourceVersion() == "" {
		return nil, errors.New("workload identity is unavailable")
	}
	operations := []map[string]any{
		{"op": "test", "path": "/metadata/uid", "value": string(before.GetUID())},
		{"op": "test", "path": "/metadata/resourceVersion", "value": before.GetResourceVersion()},
		{"op": "replace", "path": "/spec/replicas", "value": replicas},
	}
	return json.Marshal(operations)
}

func dryRunFleetOperation(ctx context.Context, client dynamic.Interface, plan fleetPendingOperation) error {
	resource := client.Resource(plan.kind.GVR).Namespace(plan.namespace)
	if plan.action == "apply" {
		if plan.absent {
			object := &unstructured.Unstructured{Object: plan.manifest}
			_, err := resource.Create(ctx, object, metav1.CreateOptions{DryRun: []string{metav1.DryRunAll}, FieldManager: "radar-fleet"})
			return err
		}
		force := false
		_, err := resource.Patch(ctx, plan.name, types.ApplyPatchType, plan.applyPayload, metav1.PatchOptions{DryRun: []string{metav1.DryRunAll}, FieldManager: "radar-fleet", Force: &force})
		return err
	}
	patch := plan.patchPayload
	subresources := []string(nil)
	if plan.action == "scale" {
		subresources = []string{"scale"}
	}
	_, err := resource.Patch(ctx, plan.name, types.JSONPatchType, patch, metav1.PatchOptions{DryRun: []string{metav1.DryRunAll}}, subresources...)
	return err
}

func performFleetMutation(ctx context.Context, client dynamic.Interface, plan fleetPendingOperation) error {
	resource := client.Resource(plan.kind.GVR).Namespace(plan.namespace)
	if plan.action == "apply" {
		if plan.absent {
			_, err := resource.Create(ctx, &unstructured.Unstructured{Object: plan.manifest}, metav1.CreateOptions{FieldManager: "radar-fleet"})
			return err
		}
		force := false
		_, err := resource.Patch(ctx, plan.name, types.ApplyPatchType, plan.applyPayload, metav1.PatchOptions{FieldManager: "radar-fleet", Force: &force})
		return err
	}
	subresources := []string(nil)
	if plan.action == "scale" {
		subresources = []string{"scale"}
	}
	_, err := resource.Patch(ctx, plan.name, types.JSONPatchType, plan.patchPayload, metav1.PatchOptions{}, subresources...)
	return err
}

func operationAuditRecord(plan fleetPendingOperation, stage, outcome string) fleet.AuditRecord {
	return fleet.AuditRecord{
		OperationID: plan.operationID, ClusterID: plan.clusterID, ConnectionID: plan.connectionID,
		Action: plan.action, Resource: plan.kind.Kind, Namespace: plan.namespace, Name: plan.name,
		Stage: stage, Outcome: outcome, ObservedAt: time.Now().UTC(), Actor: plan.actor,
	}
}

func (t fleetTools) auditStore() (*fleet.AuditStore, error) {
	provider, ok := t.source.(interface {
		AuditStore() (*fleet.AuditStore, error)
	})
	if !ok {
		return nil, errors.New("Fleet audit is unavailable")
	}
	return provider.AuditStore()
}

func isExplicitMutationRejection(err error) bool {
	var status apierrors.APIStatus
	if !errors.As(err, &status) {
		return false
	}
	switch status.Status().Code {
	case 400, 403, 404, 409, 422:
		return true
	default:
		return false
	}
}

func fleetActor(user *pkgauth.User) (string, []string, error) {
	if user == nil {
		return "local:loopback", []string{}, nil
	}
	if user.Username == "" {
		return "", nil, errors.New("Fleet caller identity is unavailable")
	}
	groups := append([]string(nil), user.Groups...)
	sort.Strings(groups)
	groups = compactStrings(groups)
	return "user:" + user.Username, groups, nil
}

func userName(user *pkgauth.User) string {
	if user == nil {
		return ""
	}
	return user.Username
}

func compactStrings(values []string) []string {
	out := values[:0]
	for _, value := range values {
		if len(out) == 0 || out[len(out)-1] != value {
			out = append(out, value)
		}
	}
	return out
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func randomHex(size int) (string, error) {
	buffer := make([]byte, size)
	if _, err := io.ReadFull(rand.Reader, buffer); err != nil {
		return "", err
	}
	return hex.EncodeToString(buffer), nil
}

func safeManifestPaths(manifest map[string]any) []string {
	paths := make([]string, 0, 8)
	for _, key := range []string{"metadata", "spec"} {
		if _, ok := manifest[key]; ok {
			paths = append(paths, key)
		}
	}
	if metadata, ok := manifest["metadata"].(map[string]any); ok {
		for _, key := range []string{"labels", "annotations"} {
			if _, ok := metadata[key]; ok {
				paths = append(paths, "metadata."+key)
			}
		}
	}
	sort.Strings(paths)
	if len(paths) == 0 {
		return []string{"object"}
	}
	return paths
}
