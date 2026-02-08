package controller

import (
	"context"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	slumlordv1alpha1 "github.com/cschockaert/slumlord/api/v1alpha1"
)

// --- Helper functions ---

func makeNode(name string, cpuAlloc, memAlloc string, taints []corev1.Taint) *corev1.Node {
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:   name,
			Labels: map[string]string{},
		},
		Spec: corev1.NodeSpec{
			Taints: taints,
		},
		Status: corev1.NodeStatus{
			Allocatable: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse(cpuAlloc),
				corev1.ResourceMemory: resource.MustParse(memAlloc),
			},
		},
	}
	return node
}

func makeRunningPod(name, namespace, nodeName, cpuReq, memReq string) *corev1.Pod {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: "apps/v1",
					Kind:       "ReplicaSet",
					Name:       name + "-rs",
				},
			},
		},
		Spec: corev1.PodSpec{
			NodeName: nodeName,
			Containers: []corev1.Container{
				{
					Name:  "app",
					Image: "nginx",
					Resources: corev1.ResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse(cpuReq),
							corev1.ResourceMemory: resource.MustParse(memReq),
						},
					},
				},
			},
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
		},
	}
	return pod
}

func makeDaemonSetPod(name, namespace, nodeName string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: "apps/v1",
					Kind:       "DaemonSet",
					Name:       "some-ds",
				},
			},
		},
		Spec: corev1.PodSpec{
			NodeName: nodeName,
			Containers: []corev1.Container{
				{Name: "app", Image: "nginx"},
			},
		},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}
}

func makeOrphanPod(name, namespace, nodeName string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: corev1.PodSpec{
			NodeName:   nodeName,
			Containers: []corev1.Container{{Name: "app", Image: "nginx"}},
		},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}
}

func makeMirrorPod(name, namespace, nodeName string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Annotations: map[string]string{
				"kubernetes.io/config.mirror": "abc123",
			},
			OwnerReferences: []metav1.OwnerReference{
				{Kind: "Node", Name: nodeName},
			},
		},
		Spec: corev1.PodSpec{
			NodeName:   nodeName,
			Containers: []corev1.Container{{Name: "app", Image: "nginx"}},
		},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}
}

func makeHostPathPod(name, namespace, nodeName string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			OwnerReferences: []metav1.OwnerReference{
				{Kind: "ReplicaSet", Name: name + "-rs"},
			},
		},
		Spec: corev1.PodSpec{
			NodeName:   nodeName,
			Containers: []corev1.Container{{Name: "app", Image: "nginx"}},
			Volumes: []corev1.Volume{
				{
					Name: "data",
					VolumeSource: corev1.VolumeSource{
						HostPath: &corev1.HostPathVolumeSource{
							Path: "/var/data",
						},
					},
				},
			},
		},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}
}

func makeSystemPod(name, nodeName string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "kube-system",
			OwnerReferences: []metav1.OwnerReference{
				{Kind: "ReplicaSet", Name: name + "-rs"},
			},
		},
		Spec: corev1.PodSpec{
			NodeName:   nodeName,
			Containers: []corev1.Container{{Name: "app", Image: "nginx"}},
		},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}
}

func makeBinPacker(name string, action string, cpuThreshold, memThreshold *int32) *slumlordv1alpha1.SlumlordBinPacker {
	return &slumlordv1alpha1.SlumlordBinPacker{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Spec: slumlordv1alpha1.SlumlordBinPackerSpec{
			Action: action,
			Thresholds: slumlordv1alpha1.BinPackerThresholds{
				CPURequestPercent:    cpuThreshold,
				MemoryRequestPercent: memThreshold,
			},
		},
	}
}

// --- Tests ---

