package controller

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	slumlordv1alpha1 "github.com/cschockaert/slumlord/api/v1alpha1"
)

func makeNodeDrainPolicy(name string, cron string, cpuThreshold, memThreshold *int32) *slumlordv1alpha1.SlumlordNodeDrainPolicy {
	return &slumlordv1alpha1.SlumlordNodeDrainPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Spec: slumlordv1alpha1.SlumlordNodeDrainPolicySpec{
			Thresholds: slumlordv1alpha1.DrainThresholds{
				CPURequestPercent:    cpuThreshold,
				MemoryRequestPercent: memThreshold,
			},
			Schedule: slumlordv1alpha1.DrainSchedule{
				Cron:     cron,
				Timezone: "UTC",
			},
		},
	}
}

func makeReadyNode(name string, cpuAlloc, memAlloc string, labels map[string]string) *corev1.Node {
	node := makeNode(name, cpuAlloc, memAlloc, nil)
	node.Labels = labels
	if labels == nil {
		node.Labels = map[string]string{}
	}
	node.Status.Conditions = []corev1.NodeCondition{
		{
			Type:   corev1.NodeReady,
			Status: corev1.ConditionTrue,
		},
	}
	return node
}

func makeEmptyDirPod(name, namespace, nodeName string) *corev1.Pod {
	pod := makeRunningPod(name, namespace, nodeName, "100m", "128Mi")
	pod.Spec.Volumes = []corev1.Volume{
		{
			Name: "cache",
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{},
			},
		},
	}
	return pod
}

func newDrainReconciler(objs ...client.Object) (*NodeDrainPolicyReconciler, client.Client) {
	scheme := newTestScheme()
	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objs...).
		WithStatusSubresource(&slumlordv1alpha1.SlumlordNodeDrainPolicy{}).
		Build()

	return &NodeDrainPolicyReconciler{
		Client:   fakeClient,
		Scheme:   scheme,
		Recorder: events.NewFakeRecorder(100),
	}, fakeClient
}

// --- Tests ---

func TestNodeDrain_CronParsing_Valid(t *testing.T) {
	policy := makeNodeDrainPolicy("test", "0 2 * * 1-5", int32Ptr(30), int32Ptr(30))

	reconciler := &NodeDrainPolicyReconciler{}
	nextRun, err := reconciler.computeNextRun(policy)
	if err != nil {
		t.Fatalf("computeNextRun() error = %v", err)
	}
	if nextRun.IsZero() {
		t.Error("Expected non-zero next run time")
	}
}

func TestNodeDrain_CronParsing_Invalid(t *testing.T) {
	policy := makeNodeDrainPolicy("test", "not-a-cron", int32Ptr(30), int32Ptr(30))

	reconciler := &NodeDrainPolicyReconciler{}
	_, err := reconciler.computeNextRun(policy)
	if err == nil {
		t.Error("Expected error for invalid cron expression")
	}
}

func TestNodeDrain_CronParsing_Timezone(t *testing.T) {
	policy := makeNodeDrainPolicy("test", "0 2 * * *", int32Ptr(30), int32Ptr(30))
	policy.Spec.Schedule.Timezone = "America/New_York"

	reconciler := &NodeDrainPolicyReconciler{}
	nextRun, err := reconciler.computeNextRun(policy)
	if err != nil {
		t.Fatalf("computeNextRun() error = %v", err)
	}

	loc, _ := time.LoadLocation("America/New_York")
	if nextRun.Location().String() != loc.String() {
		t.Errorf("Expected timezone %s, got %s", loc, nextRun.Location())
	}
}

func TestNodeDrain_CronParsing_InvalidTimezone(t *testing.T) {
	policy := makeNodeDrainPolicy("test", "0 2 * * *", int32Ptr(30), int32Ptr(30))
	policy.Spec.Schedule.Timezone = "Invalid/Zone"

	reconciler := &NodeDrainPolicyReconciler{}
	_, err := reconciler.computeNextRun(policy)
	if err == nil {
		t.Error("Expected error for invalid timezone")
	}
}

