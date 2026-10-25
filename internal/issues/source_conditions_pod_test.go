package issues

import (
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/issuesapi"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
)

func TestCompletedPodConditionsThroughCacheProvider(t *testing.T) {
	defer k8s.ResetTestState()
	defer k8s.ResetTestDynamicState()
	controller := true
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: "successful-run", Namespace: "batch", UID: "pod-uid",
			CreationTimestamp: metav1.NewTime(time.Now().Add(-time.Minute)),
			OwnerReferences:   []metav1.OwnerReference{{APIVersion: "batch/v1", Kind: "Job", Name: "successful", UID: "job-uid", Controller: &controller}},
		},
		Spec: corev1.PodSpec{RestartPolicy: corev1.RestartPolicyNever},
		Status: corev1.PodStatus{
			Phase:             corev1.PodSucceeded,
			Conditions:        []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionFalse, Reason: "PodCompleted", LastTransitionTime: metav1.Now()}},
			ContainerStatuses: []corev1.ContainerStatus{{Name: "run", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 0, Reason: "Completed"}}}},
		},
	}
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: "successful", Namespace: "batch", UID: "job-uid", CreationTimestamp: pod.CreationTimestamp, OwnerReferences: []metav1.OwnerReference{{APIVersion: "batch/v1", Kind: "CronJob", Name: "schedule", UID: "cronjob-uid", Controller: &controller}}},
		Status:     batchv1.JobStatus{Succeeded: 1, Conditions: []batchv1.JobCondition{{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}}},
	}
	cronjob := &batchv1.CronJob{
		ObjectMeta: metav1.ObjectMeta{Name: "schedule", Namespace: "batch", UID: "cronjob-uid", CreationTimestamp: job.CreationTimestamp},
		Spec:       batchv1.CronJobSpec{Schedule: "*/5 * * * *"},
		Status:     batchv1.CronJobStatus{LastScheduleTime: &pod.CreationTimestamp, LastSuccessfulTime: &pod.Status.Conditions[0].LastTransitionTime},
	}
	failedPod := pod.DeepCopy()
	failedPod.Name, failedPod.UID = "failed-run", "failed-pod-uid"
	failedPod.OwnerReferences = nil
	failedPod.Status.Phase = corev1.PodFailed
	failedPod.Status.Conditions[0].Reason = "PodFailed"
	failedPod.Status.ContainerStatuses[0].State.Terminated.ExitCode = 1
	failedPod.Status.ContainerStatuses[0].State.Terminated.Reason = "Error"
	failedJob := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: "failed", Namespace: "batch", UID: "failed-job-uid", CreationTimestamp: pod.CreationTimestamp},
		Status:     batchv1.JobStatus{Failed: 1, Conditions: []batchv1.JobCondition{{Type: batchv1.JobFailed, Status: corev1.ConditionTrue, Reason: "BackoffLimitExceeded"}}},
	}
	gateBlocked := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "readiness-gate-blocked", Namespace: "batch", CreationTimestamp: pod.CreationTimestamp},
		Spec:       corev1.PodSpec{ReadinessGates: []corev1.PodReadinessGate{{ConditionType: "example.com/ready"}}},
		Status: corev1.PodStatus{Phase: corev1.PodRunning, Conditions: []corev1.PodCondition{{
			Type: corev1.PodReady, Status: corev1.ConditionFalse, Reason: "ReadinessGatesNotReady",
		}}},
	}
	if err := k8s.InitTestResourceCache(fake.NewClientset(pod, job, cronjob, failedPod, failedJob, gateBlocked)); err != nil {
		t.Fatal(err)
	}
	gvr := schema.GroupVersionResource{Version: "v1", Resource: "pods"}
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	dyn := dynamicfake.NewSimpleDynamicClient(scheme, pod, failedPod, gateBlocked)
	if err := k8s.InitTestDynamicResourceCache(dyn, []k8s.APIResource{{Version: "v1", Kind: "Pod", Name: "pods", Namespaced: true, Verbs: []string{"get", "list", "watch"}}}); err != nil {
		t.Fatal(err)
	}
	provider := NewCacheProvider()
	assertNoCompletedIssue := func() {
		t.Helper()
		failedPodReported, failedJobReported := false, false
		for _, issue := range Compose(provider, Filters{Namespaces: []string{"batch"}, Limit: NoLimit}) {
			switch issue.Name {
			case pod.Name, job.Name, cronjob.Name:
				t.Errorf("successful CronJob run emitted an issue: %+v", issue)
			case failedPod.Name:
				failedPodReported = failedPodReported || issue.Source == SourceProblem
			case failedJob.Name:
				failedJobReported = failedJobReported || issue.Category == issuesapi.CategoryJobFailed
			}
		}
		if !failedPodReported || !failedJobReported {
			t.Errorf("failed batch work must stay visible: Pod=%v, Job=%v", failedPodReported, failedJobReported)
		}
	}
	if k8s.GetDynamicResourceCache().HasCoveringInformer(gvr, "batch") {
		t.Fatal("Pod dynamic watch unexpectedly exists before the on-demand read")
	}
	assertNoCompletedIssue()
	if _, err := k8s.GetDynamicResourceCache().ListBlocking(gvr, "batch", 2*time.Second); err != nil {
		t.Fatal(err)
	}
	assertNoCompletedIssue()
	gateReported := false
	for _, issue := range Compose(provider, Filters{Namespaces: []string{"batch"}, Limit: NoLimit}) {
		if issue.Name == gateBlocked.Name && issue.Source == SourceCondition {
			gateReported = true
		}
	}
	if !gateReported {
		t.Fatal("a Pod blocked by its readiness gate must retain its condition issue")
	}
}