func TestBinPacker_AnalyzeNodes_Utilization(t *testing.T) {
	ctx := context.Background()
	scheme := newTestScheme()

	// 3 nodes with different utilization levels
	node1 := makeNode("node-low", "4000m", "8Gi", nil)    // low util
	node2 := makeNode("node-medium", "4000m", "8Gi", nil) // medium util
	node3 := makeNode("node-high", "4000m", "8Gi", nil)   // high util

	// Pods to create different utilization:
	// node-low: 400m CPU (10%), 800Mi mem (10%)
	pod1 := makeRunningPod("pod-low", "default", "node-low", "400m", "800Mi")
	// node-medium: 2000m CPU (50%), 4Gi mem (50%)
	pod2 := makeRunningPod("pod-medium", "default", "node-medium", "2000m", "4Gi")
	// node-high: 3600m CPU (90%), 7200Mi mem (~87.5%)
	pod3 := makeRunningPod("pod-high", "default", "node-high", "3600m", "7200Mi")

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(node1, node2, node3, pod1, pod2, pod3).
		Build()

	reconciler := &BinPackerReconciler{Client: fakeClient, Scheme: scheme}

	packer := makeBinPacker("test", "report", int32Ptr(30), int32Ptr(30))
	infos, err := reconciler.analyzeNodes(ctx, packer)
	if err != nil {
		t.Fatalf("analyzeNodes() error = %v", err)
	}

	if len(infos) != 3 {
		t.Fatalf("Expected 3 nodes, got %d", len(infos))
	}

	// Find each node and verify utilization
	nodeMap := make(map[string]nodeInfo)
	for _, info := range infos {
		nodeMap[info.name] = info
	}

	low := nodeMap["node-low"]
	if low.cpuRequestPercent != 10 {
		t.Errorf("node-low CPU: expected 10%%, got %d%%", low.cpuRequestPercent)
	}

	medium := nodeMap["node-medium"]
	if medium.cpuRequestPercent != 50 {
		t.Errorf("node-medium CPU: expected 50%%, got %d%%", medium.cpuRequestPercent)
	}

	high := nodeMap["node-high"]
	if high.cpuRequestPercent != 90 {
		t.Errorf("node-high CPU: expected 90%%, got %d%%", high.cpuRequestPercent)
	}
}