func TestNodeDrain_NodeAnalysis(t *testing.T) {
	ctx := context.Background()

	node1 := makeReadyNode("node-low", "4000m", "8Gi", nil)
	node2 := makeReadyNode("node-high", "4000m", "8Gi", nil)

	pod1 := makeRunningPod("pod-low", "default", "node-low", "400m", "800Mi")
	pod2 := makeRunningPod("pod-high", "default", "node-high", "3600m", "7200Mi")

	policy := makeNodeDrainPolicy("test", "0 2 * * *", int32Ptr(30), int32Ptr(30))

	reconciler, _ := newDrainReconciler(node1, node2, pod1, pod2, policy)

	infos, err := reconciler.analyzeNodes(ctx, policy)
	if err != nil {
		t.Fatalf("analyzeNodes() error = %v", err)
	}

	if len(infos) != 2 {
		t.Fatalf("Expected 2 nodes, got %d", len(infos))
	}

	nodeMap := make(map[string]drainNodeInfo)
	for _, info := range infos {
		nodeMap[info.name] = info
	}

	low := nodeMap["node-low"]
	if low.cpuRequestPercent != 10 {
		t.Errorf("node-low CPU: expected 10%%, got %d%%", low.cpuRequestPercent)
	}

	high := nodeMap["node-high"]
	if high.cpuRequestPercent != 90 {
		t.Errorf("node-high CPU: expected 90%%, got %d%%", high.cpuRequestPercent)
	}
}

func TestNodeDrain_NodeSelector(t *testing.T) {
	ctx := context.Background()

	matching := makeReadyNode("node-matching", "4000m", "8Gi", map[string]string{"pool": "spot"})
	nonMatching := makeReadyNode("node-other", "4000m", "8Gi", map[string]string{"pool": "ondemand"})

	pod := makeRunningPod("pod-1", "default", "node-matching", "200m", "256Mi")

	policy := makeNodeDrainPolicy("test", "0 2 * * *", int32Ptr(30), int32Ptr(30))
	policy.Spec.NodeSelector = map[string]string{"pool": "spot"}

	reconciler, _ := newDrainReconciler(matching, nonMatching, pod, policy)

	infos, err := reconciler.analyzeNodes(ctx, policy)
	if err != nil {
		t.Fatalf("analyzeNodes() error = %v", err)
	}

	if len(infos) != 1 {
		t.Fatalf("Expected 1 node (matching), got %d", len(infos))
	}
	if infos[0].name != "node-matching" {
		t.Errorf("Expected node-matching, got %s", infos[0].name)
	}
}

func TestNodeDrain_ThresholdOR_CPUOnly(t *testing.T) {
	ctx := context.Background()

	// node with low CPU (10%) but high mem (90%)
	node := makeReadyNode("node-1", "4000m", "8Gi", nil)
	podCPU := makeRunningPod("pod-cpu", "default", "node-1", "400m", "7200Mi")

	policy := makeNodeDrainPolicy("test", "0 2 * * *", int32Ptr(30), int32Ptr(30))

	reconciler, _ := newDrainReconciler(node, podCPU, policy)

	infos, err := reconciler.analyzeNodes(ctx, policy)
	if err != nil {
		t.Fatalf("analyzeNodes() error = %v", err)
	}

	candidates := reconciler.findDrainCandidates(infos, policy)
	// OR logic: CPU is 10% < 30%, so node is a candidate even though mem is ~87%
	if len(candidates) != 1 {
		t.Fatalf("Expected 1 candidate (CPU below threshold), got %d", len(candidates))
	}
}

