package mcp

import (
	"context"
	"sort"
	"strings"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/skyhook-io/radar/pkg/health"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
)

type fleetFinding struct {
	Code     string   `json:"code"`
	Severity string   `json:"severity"`
	Summary  string   `json:"summary"`
	Evidence []string `json:"evidence"`
}

func diagnosisFinding(code, severity, summary string, evidence ...string) fleetFinding {
	return fleetFinding{Code: code, Severity: severity, Summary: summary, Evidence: evidence}
}

func (t fleetTools) diagnoseFleetResource(ctx context.Context, _ *mcpsdk.CallToolRequest, input fleetEvidenceInput) (*mcpsdk.CallToolResult, fleetDiagnosisResult, error) {
	if err := t.authorize(ctx); err != nil {
		return nil, fleetDiagnosisResult{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	target, object, kind, state, limitations := t.readFleetObject(ctx, input)
	result := fleetDiagnosisResult{Target: target, State: state, Findings: []fleetFinding{}, Limitations: limitations}
	if state != "available" {
		return nil, result, nil
	}
	verdict, complete, finding := classifyFleetObject(object, kind.Kind, time.Now().UTC())
	if !complete {
		result.State = "unknown"
		result.Limitations = append(result.Limitations, "required observed fields were not present")
	} else {
		result.State = "no_observed_issue"
		if finding != nil {
			result.Findings = append(result.Findings, *finding)
		}
		if verdict == health.LevelUnknown {
			result.State = "unknown"
		} else if verdict == health.LevelUnhealthy || verdict == health.LevelDegraded {
			result.State = "observed_issues"
		}
	}
	eventsInput := fleetEventsInput{fleetEvidenceInput: fleetEvidenceInput{ClusterID: input.ClusterID, Resource: input.Resource, Namespace: input.Namespace, Name: input.Name, UID: target.UID}, Limit: 20}
	_, events, _ := t.getFleetEvents(ctx, nil, eventsInput)
	if events.State != "available" {
		result.Limitations = append(result.Limitations, "event evidence was unavailable or incomplete")
	} else {
		for _, event := range events.Events {
			if event.Type != corev1.EventTypeWarning {
				continue
			}
			if len(result.Findings) >= 10 {
				result.Limitations = append(result.Limitations, "finding list truncated at 10")
				break
			}
			result.Findings = append(result.Findings, diagnosisFinding("event_"+strings.ToLower(event.Type), "info", event.Reason, event.UID))
		}
	}
	return nil, result, nil
}

type fleetDiagnosisResult struct {
	Target      fleetResource  `json:"target"`
	State       string         `json:"state"`
	Findings    []fleetFinding `json:"findings"`
	Limitations []string       `json:"limitations"`
}

func classifyFleetObject(object *unstructured.Unstructured, kind string, now time.Time) (health.Level, bool, *fleetFinding) {
	if object == nil {
		return health.LevelUnknown, false, nil
	}
	switch kind {
	case "Pod":
		if _, ok, _ := unstructured.NestedString(object.Object, "status", "phase"); !ok {
			return health.LevelUnknown, false, nil
		}
		var pod corev1.Pod
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(object.Object, &pod); err != nil {
			return health.LevelUnknown, false, nil
		}
		verdict := health.Pod(&pod, now)
		for _, status := range append(append([]corev1.ContainerStatus{}, pod.Status.ContainerStatuses...), pod.Status.InitContainerStatuses...) {
			if waiting := status.State.Waiting; waiting != nil {
				switch waiting.Reason {
				case "CrashLoopBackOff", "ImagePullBackOff", "ErrImagePull":
					code := map[string]string{"CrashLoopBackOff": "crash_loop_back_off", "ImagePullBackOff": "image_pull_back_off", "ErrImagePull": "err_image_pull"}[waiting.Reason]
					finding := diagnosisFinding(code, severityForLevel(verdict.Level), waiting.Reason, "container="+status.Name, string(object.GetUID()))
					return verdict.Level, true, &finding
				}
			}
		}
		for _, condition := range pod.Status.Conditions {
			if condition.Type == corev1.PodReady && condition.Status != corev1.ConditionTrue {
				finding := diagnosisFinding("pod_not_ready", "warning", "Pod Ready condition is "+string(condition.Status), string(object.GetUID()))
				return verdict.Level, true, &finding
			}
		}
		for _, status := range append(append([]corev1.ContainerStatus{}, pod.Status.ContainerStatuses...), pod.Status.InitContainerStatuses...) {
			if status.LastTerminationState.Terminated != nil && status.LastTerminationState.Terminated.Reason == "OOMKilled" && (verdict.Level == health.LevelHealthy || verdict.Level == health.LevelNeutral) {
				finding := diagnosisFinding("historical_oom_killed", "info", "A previous container instance was OOMKilled; this is historical evidence, not a current root-cause finding.", string(object.GetUID()))
				return verdict.Level, true, &finding
			}
		}
		if verdict.Level == health.LevelHealthy || verdict.Level == health.LevelNeutral {
			return verdict.Level, true, nil
		}
		return verdict.Level, true, fleetFindingFromVerdict(verdict, string(object.GetUID()))
	case "Deployment", "StatefulSet", "DaemonSet", "ReplicaSet":
		generation, okGeneration, _ := unstructured.NestedInt64(object.Object, "metadata", "generation")
		observed, okObserved, _ := unstructured.NestedInt64(object.Object, "status", "observedGeneration")
		if !okGeneration || !okObserved {
			return health.LevelUnknown, false, nil
		}
		if kind == "Deployment" && (observed == 0 || observed < generation) {
			return health.LevelUnknown, false, nil
		}
		var typed any
		switch kind {
		case "Deployment":
			typed = &appsv1.Deployment{}
		case "StatefulSet":
			typed = &appsv1.StatefulSet{}
		case "DaemonSet":
			typed = &appsv1.DaemonSet{}
		default:
			typed = &appsv1.ReplicaSet{}
		}
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(object.Object, typed); err != nil {
			return health.LevelUnknown, false, nil
		}
		verdict := health.Workload(typed, now)
		if verdict.Level == health.LevelHealthy || verdict.Level == health.LevelNeutral {
			return verdict.Level, true, nil
		}
		return verdict.Level, true, fleetFindingFromVerdict(verdict, string(object.GetUID()))
	case "Node":
		var node corev1.Node
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(object.Object, &node); err != nil {
			return health.LevelUnknown, false, nil
		}
		hasReady := false
		for _, condition := range node.Status.Conditions {
			if condition.Type == corev1.NodeReady {
				hasReady = true
				break
			}
		}
		if !hasReady {
			return health.LevelUnknown, false, nil
		}
		lifecycle := health.NodeLifecycle(&node, now)
		if lifecycle.Level == health.LevelHealthy || lifecycle.Level == health.LevelNeutral {
			return lifecycle.Level, true, nil
		}
		finding := diagnosisFinding("node_"+strings.ToLower(lifecycle.Label), severityForLevel(lifecycle.Level), lifecycle.Label, string(object.GetUID()))
		return lifecycle.Level, true, &finding
	default:
		return health.LevelUnknown, false, nil
	}
}

func fleetFindingFromVerdict(verdict health.Verdict, uid string) *fleetFinding {
	code := strings.ToLower(verdict.Reason)
	if code == "" {
		code = "observed_issue"
	}
	return ptrFinding(diagnosisFinding(code, severityForLevel(verdict.Level), verdict.Message, uid))
}

func ptrFinding(f fleetFinding) *fleetFinding { return &f }

func severityForLevel(level health.Level) string {
	switch level {
	case health.LevelUnhealthy:
		return "critical"
	case health.LevelDegraded:
		return "warning"
	default:
		return "info"
	}
}

func assessFleetObjects(objects map[string][]unstructured.Unstructured, coverages []fleetCoverage, now time.Time) (string, []fleetFinding, []fleetCoverage, bool) {
	findings := []fleetFinding{}
	assessment := "no_observed_issue"
	complete := true
	observed := 0
	truncated := false
	for _, coverage := range coverages {
		if coverage.State != "complete" {
			complete = false
		}
	}
	for _, kind := range []string{"Pod", "Deployment", "Node"} {
		items := objects[kind]
		sort.Slice(items, func(i, j int) bool {
			if items[i].GetName() == items[j].GetName() {
				return items[i].GetUID() < items[j].GetUID()
			}
			return items[i].GetName() < items[j].GetName()
		})
		for _, item := range items {
			observed++
			level, fieldsPresent, finding := classifyFleetObject(&item, kind, now)
			if !fieldsPresent || level == health.LevelUnknown {
				complete = false
				continue
			}
			if finding != nil {
				if len(findings) < 10 {
					findings = append(findings, *finding)
				} else {
					truncated = true
				}
			}
			if level == health.LevelUnhealthy || level == health.LevelDegraded {
				assessment = "observed_issues"
			}
		}
	}
	if (!complete || observed == 0) && assessment == "no_observed_issue" {
		assessment = "unknown"
	}
	return assessment, findings, coverages, truncated
}