func TestGenericConditionsRespectCorePodCompletion(t *testing.T) {
	for _, tc := range []struct {
		name, group, kind, phase, reason string
		wantIssue                        bool
	}{
		{name: "completed", kind: "Pod", phase: "Succeeded", reason: "PodCompleted"},
		{name: "completion overrides stale readiness", kind: "Pod", phase: "Succeeded", reason: "ContainersNotReady"},
		{name: "failed", kind: "Pod", phase: "Failed", reason: "PodFailed", wantIssue: true},
		{name: "failed with completed readiness reason", kind: "Pod", phase: "Failed", reason: "PodCompleted", wantIssue: true},
		{name: "running not ready", kind: "Pod", phase: "Running", reason: "ContainersNotReady", wantIssue: true},
		{name: "unknown phase", kind: "Pod", reason: "ContainersNotReady", wantIssue: true},
		{name: "CRD named Pod", group: "example.com", kind: "Pod", phase: "Succeeded", reason: "ReconcileFailed", wantIssue: true},
		{name: "CRD successful phase with failed condition", group: "example.com", kind: "Widget", phase: "Succeeded", reason: "ReconcileFailed", wantIssue: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gvr := schema.GroupVersionResource{Group: tc.group, Version: "v1", Resource: "pods"}
			object := &unstructured.Unstructured{Object: map[string]any{
				"metadata": map[string]any{"name": "run", "namespace": "batch"},
				"status": map[string]any{
					"phase": tc.phase,
					"conditions": []any{map[string]any{
						"type": "Ready", "status": "False", "reason": tc.reason,
					}},
				},
			}}
			p := &fakeProvider{
				dynamic:    map[schema.GroupVersionResource][]*unstructured.Unstructured{gvr: {object}},
				kinds:      map[schema.GroupVersionResource]string{gvr: tc.kind},
				namespaced: map[schema.GroupVersionResource]bool{gvr: true},
			}
			for _, namespaces := range [][]string{nil, {"batch"}} {
				out := Compose(p, Filters{Namespaces: namespaces, Limit: NoLimit})
				if (len(out) > 0) != tc.wantIssue {
					t.Fatalf("namespaces=%v: issues=%+v, want issue=%v", namespaces, out, tc.wantIssue)
				}
				if tc.wantIssue && (len(out) != 1 || out[0].Category != issuesapi.CategoryOperatorConditionFail || out[0].Reason != "Ready: "+tc.reason) {
					t.Fatalf("unexpected condition issue: %+v", out)
				}
			}
		})
	}
}