func TestNodeDrain_ThresholdOR_MemOnly(t *testing.T) {
	ctx := context.Background()

	// node with high CPU (90%) but low mem (10%)
	node := makeReadyNode("node-1", "4000m", "8Gi", nil)
	podMem := makeRunningPod("pod-mem", "default", "node-1", "3600m", "800Mi")

	policy := makeNodeDrainPolicy("test", "0 2 * * *", int32Ptr(30), int32Ptr(30))

	reconciler, _ := newDrainReconciler(node, podMem, policy)

	infos, err := reconciler.analyzeNodes(ctx, policy)
	if err != nil {
		t.Fatalf("analyzeNodes() error = %v", err)
	}

	candidates := reconciler.findDrainCandidates(infos, policy)
	// OR logic: mem is ~10% < 30%, so node is a candidate even though CPU is 90%
	if len(candidates) != 1 {
		t.Fatalf("Expected 1 candidate (mem below threshold), got %d", len(candidates))
	}
}

func TestNodeDrain_ThresholdOR_NoneBelow(t *testing.T) {
	ctx := context.Background()

	// node with high CPU (90%) and high mem (90%)
	node := makeReadyNode("node-1", "4000m", "8Gi", nil)
	pod := makeRunningPod("pod-1", "default", "node-1", "3600m", "7200Mi")

	policy := makeNodeDrainPolicy("test", "0 2 * * *", int32Ptr(30), int32Ptr(30))

	reconciler, _ := newDrainReconciler(node, pod, policy)

	infos, err := reconciler.analyzeNodes(ctx, policy)
	if err != nil {
		t.Fatalf("analyzeNodes() error = %v", err)
	}

	candidates := reconciler.findDrainCandidates(infos, policy)
	if len(candidates) != 0 {
		t.Fatalf("Expected 0 candidates (both above threshold), got %d", len(candidates))
	}
}

func TestNodeDrain_SafetyMaxNodes(t *testing.T) {
	ctx := context.Background()

	// 3 underutilized nodes
	node1 := makeReadyNode("node-1", "4000m", "8Gi", nil)
	node2 := makeReadyNode("node-2", "4000m", "8Gi", nil)
	node3 := makeReadyNode("node-3", "4000m", "8Gi", nil)
	// 1 high utilization node (target)
	nodeTarget := makeReadyNode("node-target", "4000m", "8Gi", nil)

	pod1 := makeRunningPod("pod-1", "default", "node-1", "200m", "256Mi")
	pod2 := makeRunningPod("pod-2", "default", "node-2", "200m", "256Mi")
	pod3 := makeRunningPod("pod-3", "default", "node-3", "200m", "256Mi")
	podTarget := makeRunningPod("pod-target", "default", "node-target", "3000m", "6Gi")

	maxNodes := int32(2)
	minReady := int32(1)
	policy := makeNodeDrainPolicy("test", "0 2 * * *", int32Ptr(30), int32Ptr(30))
	policy.Spec.Safety.MaxNodesPerRun = &maxNodes
	policy.Spec.Safety.MinReadyNodes = &minReady

	reconciler, _ := newDrainReconciler(node1, node2, node3, nodeTarget, pod1, pod2, pod3, podTarget, policy)

	infos, err := reconciler.analyzeNodes(ctx, policy)
	if err != nil {
		t.Fatalf("analyzeNodes() error = %v", err)
	}

	candidates := reconciler.findDrainCandidates(infos, policy)
	if len(candidates) != 3 {
		t.Fatalf("Expected 3 candidates, got %d", len(candidates))
	}

	// Apply safety: maxNodesPerRun=2, minReadyNodes=1, readyCount=4
	// maxDrainable = min(2, 4-1) = min(2, 3) = 2
	readyCount := int32(0)
	for _, info := range infos {
		if isNodeReady(&info.node) {
			readyCount++
		}
	}
	maxDrainable := readyCount - minReady
	if maxNodes < maxDrainable {
		maxDrainable = maxNodes
	}
	if int32(len(candidates)) > maxDrainable {
		candidates = candidates[:maxDrainable]
	}

	if len(candidates) != 2 {
		t.Errorf("Expected 2 candidates after safety cap, got %d", len(candidates))
	}
}