func TestBinPacker_IsEvictable(t *testing.T) {
	scheme := newTestScheme()

	reconciler := &BinPackerReconciler{Scheme: scheme}
	excludeNS := map[string]bool{"kube-system": true}
	includeNS := map[string]bool{}
	rsMap := map[string]*appsv1.ReplicaSet{}

	tests := []struct {
		name     string
		pod      *corev1.Pod
		expected bool
	}{
		{
			name:     "normal pod is evictable",
			pod:      makeRunningPod("normal", "default", "node-1", "100m", "128Mi"),
			expected: true,
		},
		{
			name:     "DaemonSet pod is not evictable",
			pod:      makeDaemonSetPod("ds-pod", "default", "node-1"),
			expected: false,
		},
		{
			name:     "orphan pod is not evictable",
			pod:      makeOrphanPod("orphan", "default", "node-1"),
			expected: false,
		},
		{
			name:     "system namespace pod is not evictable",
			pod:      makeSystemPod("sys-pod", "node-1"),
			expected: false,
		},
		{
			name:     "mirror pod is not evictable",
			pod:      makeMirrorPod("mirror", "default", "node-1"),
			expected: false,
		},
		{
			name:     "hostPath pod is not evictable",
			pod:      makeHostPathPod("hostpath", "default", "node-1"),
			expected: false,
		},
		{
			name: "terminating pod is not evictable",
			pod: func() *corev1.Pod {
				p := makeRunningPod("terminating", "default", "node-1", "100m", "128Mi")
				now := metav1.Now()
				p.DeletionTimestamp = &now
				return p
			}(),
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := reconciler.isEvictable(tt.pod, excludeNS, includeNS, rsMap)
			if got != tt.expected {
				t.Errorf("isEvictable() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestBinPacker_TolerateTaints(t *testing.T) {
	tests := []struct {
		name        string
		pod         corev1.Pod
		nodeTaints  []corev1.Taint
		expectMatch bool
	}{
		{
			name:        "no taints",
			pod:         corev1.Pod{},
			nodeTaints:  nil,
			expectMatch: true,
		},
		{
			name: "NoSchedule taint without toleration",
			pod:  corev1.Pod{},
			nodeTaints: []corev1.Taint{
				{Key: "dedicated", Value: "gpu", Effect: corev1.TaintEffectNoSchedule},
			},
			expectMatch: false,
		},
		{
			name: "NoSchedule taint with matching toleration",
			pod: corev1.Pod{
				Spec: corev1.PodSpec{
					Tolerations: []corev1.Toleration{
						{Key: "dedicated", Operator: corev1.TolerationOpEqual, Value: "gpu", Effect: corev1.TaintEffectNoSchedule},
					},
				},
			},
			nodeTaints: []corev1.Taint{
				{Key: "dedicated", Value: "gpu", Effect: corev1.TaintEffectNoSchedule},
			},
			expectMatch: true,
		},
		{
			name: "NoSchedule taint with Exists toleration",
			pod: corev1.Pod{
				Spec: corev1.PodSpec{
					Tolerations: []corev1.Toleration{
						{Key: "dedicated", Operator: corev1.TolerationOpExists},
					},
				},
			},
			nodeTaints: []corev1.Taint{
				{Key: "dedicated", Value: "gpu", Effect: corev1.TaintEffectNoSchedule},
			},
			expectMatch: true,
		},
		{
			name: "tolerate-all operator",
			pod: corev1.Pod{
				Spec: corev1.PodSpec{
					Tolerations: []corev1.Toleration{
						{Operator: corev1.TolerationOpExists},
					},
				},
			},
			nodeTaints: []corev1.Taint{
				{Key: "anything", Value: "anything", Effect: corev1.TaintEffectNoSchedule},
				{Key: "other", Value: "other", Effect: corev1.TaintEffectNoExecute},
			},
			expectMatch: true,
		},
		{
			name: "NoExecute taint without toleration",
			pod:  corev1.Pod{},
			nodeTaints: []corev1.Taint{
				{Key: "node.kubernetes.io/not-ready", Effect: corev1.TaintEffectNoExecute},
			},
			expectMatch: false,
		},
		{
			name: "PreferNoSchedule taint is ignored",
			pod:  corev1.Pod{},
			nodeTaints: []corev1.Taint{
				{Key: "prefer-no", Value: "true", Effect: corev1.TaintEffectPreferNoSchedule},
			},
			expectMatch: true,
		},
		{
			name: "Exists with specific Effect matches same effect",
			pod: corev1.Pod{
				Spec: corev1.PodSpec{
					Tolerations: []corev1.Toleration{
						{Key: "dedicated", Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoSchedule},
					},
				},
			},
			nodeTaints: []corev1.Taint{
				{Key: "dedicated", Value: "gpu", Effect: corev1.TaintEffectNoSchedule},
			},
			expectMatch: true,
		},
		{
			name: "Exists with specific Effect does NOT match different effect",
			pod: corev1.Pod{
				Spec: corev1.PodSpec{
					Tolerations: []corev1.Toleration{
						{Key: "dedicated", Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoSchedule},
					},
				},
			},
			nodeTaints: []corev1.Taint{
				{Key: "dedicated", Value: "gpu", Effect: corev1.TaintEffectNoExecute},
			},
			expectMatch: false,
		},
		{
			name: "wrong value in toleration",
			pod: corev1.Pod{
				Spec: corev1.PodSpec{
					Tolerations: []corev1.Toleration{
						{Key: "dedicated", Operator: corev1.TolerationOpEqual, Value: "other", Effect: corev1.TaintEffectNoSchedule},
					},
				},
			},
			nodeTaints: []corev1.Taint{
				{Key: "dedicated", Value: "gpu", Effect: corev1.TaintEffectNoSchedule},
			},
			expectMatch: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			node := corev1.Node{
				Spec: corev1.NodeSpec{Taints: tt.nodeTaints},
			}
			got := toleratesTaints(tt.pod, node)
			if got != tt.expectMatch {
				t.Errorf("toleratesTaints() = %v, want %v", got, tt.expectMatch)
			}
		})
	}
}

func TestBinPacker_Simulation_PodsFitOnTarget(t *testing.T) {
	ctx := context.Background()
	scheme := newTestScheme()

	// node-low: 10% utilized (400m/4000m CPU, 800Mi/8Gi mem)
	// node-medium: 50% utilized (2000m/4000m CPU, 4Gi/8Gi mem)
	// node-full: 95% utilized (3800m/4000m CPU)
	nodeLow := makeNode("node-low", "4000m", "8Gi", nil)
	nodeMedium := makeNode("node-medium", "4000m", "8Gi", nil)
	nodeFull := makeNode("node-full", "4000m", "8Gi", nil)

	podLow := makeRunningPod("pod-low", "default", "node-low", "400m", "800Mi")
	podMedium := makeRunningPod("pod-medium", "default", "node-medium", "2000m", "4Gi")
	podFull := makeRunningPod("pod-full", "default", "node-full", "3800m", "7600Mi")

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(nodeLow, nodeMedium, nodeFull, podLow, podMedium, podFull).
		Build()

	reconciler := &BinPackerReconciler{Client: fakeClient, Scheme: scheme}

	// Threshold: 30% → only node-low is candidate
	packer := makeBinPacker("test", "consolidate", int32Ptr(30), int32Ptr(30))
	packer.Spec.DryRun = true

	infos, err := reconciler.analyzeNodes(ctx, packer)
	if err != nil {
		t.Fatalf("analyzeNodes() error = %v", err)
	}

	candidates := reconciler.findCandidateNodes(infos, packer)
	if len(candidates) != 1 {
		t.Fatalf("Expected 1 candidate, got %d", len(candidates))
	}
	if candidates[0].name != "node-low" {
		t.Errorf("Expected candidate to be node-low, got %s", candidates[0].name)
	}

	plan := reconciler.buildConsolidationPlan(candidates, infos, packer)
	if len(plan) != 1 {
		t.Fatalf("Expected 1 planned eviction, got %d", len(plan))
	}

	// pod-low (400m CPU, 800Mi mem) should fit on node-medium (2000m used of 4000m alloc)
	if plan[0].PodName != "pod-low" {
		t.Errorf("Expected pod-low to be evicted, got %s", plan[0].PodName)
	}
	if plan[0].TargetNode != "node-medium" {
		t.Errorf("Expected target node-medium, got %s", plan[0].TargetNode)
	}
}

func TestBinPacker_ReportMode(t *testing.T) {
	ctx := context.Background()
	scheme := newTestScheme()

	node := makeNode("node-underutil", "4000m", "8Gi", nil)
	pod := makeRunningPod("pod-small", "default", "node-underutil", "200m", "256Mi")

	packer := makeBinPacker("test-report", "report", int32Ptr(30), int32Ptr(30))

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(node, pod, packer).
		WithStatusSubresource(packer).
		Build()

	reconciler := &BinPackerReconciler{Client: fakeClient, Scheme: scheme}

	result, err := reconciler.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Name: packer.Name},
	})
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if result.RequeueAfter != 2*time.Minute {
		t.Errorf("Expected requeue after 2m, got %v", result.RequeueAfter)
	}

	var updated slumlordv1alpha1.SlumlordBinPacker
	if err := fakeClient.Get(ctx, types.NamespacedName{Name: packer.Name}, &updated); err != nil {
		t.Fatalf("Failed to get packer: %v", err)
	}

	if updated.Status.CandidateNodeCount != 1 {
		t.Errorf("Expected 1 candidate, got %d", updated.Status.CandidateNodeCount)
	}
	if len(updated.Status.CandidateNodes) != 1 {
		t.Fatalf("Expected 1 candidate node in status, got %d", len(updated.Status.CandidateNodes))
	}
	if updated.Status.CandidateNodes[0].Name != "node-underutil" {
		t.Errorf("Expected candidate node-underutil, got %s", updated.Status.CandidateNodes[0].Name)
	}
	if updated.Status.PodsEvictedThisCycle != 0 {
		t.Errorf("Expected 0 evictions in report mode, got %d", updated.Status.PodsEvictedThisCycle)
	}
	if updated.Status.LastAnalysisTime == nil {
		t.Error("Expected lastAnalysisTime to be set")
	}
}