func TestReadinessGateFailureDoesNotRequireADynamicWatch(t *testing.T) {
	defer k8s.ResetTestState()
	defer k8s.ResetTestDynamicState()
	now := time.Now().UTC().Truncate(time.Second)
	since := now.Add(-7 * time.Minute)
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "gate-blocked", Namespace: "test", CreationTimestamp: metav1.NewTime(now.Add(-time.Hour))}, Spec: corev1.PodSpec{ReadinessGates: []corev1.PodReadinessGate{{ConditionType: "example.com/ready"}}}, Status: corev1.PodStatus{Phase: corev1.PodRunning, Conditions: []corev1.PodCondition{
		{Type: corev1.PodReady, Status: corev1.ConditionFalse, Reason: "ReadinessGatesNotReady", Message: "gate controller has not reported ready", LastTransitionTime: metav1.NewTime(now.Add(-10 * time.Minute))},
		{Type: corev1.ContainersReady, Status: corev1.ConditionTrue, LastTransitionTime: metav1.NewTime(since)},
	}, ContainerStatuses: []corev1.ContainerStatus{{Name: "app", Ready: true, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}}}}
	if err := k8s.InitTestResourceCache(fake.NewClientset(pod)); err != nil {
		t.Fatal(err)
	}
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := k8s.InitTestDynamicResourceCache(dynamicfake.NewSimpleDynamicClient(scheme, pod), []k8s.APIResource{{Version: "v1", Kind: "Pod", Name: "pods", Namespaced: true, Verbs: []string{"get", "list", "watch"}}}); err != nil {
		t.Fatal(err)
	}
	provider := NewCacheProvider()
	check := func() {
		t.Helper()
		found := false
		for _, i := range Compose(provider, Filters{Namespaces: []string{"test"}}) {
			if i.Source == SourceProblem && i.Name == pod.Name {
				found = true
				if i.Category != issuesapi.CategoryReadinessFailed || i.Severity != SeverityWarning || i.Message != "gate controller has not reported ready" || !i.FirstSeen.Equal(since) {
					t.Fatalf("wrong gate finding %+v", i)
				}
			}
		}
		if !found {
			t.Fatal("native gate issue absent")
		}
	}
	check()
	if _, err := k8s.GetDynamicResourceCache().ListBlocking(schema.GroupVersionResource{Version: "v1", Resource: "pods"}, "test", 2*time.Second); err != nil {
		t.Fatal(err)
	}
	check()
}