func TestNodeDrain_SafetyMinReady(t *testing.T) {
	ctx := context.Background()

	// 2 underutilized nodes, 1 target — minReady=2 means only 1 can be drained
	node1 := makeReadyNode("node-1", "4000m", "8Gi", nil)
	node2 := makeReadyNode("node-2", "4000m", "8Gi", nil)
	nodeTarget := makeReadyNode("node-target", "4000m", "8Gi", nil)

	pod1 := makeRunningPod("pod-1", "default", "node-1", "200m", "256Mi")
	pod2 := makeRunningPod("pod-2", "default", "node-2", "200m", "256Mi")
	podTarget := makeRunningPod("pod-target", "default", "node-target", "3000m", "6Gi")

	maxNodes := int32(10)
	minReady := int32(2)
	policy := makeNodeDrainPolicy("test", "0 2 * * *", int32Ptr(30), int32Ptr(30))
	policy.Spec.Safety.MaxNodesPerRun = &maxNodes
	policy.Spec.Safety.MinReadyNodes = &minReady

	reconciler, _ := newDrainReconciler(node1, node2, nodeTarget, pod1, pod2, podTarget, policy)

	infos, err := reconciler.analyzeNodes(ctx, policy)
	if err != nil {
		t.Fatalf("analyzeNodes() error = %v", err)
	}

	candidates := reconciler.findDrainCandidates(infos, policy)
	if len(candidates) != 2 {
		t.Fatalf("Expected 2 candidates, got %d", len(candidates))
	}

	// readyCount=3, minReady=2, maxDrainable=min(10, 3-2)=1
	readyCount := int32(0)
	for _, info := range infos {
		if isNodeReady(&info.node) {
			readyCount++
		}
	}
	maxDrainable := readyCount - minReady
	if maxNodes < maxDrainable {
		maxDrainable = maxNodes
	}
	if int32(len(candidates)) > maxDrainable {
		candidates = candidates[:maxDrainable]
	}

	if len(candidates) != 1 {
		t.Errorf("Expected 1 candidate after minReady constraint, got %d", len(candidates))
	}
}

func TestNodeDrain_Cordon(t *testing.T) {
	ctx := context.Background()

	node := makeReadyNode("node-1", "4000m", "8Gi", nil)

	reconciler, fakeClient := newDrainReconciler(node)

	if err := reconciler.cordonNode(ctx, node); err != nil {
		t.Fatalf("cordonNode() error = %v", err)
	}

	var updated corev1.Node
	if err := fakeClient.Get(ctx, types.NamespacedName{Name: "node-1"}, &updated); err != nil {
		t.Fatalf("Failed to get node: %v", err)
	}
	if !updated.Spec.Unschedulable {
		t.Error("Expected node to be unschedulable after cordon")
	}
}

func TestNodeDrain_DryRun(t *testing.T) {
	ctx := context.Background()

	node := makeReadyNode("node-low", "4000m", "8Gi", nil)
	nodeTarget := makeReadyNode("node-target", "4000m", "8Gi", nil)
	podLow := makeRunningPod("pod-low", "default", "node-low", "200m", "256Mi")
	podTarget := makeRunningPod("pod-target", "default", "node-target", "3000m", "6Gi")

	// Use a past cron to trigger immediate run
	policy := makeNodeDrainPolicy("test-dryrun", "* * * * *", int32Ptr(30), int32Ptr(30))
	policy.Spec.Safety.DryRun = true

	reconciler, fakeClient := newDrainReconciler(node, nodeTarget, podLow, podTarget, policy)

	// Reconcile — the first call adds the finalizer
	_, err := reconciler.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Name: policy.Name},
	})
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	// Reconcile again — now the drain should run
	_, err = reconciler.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Name: policy.Name},
	})
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	var updated slumlordv1alpha1.SlumlordNodeDrainPolicy
	if err := fakeClient.Get(ctx, types.NamespacedName{Name: policy.Name}, &updated); err != nil {
		t.Fatalf("Failed to get policy: %v", err)
	}

	if updated.Status.LastRun == nil {
		t.Fatal("Expected lastRun to be set")
	}

	// In dry run, no nodes should actually be drained
	if updated.Status.LastRun.NodesDrained != 0 {
		t.Errorf("Expected 0 drained in dry run, got %d", updated.Status.LastRun.NodesDrained)
	}

	// But we should see node results with "dry-run" action
	foundDryRun := false
	for _, result := range updated.Status.LastRun.NodeResults {
		if result.Action == "dry-run" {
			foundDryRun = true
		}
	}
	if !foundDryRun && updated.Status.LastRun.NodesCandidates > 0 {
		t.Error("Expected dry-run action in node results")
	}

	// Verify node is NOT cordoned (dry run should not modify anything)
	var nodeAfter corev1.Node
	if err := fakeClient.Get(ctx, types.NamespacedName{Name: "node-low"}, &nodeAfter); err != nil {
		t.Fatalf("Failed to get node: %v", err)
	}
	if nodeAfter.Spec.Unschedulable {
		t.Error("Node should NOT be cordoned in dry run mode")
	}
}

