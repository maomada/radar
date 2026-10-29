package mcp

import (
	"context"
	"errors"
	"io"
	"strings"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/skyhook-io/radar/internal/fleet"
	aicontext "github.com/skyhook-io/radar/pkg/ai/context"
	"github.com/skyhook-io/radar/pkg/resourceid"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation"
)

type fleetEvidenceInput struct {
	ClusterID string `json:"clusterId"`
	Resource  string `json:"resource"`
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name"`
	UID       string `json:"uid,omitempty"`
}

type fleetEvidenceResult struct {
	Target      fleetResource `json:"target"`
	State       string        `json:"state"`
	Object      any           `json:"object,omitempty"`
	Limitations []string      `json:"limitations"`
}

type fleetEventsInput struct {
	fleetEvidenceInput
	Limit int `json:"limit,omitempty"`
}

type fleetEvent struct {
	UID            string    `json:"uid"`
	Type           string    `json:"type"`
	Reason         string    `json:"reason"`
	Message        string    `json:"message"`
	Count          int32     `json:"count"`
	LastObservedAt time.Time `json:"lastObservedAt"`
}

type fleetEventsResult struct {
	Target      fleetResource `json:"target"`
	State       string        `json:"state"`
	Events      []fleetEvent  `json:"events"`
	Truncated   bool          `json:"truncated"`
	Limitations []string      `json:"limitations"`
}