func TestBinPacker_DryRun(t *testing.T) {
	ctx := context.Background()
	scheme := newTestScheme()

	nodeLow := makeNode("node-low", "4000m", "8Gi", nil)
	nodeTarget := makeNode("node-target", "4000m", "8Gi", nil)
	podLow := makeRunningPod("pod-low", "default", "node-low", "200m", "256Mi")
	podTarget := makeRunningPod("pod-target", "default", "node-target", "2000m", "4Gi")

	packer := makeBinPacker("test-dryrun", "consolidate", int32Ptr(30), int32Ptr(30))
	packer.Spec.DryRun = true

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(nodeLow, nodeTarget, podLow, podTarget, packer).
		WithStatusSubresource(packer).
		Build()

	reconciler := &BinPackerReconciler{Client: fakeClient, Scheme: scheme}

	_, err := reconciler.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Name: packer.Name},
	})
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	var updated slumlordv1alpha1.SlumlordBinPacker
	if err := fakeClient.Get(ctx, types.NamespacedName{Name: packer.Name}, &updated); err != nil {
		t.Fatalf("Failed to get packer: %v", err)
	}

	// Plan should be built but no evictions
	if len(updated.Status.ConsolidationPlan) == 0 {
		t.Error("Expected consolidation plan to be built in dry run mode")
	}
	if updated.Status.PodsEvictedThisCycle != 0 {
		t.Errorf("Expected 0 evictions in dry run, got %d", updated.Status.PodsEvictedThisCycle)
	}
}