func TestNodeDrain_Suspend(t *testing.T) {
	ctx := context.Background()

	policy := makeNodeDrainPolicy("test-suspend", "* * * * *", int32Ptr(30), int32Ptr(30))
	policy.Spec.Suspend = true

	reconciler, fakeClient := newDrainReconciler(policy)

	// First reconcile adds finalizer
	_, err := reconciler.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Name: policy.Name},
	})
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	// Second reconcile handles suspend
	result, err := reconciler.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Name: policy.Name},
	})
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	if result.RequeueAfter != 5*time.Minute {
		t.Errorf("Expected requeue after 5m for suspended policy, got %v", result.RequeueAfter)
	}

	var updated slumlordv1alpha1.SlumlordNodeDrainPolicy
	if err := fakeClient.Get(ctx, types.NamespacedName{Name: policy.Name}, &updated); err != nil {
		t.Fatalf("Failed to get policy: %v", err)
	}

	// Should have Suspended condition
	foundSuspended := false
	for _, cond := range updated.Status.Conditions {
		if cond.Type == "Ready" && cond.Reason == "Suspended" {
			foundSuspended = true
		}
	}
	if !foundSuspended {
		t.Error("Expected Suspended condition")
	}

	// No drain should have run
	if updated.Status.LastRun != nil {
		t.Error("Expected no lastRun when suspended")
	}
}

func TestNodeDrain_Finalizer_AddedOnCreate(t *testing.T) {
	ctx := context.Background()

	policy := makeNodeDrainPolicy("test-finalizer", "0 2 * * *", int32Ptr(30), int32Ptr(30))

	reconciler, fakeClient := newDrainReconciler(policy)

	_, err := reconciler.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Name: policy.Name},
	})
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	var updated slumlordv1alpha1.SlumlordNodeDrainPolicy
	if err := fakeClient.Get(ctx, types.NamespacedName{Name: policy.Name}, &updated); err != nil {
		t.Fatalf("Failed to get policy: %v", err)
	}

	found := false
	for _, f := range updated.Finalizers {
		if f == nodeDrainFinalizer {
			found = true
			break
		}
	}
	if !found {
		t.Error("Expected finalizer to be added on create")
	}
}

func TestNodeDrain_Finalizer_RemovedOnDelete(t *testing.T) {
	ctx := context.Background()

	now := metav1.Now()
	policy := makeNodeDrainPolicy("test-delete", "0 2 * * *", int32Ptr(30), int32Ptr(30))
	policy.Finalizers = []string{nodeDrainFinalizer}
	policy.DeletionTimestamp = &now

	reconciler, fakeClient := newDrainReconciler(policy)

	_, err := reconciler.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Name: policy.Name},
	})
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	var updated slumlordv1alpha1.SlumlordNodeDrainPolicy
	err = fakeClient.Get(ctx, types.NamespacedName{Name: policy.Name}, &updated)
	if err == nil {
		for _, f := range updated.Finalizers {
			if f == nodeDrainFinalizer {
				t.Error("Expected finalizer to be removed on delete")
			}
		}
	}
}