func TestPodReadinessNoiseKeepsRealFailures(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	for _, tc := range []struct {
		name, reason, phase, group, root                                                                 string
		age, containersAge                                                                               time.Duration
		noTime, gates, unknownGate, otherFailure, withheld, workload, noContainersReady, containersFalse bool
		want                                                                                             []string
	}{
		{name: "fresh startup", age: 30 * time.Second},
		{name: "fresh Pending", phase: "Pending", age: 30 * time.Second},
		{name: "fresh startup preserves fatal failure", age: 30 * time.Second, root: "ImagePullBackOff", want: []string{"ImagePullBackOff"}},
		{name: "old readiness without native", want: []string{"Ready: ContainersNotReady"}},
		{name: "unknown transition grants no grace", noTime: true, want: []string{"Ready: ContainersNotReady"}},
		{name: "future transition grants no grace", age: -time.Minute, want: []string{"Ready: ContainersNotReady"}},
		{name: "fresh Ready does not hide other error", age: 30 * time.Second, otherFailure: true, want: []string{"Healthy: ControllerError"}},
		{name: "fresh gate Ready", reason: "ReadinessGatesNotReady", gates: true, age: 30 * time.Second},
		{name: "long startup newly container ready", reason: "ReadinessGatesNotReady", gates: true, age: time.Hour, containersAge: 30 * time.Second},

		{name: "future ContainersReady grants no grace", reason: "ReadinessGatesNotReady", gates: true, containersAge: -time.Minute, want: []string{"Ready: ReadinessGatesNotReady"}},
		{name: "gate without ContainersReady", reason: "ReadinessGatesNotReady", gates: true, noContainersReady: true, age: 30 * time.Second, want: []string{"Ready: ReadinessGatesNotReady"}},
		{name: "gate with ContainersReady False", reason: "ReadinessGatesNotReady", gates: true, containersFalse: true, age: 30 * time.Second, want: []string{"Ready: ReadinessGatesNotReady"}},
		{name: "old gate without native", reason: "ReadinessGatesNotReady", gates: true, want: []string{"Ready: ReadinessGatesNotReady"}},
		{name: "unknown gate without native", reason: "ReadinessGatesNotReady", gates: true, unknownGate: true, want: []string{"Ready: ReadinessGatesNotReady"}},
		{name: "unknown gate with unrelated native error", reason: "ReadinessGatesNotReady", gates: true, unknownGate: true, root: "CrashLoopBackOff", want: []string{"Ready: ReadinessGatesNotReady", "CrashLoopBackOff"}},
		{name: "gate native echo", reason: "ReadinessGatesNotReady", gates: true, root: "ReadinessGatesNotReady", want: []string{"ReadinessGatesNotReady"}},
		{name: "runtime native echo", root: "CrashLoopBackOff", want: []string{"CrashLoopBackOff"}},
		{name: "withheld native retains fallback", root: "CrashLoopBackOff", withheld: true, want: []string{"Ready: ContainersNotReady"}},
		{name: "root later folds into workload", root: "CrashLoopBackOff", workload: true, want: []string{"CrashLoopBackOff"}},
		{name: "failed Pod echo", phase: "Failed", reason: "PodFailed", root: "Error", want: []string{"Error"}},
		{name: "failed phase is not startup grace", phase: "Failed", age: 30 * time.Second, want: []string{"Ready: ContainersNotReady"}},
		{name: "node unreachable stays", reason: "NodeNotReady", age: 30 * time.Second, want: []string{"Ready: NodeNotReady"}},
		{name: "custom reason stays", reason: "CustomFailure", root: "CrashLoopBackOff", want: []string{"Ready: CustomFailure", "CrashLoopBackOff"}},
		{name: "colliding kind stays", group: "example.com", age: 30 * time.Second, want: []string{"Ready: ContainersNotReady"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reason := tc.reason
			if reason == "" {
				reason = "ContainersNotReady"
			}
			phase := tc.phase
			if phase == "" {
				phase = "Running"
			}
			age := tc.age
			if age == 0 {
				age = 6 * time.Minute
			}
			containerAge := tc.containersAge
			if containerAge == 0 {
				containerAge = 6 * time.Minute
			}
			ready := map[string]any{"type": "Ready", "status": "False", "reason": reason}
			if !tc.noTime {
				ready["lastTransitionTime"] = now.Add(-age).Format(time.RFC3339)
			}
			conds := []map[string]any{ready}
			if tc.gates && !tc.noContainersReady {
				status := "True"
				if tc.containersFalse {
					status = "False"
				}
				conds = append(conds, map[string]any{"type": "ContainersReady", "status": status, "lastTransitionTime": now.Add(-containerAge).Format(time.RFC3339)})
			}
			if tc.unknownGate {
				conds = append(conds, map[string]any{"type": "example.com/ready", "status": "Unknown"})
			}
			if tc.otherFailure {
				conds = append(conds, map[string]any{"type": "Healthy", "status": "False", "reason": "ControllerError"})
			}
			u := rolloutWithConditions(conds)
			u.SetName("pod")
			u.SetNamespace("test")
			u.SetCreationTimestamp(metav1.NewTime(now.Add(-time.Hour)))
			u.Object["status"].(map[string]any)["phase"] = phase
			if tc.gates {
				u.Object["spec"] = map[string]any{"readinessGates": []any{map[string]any{"conditionType": "example.com/ready"}}}
			}
			gvr := schema.GroupVersionResource{Group: tc.group, Version: "v1", Resource: "pods"}
			p := &fakeProvider{dynamic: map[schema.GroupVersionResource][]*unstructured.Unstructured{gvr: {u}}, kinds: map[schema.GroupVersionResource]string{gvr: "Pod"}, namespaced: map[schema.GroupVersionResource]bool{gvr: true}}
			if tc.root != "" {
				root := k8s.Detection{Kind: "Pod", Namespace: "test", Name: "pod", Reason: tc.root, Severity: "critical"}
				if tc.withheld {
					root.RequiredReads = []issuesapi.EvidenceRead{{Resource: "secrets", Namespace: "test", Verb: "get"}}
				}
				if tc.workload {
					root.OwnerKind = "Deployment"
					root.OwnerGroup = "apps"
					root.OwnerName = "app"
					p.problems = append(p.problems, k8s.Detection{Kind: "Deployment", Group: "apps", Namespace: "test", Name: "app", Reason: "0/1 available", Severity: "critical"})
				}
				p.problems = append(p.problems, root)
			}
			for _, grouped := range []bool{false, true} {
				f := Filters{Namespaces: []string{"test"}, Grouped: grouped}
				if tc.withheld {
					f.CanReadEvidence = func(r EvidenceRead) bool { return r.Resource != "secrets" }
				}
				out := Compose(p, f)
				if len(out) != len(tc.want) {
					t.Fatalf("grouped=%v want%v got%+v", grouped, tc.want, out)
				}
				for _, want := range tc.want {
					found := false
					for _, i := range out {
						if i.Reason == want {
							found = true
						}
					}
					if !found {
						t.Fatalf("missing %q in %+v", want, out)
					}
				}
			}
		})
	}
}