func TestBinPacker_ScheduleWindow_OutsideWindow(t *testing.T) {
	reconciler := &BinPackerReconciler{}

	packer := makeBinPacker("test-schedule", "consolidate", int32Ptr(30), int32Ptr(30))
	// Set a schedule window that is definitely not now (02:00-03:00 UTC)
	packer.Spec.Schedule = &slumlordv1alpha1.SleepWindow{
		Start:    "02:00",
		End:      "03:00",
		Timezone: "UTC",
		Days:     []int{0, 1, 2, 3, 4, 5, 6}, // every day
	}

	// Override time to be outside window (14:00 UTC)
	// We test the isInConsolidationWindow function directly since we can't easily
	// mock time.Now() in the reconciler
	inWindow := reconciler.isInConsolidationWindow(packer, time.Date(2026, 2, 8, 14, 0, 0, 0, time.UTC))
	if inWindow {
		t.Error("Expected to be outside consolidation window at 14:00 UTC")
	}

	inWindow = reconciler.isInConsolidationWindow(packer, time.Date(2026, 2, 8, 2, 30, 0, 0, time.UTC))
	if !inWindow {
		t.Error("Expected to be inside consolidation window at 02:30 UTC")
	}
}

func TestBinPacker_MaxEvictions(t *testing.T) {
	ctx := context.Background()
	scheme := newTestScheme()

	nodeLow := makeNode("node-low", "4000m", "8Gi", nil)
	nodeTarget := makeNode("node-target", "4000m", "8Gi", nil)

	// Multiple pods on the low node (total: 300m/4000m = 7.5% CPU, well below 30%)
	pod1 := makeRunningPod("pod-1", "default", "node-low", "100m", "128Mi")
	pod2 := makeRunningPod("pod-2", "default", "node-low", "100m", "128Mi")
	pod3 := makeRunningPod("pod-3", "default", "node-low", "100m", "128Mi")
	// Target node above threshold (2000m/4000m = 50% CPU, 4Gi/8Gi = 50% mem)
	podTarget := makeRunningPod("pod-target", "default", "node-target", "2000m", "4Gi")

	maxEvictions := int32(2)
	packer := makeBinPacker("test-maxevict", "consolidate", int32Ptr(30), int32Ptr(30))
	packer.Spec.MaxEvictionsPerCycle = &maxEvictions
	packer.Spec.DryRun = true
	packer.Finalizers = []string{binPackerFinalizer}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(nodeLow, nodeTarget, pod1, pod2, pod3, podTarget, packer).
		WithStatusSubresource(packer).
		Build()

	reconciler := &BinPackerReconciler{Client: fakeClient, Scheme: scheme}

	_, err := reconciler.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Name: packer.Name},
	})
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	var updated slumlordv1alpha1.SlumlordBinPacker
	if err := fakeClient.Get(ctx, types.NamespacedName{Name: packer.Name}, &updated); err != nil {
		t.Fatalf("Failed to get packer: %v", err)
	}

	if len(updated.Status.ConsolidationPlan) != 2 {
		t.Errorf("Expected exactly 2 planned evictions (maxEvictions=2), got %d", len(updated.Status.ConsolidationPlan))
	}
}