type fleetLogsInput struct {
	ClusterID string `json:"clusterId"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	Container string `json:"container"`
	TailLines int    `json:"tailLines,omitempty"`
	Previous  bool   `json:"previous,omitempty"`
}

type fleetLogsResult struct {
	Target      fleetResource `json:"target"`
	State       string        `json:"state"`
	Container   string        `json:"container"`
	Text        string        `json:"text"`
	Truncated   bool          `json:"truncated"`
	Limitations []string      `json:"limitations"`
}

type evidenceKind struct {
	GVR        schema.GroupVersionResource
	Kind       string
	Namespaced bool
}

var evidenceKinds = map[string]evidenceKind{
	"pods":         {schema.GroupVersionResource{Version: "v1", Resource: "pods"}, "Pod", true},
	"deployments":  {schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}, "Deployment", true},
	"statefulsets": {schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "statefulsets"}, "StatefulSet", true},
	"daemonsets":   {schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "daemonsets"}, "DaemonSet", true},
	"replicasets":  {schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "replicasets"}, "ReplicaSet", true},
	"services":     {schema.GroupVersionResource{Version: "v1", Resource: "services"}, "Service", true},
	"nodes":        {schema.GroupVersionResource{Version: "v1", Resource: "nodes"}, "Node", false},
	"namespaces":   {schema.GroupVersionResource{Version: "v1", Resource: "namespaces"}, "Namespace", false},
}

func evidenceKindFor(name string) (evidenceKind, bool) {
	kind, ok := evidenceKinds[strings.ToLower(strings.TrimSpace(name))]
	return kind, ok
}

func (t fleetTools) evidenceTarget(ctx context.Context, input fleetEvidenceInput) (fleet.ConnectionStatus, evidenceKind, *unstructured.Unstructured, fleetResource, string, []string) {
	kind, ok := evidenceKindFor(input.Resource)
	target := fleetResource{ClusterID: input.ClusterID, Ref: resourceid.NewRef("", input.Resource, input.Namespace, input.Name), ObservedAt: time.Now().UTC(), Source: "kubernetes-api"}
	if !ok {
		return fleet.ConnectionStatus{}, kind, nil, target, "unsupported", []string{"resource kind is not supported"}
	}
	target.Ref = resourceid.NewRef(kind.GVR.Group, kind.Kind, input.Namespace, input.Name)
	if input.ClusterID == "" || len(input.ClusterID) > 128 || input.Name == "" || len(input.Name) > 253 || len(validation.IsDNS1123Subdomain(input.Name)) > 0 || len(input.UID) > 256 || (kind.Namespaced && input.Namespace == "") || (!kind.Namespaced && input.Namespace != "") {
		return fleet.ConnectionStatus{}, kind, nil, target, "invalid", []string{"clusterId, resource, namespace and name must match the selected resource scope"}
	}
	if kind.Namespaced && len(validation.IsDNS1123Label(input.Namespace)) > 0 {
		return fleet.ConnectionStatus{}, kind, nil, target, "invalid", []string{"namespace is invalid"}
	}
	statuses := t.source.Statuses()
	var selected fleet.ConnectionStatus
	for _, status := range statuses {
		if status.ClusterID == input.ClusterID {
			selected = status
			break
		}
	}
	if selected.ClusterID == "" {
		return selected, kind, nil, target, "unavailable", []string{"selected cluster is unavailable"}
	}
	return selected, kind, nil, target, "", nil
}

func fleetNamespaceAllowed(configured []string, namespace string) bool {
	if namespace == "" || len(configured) == 0 {
		return true
	}
	for _, allowed := range configured {
		if allowed == namespace {
			return true
		}
	}
	return false
}

func classifyFleetReadError(err error) string {
	switch {
	case apierrors.IsForbidden(err):
		return "denied"
	case apierrors.IsUnauthorized(err):
		return "unauthenticated"
	case apierrors.IsNotFound(err):
		return "not_found"
	default:
		return "unavailable"
	}
}

func fleetTargetFromObject(status fleet.ConnectionStatus, kind evidenceKind, obj *unstructured.Unstructured) fleetResource {
	return fleetResource{ClusterID: status.ClusterID, ConnectionID: status.ConnectionID,
		Ref: resourceid.NewRef(kind.GVR.Group, kind.Kind, obj.GetNamespace(), obj.GetName()),
		UID: string(obj.GetUID()), APIVersion: kind.GVR.GroupVersion().String(),
		ResourceVersion: obj.GetResourceVersion(), ObservedAt: time.Now().UTC(), Source: "kubernetes-api"}
}

func sanitizeFleetObject(obj *unstructured.Unstructured) any {
	copy := obj.DeepCopy()
	meta, _ := copy.Object["metadata"].(map[string]any)
	delete(meta, "managedFields")
	annotations, _ := meta["annotations"].(map[string]any)
	delete(annotations, "kubectl.kubernetes.io/last-applied-configuration")
	aicontext.RedactInlineSecrets(copy.Object)
	return aicontext.MinifyUnstructured(copy, aicontext.LevelDetail)
}

func (t fleetTools) readFleetObject(ctx context.Context, input fleetEvidenceInput) (fleetResource, *unstructured.Unstructured, evidenceKind, string, []string) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	status, kind, _, target, state, limitations := t.evidenceTarget(ctx, input)
	if state != "" {
		return target, nil, kind, state, limitations
	}
	_, client, namespaces, err := t.clients(ctx, status.ClusterID)
	if err != nil {
		return target, nil, kind, "unavailable", []string{"target caller client is unavailable"}
	}
	if kind.Namespaced && !fleetNamespaceAllowed(namespaces, input.Namespace) {
		return target, nil, kind, "out_of_scope", []string{"namespace is outside the runtime collection clamp"}
	}
	obj, err := client.Resource(kind.GVR).Namespace(input.Namespace).Get(ctx, input.Name, metav1.GetOptions{})
	if err != nil {
		return target, nil, kind, classifyFleetReadError(err), []string{"target resource could not be read"}
	}
	target = fleetTargetFromObject(status, kind, obj)
	if input.UID != "" && input.UID != string(obj.GetUID()) {
		return target, nil, kind, "stale", []string{"the supplied UID does not match the current resource"}
	}
	return target, obj, kind, "available", []string{}
}

func (t fleetTools) getFleetResource(ctx context.Context, _ *mcpsdk.CallToolRequest, input fleetEvidenceInput) (*mcpsdk.CallToolResult, fleetEvidenceResult, error) {
	if err := t.authorize(ctx); err != nil {
		return nil, fleetEvidenceResult{}, err
	}
	target, obj, _, state, limitations := t.readFleetObject(ctx, input)
	result := fleetEvidenceResult{Target: target, State: state, Limitations: limitations}
	if state == "available" {
		result.Object = sanitizeFleetObject(obj)
	}
	return nil, result, nil
}

func (t fleetTools) getFleetEvents(ctx context.Context, _ *mcpsdk.CallToolRequest, input fleetEventsInput) (*mcpsdk.CallToolResult, fleetEventsResult, error) {
	if err := t.authorize(ctx); err != nil {
		return nil, fleetEventsResult{}, err
	}
	if input.UID == "" {
		return nil, fleetEventsResult{}, errors.New("uid is required")
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	clamped := input.Limit > 100
	if input.Limit <= 0 || input.Limit > 100 {
		input.Limit = 100
	}
	target, obj, kind, state, limitations := t.readFleetObject(ctx, input.fleetEvidenceInput)
	result := fleetEventsResult{Target: target, State: state, Events: []fleetEvent{}, Truncated: clamped, Limitations: limitations}
	if state != "available" {
		return nil, result, nil
	}
	typed, _, namespaces, err := t.clients(ctx, input.ClusterID)
	if err != nil {
		result.State, result.Limitations = "unavailable", []string{"target caller client is unavailable"}
		return nil, result, nil
	}
	eventNamespaces := namespaces
	if kind.Namespaced {
		eventNamespaces = []string{obj.GetNamespace()}
	}
	if len(eventNamespaces) == 0 {
		eventNamespaces = []string{""}
	}
	for _, namespace := range eventNamespaces {
		if len(result.Events) >= input.Limit {
			result.Truncated = true
			break
		}
		list, err := typed.CoreV1().Events(namespace).List(ctx, metav1.ListOptions{
			FieldSelector: fields.OneTermEqualSelector("involvedObject.uid", string(obj.GetUID())).String(),
			Limit:         int64(input.Limit - len(result.Events)),
		})
		if err != nil {
			result.State = classifyFleetReadError(err)
			result.Limitations = append(result.Limitations, "event coverage is incomplete")
			continue
		}
		if list.Continue != "" {
			result.Truncated = true
		}
		for _, event := range list.Items {
			if event.InvolvedObject.UID != obj.GetUID() || (kind.Namespaced && event.Namespace != obj.GetNamespace()) {
				continue
			}
			if len(result.Events) >= input.Limit {
				result.Truncated = true
				break
			}
			message := aicontext.RedactSecrets(event.Message)
			count := event.Count
			last := time.Time{}
			if event.Series != nil {
				if event.Series.Count > 0 {
					count = event.Series.Count
				}
				last = event.Series.LastObservedTime.Time
			}
			if last.IsZero() {
				last = event.LastTimestamp.Time
			}
			if last.IsZero() {
				last = event.EventTime.Time
			}
			if last.IsZero() {
				last = event.CreationTimestamp.Time
			}
			result.Events = append(result.Events, fleetEvent{UID: string(event.UID), Type: event.Type, Reason: event.Reason, Message: message, Count: count, LastObservedAt: last})
		}
	}
	return nil, result, nil
}

func (t fleetTools) getFleetLogs(ctx context.Context, _ *mcpsdk.CallToolRequest, input fleetLogsInput) (*mcpsdk.CallToolResult, fleetLogsResult, error) {
	if err := t.authorize(ctx); err != nil {
		return nil, fleetLogsResult{}, err
	}
	if input.Container == "" {
		return nil, fleetLogsResult{}, errors.New("container is required")
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	target, podObj, kind, state, limitations := t.readFleetObject(ctx, fleetEvidenceInput{ClusterID: input.ClusterID, Resource: "pods", Namespace: input.Namespace, Name: input.Name})
	result := fleetLogsResult{Target: target, State: state, Container: input.Container, Text: "", Limitations: limitations}
	if kind.Kind != "Pod" || state != "available" {
		return nil, result, nil
	}
	typed, _, namespaces, err := t.clients(ctx, input.ClusterID)
	if err != nil {
		result.State, result.Limitations = "unavailable", []string{"target caller client is unavailable"}
		return nil, result, nil
	}
	if !fleetNamespaceAllowed(namespaces, input.Namespace) {
		result.State, result.Limitations = "out_of_scope", []string{"namespace is outside the runtime collection clamp"}
		return nil, result, nil
	}
	pod := &corev1.Pod{}
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(podObj.Object, pod); err != nil || !fleetContainerExists(pod, input.Container) {
		result.State, result.Limitations = "invalid", []string{"named container is not present in the Pod"}
		return nil, result, nil
	}
	if pod.UID == "" {
		result.State, result.Limitations = "unknown", []string{"Pod UID is unavailable"}
		return nil, result, nil
	}
	tail := int64(input.TailLines)
	if tail <= 0 || tail > 200 {
		if tail > 200 {
			result.Truncated = true
		}
		tail = 200
	}
	stream, err := typed.CoreV1().Pods(input.Namespace).GetLogs(input.Name, &corev1.PodLogOptions{Container: input.Container, TailLines: &tail, Previous: input.Previous, Timestamps: true}).Stream(ctx)
	if err != nil {
		result.State, result.Limitations = classifyFleetReadError(err), []string{"pod logs could not be read"}
		return nil, result, nil
	}
	defer stream.Close()
	data, err := io.ReadAll(io.LimitReader(stream, 64*1024+1))
	if err != nil {
		result.State, result.Limitations = "unavailable", []string{"pod log stream was interrupted"}
		return nil, result, nil
	}
	if len(data) > 64*1024 {
		result.Truncated = true
		data = data[:64*1024]
	}
	lines := strings.Split(string(data), "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) > 200 {
		result.Truncated = true
		data = []byte(strings.Join(lines[:200], "\n"))
	}
	result.Text = aicontext.RedactSecrets(string(data))
	after, err := typed.CoreV1().Pods(input.Namespace).Get(ctx, input.Name, metav1.GetOptions{})
	if err != nil || after.UID != pod.UID {
		result.State, result.Text = "stale", ""
		result.Limitations = []string{"Pod UID changed while logs were being read"}
	}
	return nil, result, nil
}

func fleetContainerExists(pod *corev1.Pod, name string) bool {
	for _, container := range pod.Spec.Containers {
		if container.Name == name {
			return true
		}
	}
	for _, container := range pod.Spec.InitContainers {
		if container.Name == name {
			return true
		}
	}
	for _, container := range pod.Spec.EphemeralContainers {
		if container.Name == name {
			return true
		}
	}
	return false
}