func TestNodeDrain_SkipUnschedulable(t *testing.T) {
	ctx := context.Background()

	schedulable := makeReadyNode("node-schedulable", "4000m", "8Gi", nil)
	unschedulable := makeReadyNode("node-cordoned", "4000m", "8Gi", nil)
	unschedulable.Spec.Unschedulable = true

	pod := makeRunningPod("pod-1", "default", "node-schedulable", "200m", "256Mi")

	policy := makeNodeDrainPolicy("test", "0 2 * * *", int32Ptr(50), int32Ptr(50))

	reconciler, _ := newDrainReconciler(schedulable, unschedulable, pod, policy)

	infos, err := reconciler.analyzeNodes(ctx, policy)
	if err != nil {
		t.Fatalf("analyzeNodes() error = %v", err)
	}

	if len(infos) != 1 {
		t.Fatalf("Expected 1 node (schedulable only), got %d", len(infos))
	}
	if infos[0].name != "node-schedulable" {
		t.Errorf("Expected node-schedulable, got %s", infos[0].name)
	}
}

func TestNodeDrain_IgnoreDaemonSetPods(t *testing.T) {
	node := makeReadyNode("node-1", "4000m", "8Gi", nil)
	normalPod := makeRunningPod("normal", "default", "node-1", "100m", "128Mi")
	dsPod := makeDaemonSetPod("ds-pod", "default", "node-1")

	// isDrainable should return false for DaemonSet pods
	if isDrainable(dsPod, true, false, nil) {
		t.Error("Expected DaemonSet pod to not be drainable")
	}
	if !isDrainable(normalPod, true, false, nil) {
		t.Error("Expected normal pod to be drainable")
	}

	_ = node
}

func TestNodeDrain_EmptyDirPods(t *testing.T) {
	pod := makeEmptyDirPod("emptydir-pod", "default", "node-1")

	// Without deleteEmptyDirData, emptyDir pods should NOT be drainable
	if isDrainable(pod, true, false, nil) {
		t.Error("Expected emptyDir pod to not be drainable when deleteEmptyDirData=false")
	}

	// With deleteEmptyDirData, emptyDir pods should be drainable
	if !isDrainable(pod, true, true, nil) {
		t.Error("Expected emptyDir pod to be drainable when deleteEmptyDirData=true")
	}
}

func TestNodeDrain_PodSelector(t *testing.T) {
	pod := makeRunningPod("pod-1", "default", "node-1", "100m", "128Mi")
	pod.Labels = map[string]string{"app": "web"}

	// Pod matches selector
	if !isDrainable(pod, true, false, map[string]string{"app": "web"}) {
		t.Error("Expected pod to be drainable when matching podSelector")
	}

	// Pod does not match selector
	if isDrainable(pod, true, false, map[string]string{"app": "api"}) {
		t.Error("Expected pod to NOT be drainable when not matching podSelector")
	}
}

func TestNodeDrain_MirrorPodNotDrainable(t *testing.T) {
	pod := makeMirrorPod("mirror", "default", "node-1")
	if isDrainable(pod, true, false, nil) {
		t.Error("Expected mirror pod to not be drainable")
	}
}

func TestNodeDrain_TerminatingPodNotDrainable(t *testing.T) {
	pod := makeRunningPod("terminating", "default", "node-1", "100m", "128Mi")
	now := metav1.Now()
	pod.DeletionTimestamp = &now

	if isDrainable(pod, true, false, nil) {
		t.Error("Expected terminating pod to not be drainable")
	}
}