func TestFreshGateWithoutTransitionTimesThroughCache(t *testing.T) {
	defer k8s.ResetTestState()
	defer k8s.ResetTestDynamicState()
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "young-gate", Namespace: "test", CreationTimestamp: metav1.NewTime(time.Now().Add(-30 * time.Second))}, Spec: corev1.PodSpec{ReadinessGates: []corev1.PodReadinessGate{{ConditionType: "example.com/ready"}}}, Status: corev1.PodStatus{Phase: corev1.PodRunning, Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionFalse, Reason: "ReadinessGatesNotReady"}, {Type: corev1.ContainersReady, Status: corev1.ConditionTrue}}, ContainerStatuses: []corev1.ContainerStatus{{Name: "app", Ready: true, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}}}}
	control := pod.DeepCopy()
	control.Name = "independent-error"
	control.Status.Conditions = append(control.Status.Conditions, corev1.PodCondition{Type: "Healthy", Status: corev1.ConditionFalse, Reason: "ControllerError"})
	if err := k8s.InitTestResourceCache(fake.NewClientset(pod, control)); err != nil {
		t.Fatal(err)
	}
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := k8s.InitTestDynamicResourceCache(dynamicfake.NewSimpleDynamicClient(scheme, pod, control), []k8s.APIResource{{Version: "v1", Kind: "Pod", Name: "pods", Namespaced: true, Verbs: []string{"get", "list", "watch"}}}); err != nil {
		t.Fatal(err)
	}
	p := NewCacheProvider()
	for _, warm := range []bool{false, true} {
		if warm {
			if _, err := k8s.GetDynamicResourceCache().ListBlocking(schema.GroupVersionResource{Version: "v1", Resource: "pods"}, "test", 2*time.Second); err != nil {
				t.Fatal(err)
			}
		}
		for _, grouped := range []bool{false, true} {
			got := Compose(p, Filters{Namespaces: []string{"test"}, Grouped: grouped})
			want := 0
			if warm {
				want = 1
			}
			if len(got) != want || (warm && (got[0].Name != control.Name || got[0].Reason != "Healthy: ControllerError")) {
				t.Fatalf("warm=%v grouped=%v got %+v", warm, grouped, got)
			}
		}
	}
}