func TestBinPacker_Finalizer_AddedOnCreate(t *testing.T) {
	ctx := context.Background()
	scheme := newTestScheme()

	packer := makeBinPacker("test-finalizer", "report", int32Ptr(30), int32Ptr(30))

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(packer).
		WithStatusSubresource(packer).
		Build()

	reconciler := &BinPackerReconciler{Client: fakeClient, Scheme: scheme}

	_, err := reconciler.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Name: packer.Name},
	})
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	var updated slumlordv1alpha1.SlumlordBinPacker
	if err := fakeClient.Get(ctx, types.NamespacedName{Name: packer.Name}, &updated); err != nil {
		t.Fatalf("Failed to get packer: %v", err)
	}

	found := false
	for _, f := range updated.Finalizers {
		if f == binPackerFinalizer {
			found = true
			break
		}
	}
	if !found {
		t.Error("Expected finalizer to be added on create")
	}
}

func TestBinPacker_Finalizer_RemovedOnDelete(t *testing.T) {
	ctx := context.Background()
	scheme := newTestScheme()

	now := metav1.Now()
	packer := makeBinPacker("test-delete", "report", int32Ptr(30), int32Ptr(30))
	packer.Finalizers = []string{binPackerFinalizer}
	packer.DeletionTimestamp = &now

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(packer).
		WithStatusSubresource(packer).
		Build()

	reconciler := &BinPackerReconciler{Client: fakeClient, Scheme: scheme}

	_, err := reconciler.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Name: packer.Name},
	})
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	var updated slumlordv1alpha1.SlumlordBinPacker
	err = fakeClient.Get(ctx, types.NamespacedName{Name: packer.Name}, &updated)
	if err == nil {
		for _, f := range updated.Finalizers {
			if f == binPackerFinalizer {
				t.Error("Expected finalizer to be removed on delete")
			}
		}
	}
}

func TestBinPacker_SkipsUnschedulableNodes(t *testing.T) {
	ctx := context.Background()
	scheme := newTestScheme()

	schedulable := makeNode("node-schedulable", "4000m", "8Gi", nil)
	unschedulable := makeNode("node-unschedulable", "4000m", "8Gi", nil)
	unschedulable.Spec.Unschedulable = true

	pod := makeRunningPod("pod-1", "default", "node-schedulable", "200m", "256Mi")

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(schedulable, unschedulable, pod).
		Build()

	reconciler := &BinPackerReconciler{Client: fakeClient, Scheme: scheme}

	packer := makeBinPacker("test", "report", int32Ptr(50), int32Ptr(50))
	infos, err := reconciler.analyzeNodes(ctx, packer)
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

func TestBinPacker_NodeSelectorFilter(t *testing.T) {
	ctx := context.Background()
	scheme := newTestScheme()

	matching := makeNode("node-matching", "4000m", "8Gi", nil)
	matching.Labels["pool"] = "workers"

	nonMatching := makeNode("node-other", "4000m", "8Gi", nil)
	nonMatching.Labels["pool"] = "system"

	pod := makeRunningPod("pod-1", "default", "node-matching", "200m", "256Mi")

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(matching, nonMatching, pod).
		Build()

	reconciler := &BinPackerReconciler{Client: fakeClient, Scheme: scheme}

	packer := makeBinPacker("test", "report", int32Ptr(50), int32Ptr(50))
	packer.Spec.NodeSelector = map[string]string{"pool": "workers"}

	infos, err := reconciler.analyzeNodes(ctx, packer)
	if err != nil {
		t.Fatalf("analyzeNodes() error = %v", err)
	}

	if len(infos) != 1 {
		t.Fatalf("Expected 1 node (matching nodeSelector), got %d", len(infos))
	}
	if infos[0].name != "node-matching" {
		t.Errorf("Expected node-matching, got %s", infos[0].name)
	}
}

func TestBinPacker_NamespaceFilter(t *testing.T) {
	ctx := context.Background()
	scheme := newTestScheme()

	node := makeNode("node-1", "4000m", "8Gi", nil)
	podIncluded := makeRunningPod("pod-included", "staging", "node-1", "200m", "256Mi")
	podExcluded := makeRunningPod("pod-excluded", "production", "node-1", "200m", "256Mi")

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(node, podIncluded, podExcluded).
		Build()

	reconciler := &BinPackerReconciler{Client: fakeClient, Scheme: scheme}

	packer := makeBinPacker("test", "report", int32Ptr(50), int32Ptr(50))
	packer.Spec.Namespaces = []string{"staging"}

	infos, err := reconciler.analyzeNodes(ctx, packer)
	if err != nil {
		t.Fatalf("analyzeNodes() error = %v", err)
	}

	if len(infos) != 1 {
		t.Fatalf("Expected 1 node, got %d", len(infos))
	}

	// Only the staging pod should be evictable
	if len(infos[0].evictablePods) != 1 {
		t.Fatalf("Expected 1 evictable pod (staging only), got %d", len(infos[0].evictablePods))
	}
	if infos[0].evictablePods[0].Name != "pod-included" {
		t.Errorf("Expected pod-included, got %s", infos[0].evictablePods[0].Name)
	}
}

func TestBinPacker_IsInConsolidationWindow_Overnight(t *testing.T) {
	reconciler := &BinPackerReconciler{}

	packer := &slumlordv1alpha1.SlumlordBinPacker{
		Spec: slumlordv1alpha1.SlumlordBinPackerSpec{
			Schedule: &slumlordv1alpha1.SleepWindow{
				Start:    "22:00",
				End:      "06:00",
				Timezone: "UTC",
			},
		},
	}

	tests := []struct {
		name   string
		t      time.Time
		expect bool
	}{
		{"before start", time.Date(2026, 2, 8, 21, 0, 0, 0, time.UTC), false},
		{"at start", time.Date(2026, 2, 8, 22, 1, 0, 0, time.UTC), true},
		{"midnight", time.Date(2026, 2, 9, 0, 0, 0, 0, time.UTC), true},
		{"early morning", time.Date(2026, 2, 9, 3, 0, 0, 0, time.UTC), true},
		{"at end", time.Date(2026, 2, 9, 5, 59, 0, 0, time.UTC), true},
		{"after end", time.Date(2026, 2, 9, 6, 1, 0, 0, time.UTC), false},
		{"afternoon", time.Date(2026, 2, 9, 14, 0, 0, 0, time.UTC), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := reconciler.isInConsolidationWindow(packer, tt.t)
			if got != tt.expect {
				t.Errorf("isInConsolidationWindow() at %v = %v, want %v", tt.t, got, tt.expect)
			}
		})
	}
}