func TestNodeDrain_Conditions(t *testing.T) {
	policy := &slumlordv1alpha1.SlumlordNodeDrainPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "test"},
	}

	reconciler := &NodeDrainPolicyReconciler{}

	// Set Ready=True
	reconciler.setCondition(policy, metav1.ConditionTrue, "RunCompleted", "All good")
	if len(policy.Status.Conditions) != 1 {
		t.Fatalf("Expected 1 condition, got %d", len(policy.Status.Conditions))
	}
	if policy.Status.Conditions[0].Status != metav1.ConditionTrue {
		t.Error("Expected condition status True")
	}
	if policy.Status.Conditions[0].Reason != "RunCompleted" {
		t.Errorf("Expected reason RunCompleted, got %s", policy.Status.Conditions[0].Reason)
	}

	// Update to Suspended
	reconciler.setCondition(policy, metav1.ConditionFalse, "Suspended", "Policy suspended")
	if len(policy.Status.Conditions) != 1 {
		t.Fatalf("Expected 1 condition (replaced), got %d", len(policy.Status.Conditions))
	}
	if policy.Status.Conditions[0].Reason != "Suspended" {
		t.Errorf("Expected reason Suspended, got %s", policy.Status.Conditions[0].Reason)
	}
}

func TestNodeDrain_DeleteNodeAfterDrain(t *testing.T) {
	ctx := context.Background()

	node := makeReadyNode("node-delete-me", "4000m", "8Gi", nil)

	reconciler, fakeClient := newDrainReconciler(node)

	if err := reconciler.deleteNode(ctx, "node-delete-me"); err != nil {
		t.Fatalf("deleteNode() error = %v", err)
	}

	// Verify node is deleted
	var deleted corev1.Node
	err := fakeClient.Get(ctx, types.NamespacedName{Name: "node-delete-me"}, &deleted)
	if err == nil {
		t.Error("Expected node to be deleted")
	}
}

func TestNodeDrain_Reconcile_NonexistentPolicy(t *testing.T) {
	ctx := context.Background()

	reconciler, _ := newDrainReconciler()

	_, err := reconciler.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "nonexistent"},
	})
	if err != nil {
		t.Fatalf("Expected no error for nonexistent policy, got: %v", err)
	}
}

func TestNodeDrain_IsNodeReady(t *testing.T) {
	readyNode := &corev1.Node{
		Status: corev1.NodeStatus{
			Conditions: []corev1.NodeCondition{
				{Type: corev1.NodeReady, Status: corev1.ConditionTrue},
			},
		},
	}
	if !isNodeReady(readyNode) {
		t.Error("Expected node to be ready")
	}

	notReadyNode := &corev1.Node{
		Status: corev1.NodeStatus{
			Conditions: []corev1.NodeCondition{
				{Type: corev1.NodeReady, Status: corev1.ConditionFalse},
			},
		},
	}
	if isNodeReady(notReadyNode) {
		t.Error("Expected node to not be ready")
	}

	noConditionNode := &corev1.Node{}
	if isNodeReady(noConditionNode) {
		t.Error("Expected node without conditions to not be ready")
	}
}

func TestNodeDrainPolicy_ReconcileInterval(t *testing.T) {
	twoMin := metav1.Duration{Duration: 2 * time.Minute}

	tests := []struct {
		name            string
		specInterval    *metav1.Duration
		defaultInterval time.Duration
		expected        time.Duration
	}{
		{
			name:            "default when nothing configured",
			specInterval:    nil,
			defaultInterval: 0,
			expected:        5 * time.Minute,
		},
		{
			name:            "spec overrides everything",
			specInterval:    &twoMin,
			defaultInterval: 5 * time.Minute,
			expected:        2 * time.Minute,
		},
		{
			name:            "global default used when no spec",
			specInterval:    nil,
			defaultInterval: 30 * time.Second,
			expected:        30 * time.Second,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &NodeDrainPolicyReconciler{
				DefaultReconcileInterval: tt.defaultInterval,
			}
			policy := &slumlordv1alpha1.SlumlordNodeDrainPolicy{
				Spec: slumlordv1alpha1.SlumlordNodeDrainPolicySpec{
					ReconcileInterval: tt.specInterval,
				},
			}
			got := r.reconcileInterval(policy)
			if got != tt.expected {
				t.Errorf("reconcileInterval() = %v, want %v", got, tt.expected)
			}
		})
	}
}