func TestBinPacker_PodRequests(t *testing.T) {
	pod := corev1.Pod{
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Resources: corev1.ResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("200m"),
							corev1.ResourceMemory: resource.MustParse("256Mi"),
						},
					},
				},
				{
					Resources: corev1.ResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("100m"),
							corev1.ResourceMemory: resource.MustParse("128Mi"),
						},
					},
				},
			},
		},
	}

	cpu, mem := podRequests(pod)
	if cpu.MilliValue() != 300 {
		t.Errorf("Expected 300m CPU, got %dm", cpu.MilliValue())
	}
	if mem.Value() != 384*1024*1024 {
		t.Errorf("Expected 384Mi memory, got %d", mem.Value())
	}
}

func TestBinPacker_NoCandidateNodes_HighUtilization(t *testing.T) {
	ctx := context.Background()
	scheme := newTestScheme()

	node := makeNode("node-busy", "4000m", "8Gi", nil)
	pod := makeRunningPod("pod-busy", "default", "node-busy", "3000m", "6Gi")

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(node, pod).
		Build()

	reconciler := &BinPackerReconciler{Client: fakeClient, Scheme: scheme}

	// Threshold at 30% — node at 75% should NOT be a candidate
	packer := makeBinPacker("test", "report", int32Ptr(30), int32Ptr(30))
	infos, err := reconciler.analyzeNodes(ctx, packer)
	if err != nil {
		t.Fatalf("analyzeNodes() error = %v", err)
	}

	candidates := reconciler.findCandidateNodes(infos, packer)
	if len(candidates) != 0 {
		t.Errorf("Expected 0 candidates for high utilization node, got %d", len(candidates))
	}
}

func TestBinPacker_Reconcile_NonexistentPacker(t *testing.T) {
	ctx := context.Background()
	scheme := newTestScheme()

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		Build()

	reconciler := &BinPackerReconciler{Client: fakeClient, Scheme: scheme}

	_, err := reconciler.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "nonexistent"},
	})
	if err != nil {
		t.Fatalf("Expected no error for nonexistent packer, got: %v", err)
	}
}
