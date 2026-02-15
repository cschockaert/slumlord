package controller

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/robfig/cron/v3"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	slumlordv1alpha1 "github.com/cschockaert/slumlord/api/v1alpha1"
)

const nodeDrainFinalizer = "slumlord.io/nodedrain-finalizer"

const defaultNodeDrainInterval = 5 * time.Minute

// NodeDrainPolicyReconciler reconciles a SlumlordNodeDrainPolicy object
type NodeDrainPolicyReconciler struct {
	client.Client
	Scheme                   *runtime.Scheme
	Recorder                 events.EventRecorder
	DefaultReconcileInterval time.Duration
}

func (r *NodeDrainPolicyReconciler) reconcileInterval(policy *slumlordv1alpha1.SlumlordNodeDrainPolicy) time.Duration {
	if policy.Spec.ReconcileInterval != nil {
		return policy.Spec.ReconcileInterval.Duration
	}
	if r.DefaultReconcileInterval > 0 {
		return r.DefaultReconcileInterval
	}
	return defaultNodeDrainInterval
}

// drainNodeInfo holds computed utilization data for a node during drain analysis
type drainNodeInfo struct {
	name              string
	cpuAllocatable    resource.Quantity
	memAllocatable    resource.Quantity
	cpuRequests       resource.Quantity
	memRequests       resource.Quantity
	cpuRequestPercent int32
	memRequestPercent int32
	drainablePods     []corev1.Pod
	node              corev1.Node
}

// +kubebuilder:rbac:groups=slumlord.io,resources=slumlordnodedrainpolicies,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=slumlord.io,resources=slumlordnodedrainpolicies/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=slumlord.io,resources=slumlordnodedrainpolicies/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=nodes,verbs=get;list;watch;update;patch;delete
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=pods/eviction,verbs=create
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

// Reconcile handles the reconciliation loop for SlumlordNodeDrainPolicy
func (r *NodeDrainPolicyReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Fetch the policy (cluster-scoped)
	var policy slumlordv1alpha1.SlumlordNodeDrainPolicy
	if err := r.Get(ctx, req.NamespacedName, &policy); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Handle deletion
	if !policy.DeletionTimestamp.IsZero() {
		if controllerutil.ContainsFinalizer(&policy, nodeDrainFinalizer) {
			controllerutil.RemoveFinalizer(&policy, nodeDrainFinalizer)
			if err := r.Update(ctx, &policy); err != nil {
				return ctrl.Result{}, err
			}
		}
		return ctrl.Result{}, nil
	}

	// Add finalizer if not present
	if !controllerutil.ContainsFinalizer(&policy, nodeDrainFinalizer) {
		controllerutil.AddFinalizer(&policy, nodeDrainFinalizer)
		if err := r.Update(ctx, &policy); err != nil {
			return ctrl.Result{}, err
		}
	}

	// Handle suspend
	if policy.Spec.Suspend {
		r.setCondition(&policy, metav1.ConditionFalse, "Suspended", "Policy is suspended")
		if err := r.Status().Update(ctx, &policy); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: r.reconcileInterval(&policy)}, nil
	}

	// Parse cron schedule
	schedule, loc, err := r.parseCronSchedule(&policy)
	if err != nil {
		logger.Error(err, "Failed to parse cron expression", "cron", policy.Spec.Schedule.Cron)
		r.setCondition(&policy, metav1.ConditionFalse, "InvalidCron", err.Error())
		if statusErr := r.Status().Update(ctx, &policy); statusErr != nil {
			return ctrl.Result{}, statusErr
		}
		return ctrl.Result{RequeueAfter: r.reconcileInterval(&policy)}, nil
	}

	now := time.Now().In(loc)
	nextRun := schedule.Next(now)

	// Compute the most recent past tick by checking when the previous period started.
	// We look back from now to find the cron tick we may have missed.
	prevTick := schedule.Next(now.Add(-2 * time.Minute))
	// prevTick is the first tick after (now - 2min). If it's <= now, it's a tick we should have caught.

	// Determine if we should run now.
	shouldRun := false
	if policy.Status.LastRun != nil {
		// Run if prevTick is after the last run and we haven't processed it yet
		if prevTick.After(policy.Status.LastRun.StartTime.Time) && !prevTick.After(now) {
			shouldRun = true
		}
	} else {
		// Never ran — run if there's a recent tick we should have caught
		if !prevTick.After(now) {
			shouldRun = true
		}
	}

	if !shouldRun {
		// Update nextRun in status and requeue
		nextRunTime := metav1.NewTime(nextRun)
		policy.Status.NextRun = &nextRunTime
		r.setCondition(&policy, metav1.ConditionTrue, "Scheduled", fmt.Sprintf("Next run at %s", nextRun.Format(time.RFC3339)))
		if err := r.Status().Update(ctx, &policy); err != nil {
			return ctrl.Result{}, err
		}
		delay := time.Until(nextRun)
		if delay < 0 {
			delay = r.reconcileInterval(&policy)
		}
		return ctrl.Result{RequeueAfter: delay}, nil
	}

	// Execute drain run
	logger.Info("Starting drain run", "policy", policy.Name)
	runStartTime := metav1.Now()

	runStatus := &slumlordv1alpha1.DrainRunStatus{
		StartTime: runStartTime,
	}

	// Analyze nodes
	nodeInfos, err := r.analyzeNodes(ctx, &policy)
	if err != nil {
		logger.Error(err, "Failed to analyze nodes")
		r.setCondition(&policy, metav1.ConditionFalse, "AnalysisFailed", err.Error())
		if statusErr := r.Status().Update(ctx, &policy); statusErr != nil {
			return ctrl.Result{}, statusErr
		}
		return ctrl.Result{RequeueAfter: r.reconcileInterval(&policy)}, err
	}

	runStatus.NodesEvaluated = int32(len(nodeInfos))

	// Find candidates (OR logic on thresholds)
	candidates := r.findDrainCandidates(nodeInfos, &policy)
	runStatus.NodesCandidates = int32(len(candidates))

	// Sort by utilization (lowest first)
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].cpuRequestPercent+candidates[i].memRequestPercent <
			candidates[j].cpuRequestPercent+candidates[j].memRequestPercent
	})

	// Apply safety constraints
	maxNodes := int32(1)
	if policy.Spec.Safety.MaxNodesPerRun != nil {
		maxNodes = *policy.Spec.Safety.MaxNodesPerRun
	}
	minReady := int32(1)
	if policy.Spec.Safety.MinReadyNodes != nil {
		minReady = *policy.Spec.Safety.MinReadyNodes
	}

	// Count ready nodes
	readyCount := int32(0)
	for _, info := range nodeInfos {
		if isNodeReady(&info.node) {
			readyCount++
		}
	}

	maxDrainable := readyCount - minReady
	if maxDrainable < 0 {
		maxDrainable = 0
	}
	if maxNodes < maxDrainable {
		maxDrainable = maxNodes
	}

	// Limit candidates
	if int32(len(candidates)) > maxDrainable {
		candidates = candidates[:maxDrainable]
	}

	dryRun := policy.Spec.Safety.DryRun
	timeoutSeconds := int32(300)
	if policy.Spec.Drain.TimeoutSeconds != nil {
		timeoutSeconds = *policy.Spec.Drain.TimeoutSeconds
	}

	// Process each candidate
	for _, candidate := range candidates {
		result := slumlordv1alpha1.DrainNodeResult{
			Name:                 candidate.name,
			CPURequestPercent:    candidate.cpuRequestPercent,
			MemoryRequestPercent: candidate.memRequestPercent,
		}

		if dryRun {
			result.Action = "dry-run"
			result.Reason = fmt.Sprintf("Would drain node (cpu=%d%%, mem=%d%%)", candidate.cpuRequestPercent, candidate.memRequestPercent)
			result.PodsEvicted = int32(len(candidate.drainablePods))
			runStatus.NodeResults = append(runStatus.NodeResults, result)
			logger.Info("Dry run: would drain node", "node", candidate.name,
				"cpu%", candidate.cpuRequestPercent, "mem%", candidate.memRequestPercent,
				"pods", len(candidate.drainablePods))
			continue
		}

		// Cordon the node
		if err := r.cordonNode(ctx, &candidate.node); err != nil {
			logger.Error(err, "Failed to cordon node", "node", candidate.name)
			result.Action = "failed"
			result.Reason = fmt.Sprintf("cordon failed: %v", err)
			runStatus.NodeResults = append(runStatus.NodeResults, result)
			r.Recorder.Eventf(&policy, nil, corev1.EventTypeWarning, "CordonFailed", "CordonNode", "Failed to cordon node %s: %v", candidate.name, err)
			continue
		}

		r.Recorder.Eventf(&policy, nil, corev1.EventTypeNormal, "DrainStarted", "DrainNode", "Started draining node %s", candidate.name)

		// Evict pods
		podsEvicted := int32(0)
		evictionFailed := false
		for i := range candidate.drainablePods {
			pod := &candidate.drainablePods[i]
			if err := r.evictPodForDrain(ctx, pod, policy.Spec.Drain.GracePeriodSeconds); err != nil {
				if client.IgnoreNotFound(err) == nil {
					continue // pod already gone
				}
				logger.Error(err, "Failed to evict pod", "pod", pod.Name, "namespace", pod.Namespace)
				evictionFailed = true
				break
			}
			podsEvicted++
		}

		result.PodsEvicted = podsEvicted

		if evictionFailed {
			// Wait for drain with timeout, then uncordon if it fails
			if !r.waitForDrain(ctx, candidate.name, time.Duration(timeoutSeconds)*time.Second) {
				logger.Info("Drain timed out, uncordoning", "node", candidate.name)
				if uncordonErr := r.uncordonNode(ctx, candidate.name); uncordonErr != nil {
					logger.Error(uncordonErr, "Failed to uncordon node after timeout", "node", candidate.name)
				}
				result.Action = "failed"
				result.Reason = "eviction failed and drain timed out"
				runStatus.NodeResults = append(runStatus.NodeResults, result)
				r.Recorder.Eventf(&policy, nil, corev1.EventTypeWarning, "DrainFailed", "DrainNode", "Failed to drain node %s: eviction error", candidate.name)
				continue
			}
		}

		// Wait for drain to complete
		if !r.waitForDrain(ctx, candidate.name, time.Duration(timeoutSeconds)*time.Second) {
			logger.Info("Drain timed out, uncordoning", "node", candidate.name)
			if uncordonErr := r.uncordonNode(ctx, candidate.name); uncordonErr != nil {
				logger.Error(uncordonErr, "Failed to uncordon node after timeout", "node", candidate.name)
			}
			result.Action = "failed"
			result.Reason = "drain timed out"
			runStatus.NodeResults = append(runStatus.NodeResults, result)
			r.Recorder.Eventf(&policy, nil, corev1.EventTypeWarning, "DrainFailed", "DrainNode", "Drain timed out for node %s", candidate.name)
			continue
		}

		runStatus.NodesDrained++
		result.Action = "drained"
		result.Reason = fmt.Sprintf("Node drained (cpu=%d%%, mem=%d%%)", candidate.cpuRequestPercent, candidate.memRequestPercent)

		// Delete node if configured
		if policy.Spec.Drain.DeleteNodeAfterDrain {
			if err := r.deleteNode(ctx, candidate.name); err != nil {
				logger.Error(err, "Failed to delete node after drain", "node", candidate.name)
				r.Recorder.Eventf(&policy, nil, corev1.EventTypeWarning, "NodeDeleteFailed", "DeleteNode", "Failed to delete node %s: %v", candidate.name, err)
			} else {
				runStatus.NodesDeleted++
				result.Action = "deleted"
				r.Recorder.Eventf(&policy, nil, corev1.EventTypeNormal, "NodeDeleted", "DeleteNode", "Deleted node %s after drain", candidate.name)
			}
		}

		runStatus.NodeResults = append(runStatus.NodeResults, result)
		r.Recorder.Eventf(&policy, nil, corev1.EventTypeNormal, "DrainCompleted", "DrainNode", "Completed draining node %s (%d pods evicted)", candidate.name, podsEvicted)

		// Persist status after each node for safety
		policy.Status.LastRun = runStatus
		policy.Status.TotalNodesDrained += 1
		if statusErr := r.Status().Update(ctx, &policy); statusErr != nil {
			logger.Error(statusErr, "Failed to persist status after draining node")
		}
	}

	// Finalize run
	completionTime := metav1.Now()
	runStatus.CompletionTime = &completionTime
	policy.Status.LastRun = runStatus

	// Compute next run for status
	nextRunAfter, err := r.computeNextRun(&policy)
	if err == nil {
		nextRunTime := metav1.NewTime(nextRunAfter)
		policy.Status.NextRun = &nextRunTime
	}

	r.setCondition(&policy, metav1.ConditionTrue, "RunCompleted",
		fmt.Sprintf("Drained %d/%d candidate nodes", runStatus.NodesDrained, runStatus.NodesCandidates))

	if err := r.Status().Update(ctx, &policy); err != nil {
		return ctrl.Result{}, err
	}

	// Requeue for next run
	delay := time.Until(nextRunAfter)
	if delay < 0 {
		delay = r.reconcileInterval(&policy)
	}
	return ctrl.Result{RequeueAfter: delay}, nil
}

// parseCronSchedule parses the cron expression and returns the schedule and location
func (r *NodeDrainPolicyReconciler) parseCronSchedule(policy *slumlordv1alpha1.SlumlordNodeDrainPolicy) (cron.Schedule, *time.Location, error) {
	loc := time.UTC
	if policy.Spec.Schedule.Timezone != "" {
		l, err := time.LoadLocation(policy.Spec.Schedule.Timezone)
		if err != nil {
			return nil, nil, fmt.Errorf("invalid timezone %q: %w", policy.Spec.Schedule.Timezone, err)
		}
		loc = l
	}

	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
	schedule, err := parser.Parse(policy.Spec.Schedule.Cron)
	if err != nil {
		return nil, nil, fmt.Errorf("invalid cron expression %q: %w", policy.Spec.Schedule.Cron, err)
	}

	return schedule, loc, nil
}

// computeNextRun calculates the next run time from the cron expression
func (r *NodeDrainPolicyReconciler) computeNextRun(policy *slumlordv1alpha1.SlumlordNodeDrainPolicy) (time.Time, error) {
	schedule, loc, err := r.parseCronSchedule(policy)
	if err != nil {
		return time.Time{}, err
	}
	now := time.Now().In(loc)
	return schedule.Next(now), nil
}

// analyzeNodes lists nodes matching the policy's nodeSelector and computes utilization
func (r *NodeDrainPolicyReconciler) analyzeNodes(ctx context.Context, policy *slumlordv1alpha1.SlumlordNodeDrainPolicy) ([]drainNodeInfo, error) {
	var nodeList corev1.NodeList
	if err := r.List(ctx, &nodeList); err != nil {
		return nil, err
	}

	var podList corev1.PodList
	if err := r.List(ctx, &podList); err != nil {
		return nil, err
	}

	// Build pod-by-node map
	podsByNode := make(map[string][]corev1.Pod)
	for i := range podList.Items {
		pod := &podList.Items[i]
		if pod.Spec.NodeName != "" && pod.Status.Phase == corev1.PodRunning {
			podsByNode[pod.Spec.NodeName] = append(podsByNode[pod.Spec.NodeName], *pod)
		}
	}

	ignoreDaemonSets := true
	if policy.Spec.Drain.IgnoreDaemonSets != nil {
		ignoreDaemonSets = *policy.Spec.Drain.IgnoreDaemonSets
	}

	var infos []drainNodeInfo
	for i := range nodeList.Items {
		node := &nodeList.Items[i]

		// Skip unschedulable (already cordoned) nodes
		if node.Spec.Unschedulable {
			continue
		}

		// Apply nodeSelector filter
		if len(policy.Spec.NodeSelector) > 0 {
			match := true
			for k, v := range policy.Spec.NodeSelector {
				if node.Labels[k] != v {
					match = false
					break
				}
			}
			if !match {
				continue
			}
		}

		cpuAlloc := node.Status.Allocatable[corev1.ResourceCPU]
		memAlloc := node.Status.Allocatable[corev1.ResourceMemory]

		var cpuReqs, memReqs resource.Quantity
		var drainable []corev1.Pod

		pods := podsByNode[node.Name]
		for j := range pods {
			pod := &pods[j]

			// Sum requests for utilization calculation
			podCPU, podMem := podRequests(*pod)
			cpuReqs.Add(podCPU)
			memReqs.Add(podMem)

			// Check if pod is drainable
			if isDrainable(pod, ignoreDaemonSets, policy.Spec.Drain.DeleteEmptyDirData, policy.Spec.Drain.PodSelector) {
				drainable = append(drainable, *pod)
			}
		}

		info := drainNodeInfo{
			name:           node.Name,
			cpuAllocatable: cpuAlloc,
			memAllocatable: memAlloc,
			cpuRequests:    cpuReqs,
			memRequests:    memReqs,
			drainablePods:  drainable,
			node:           *node,
		}

		if cpuAlloc.MilliValue() > 0 {
			info.cpuRequestPercent = int32(cpuReqs.MilliValue() * 100 / cpuAlloc.MilliValue())
		}
		if memAlloc.Value() > 0 {
			info.memRequestPercent = int32(memReqs.Value() * 100 / memAlloc.Value())
		}

		infos = append(infos, info)
	}

	return infos, nil
}

// isDrainable returns true if a pod can be evicted during a drain operation
func isDrainable(pod *corev1.Pod, ignoreDaemonSets, deleteEmptyDirData bool, podSelector map[string]string) bool {
	// Skip mirror pods
	if _, ok := pod.Annotations["kubernetes.io/config.mirror"]; ok {
		return false
	}

	// Skip terminating pods
	if pod.DeletionTimestamp != nil {
		return false
	}

	// Skip DaemonSet pods if configured
	if ignoreDaemonSets {
		for _, ref := range pod.OwnerReferences {
			if ref.Kind == "DaemonSet" {
				return false
			}
		}
	}

	// Skip pods with emptyDir volumes unless deleteEmptyDirData is set
	if !deleteEmptyDirData {
		for _, vol := range pod.Spec.Volumes {
			if vol.EmptyDir != nil {
				return false
			}
		}
	}

	// Apply podSelector filter if set
	if len(podSelector) > 0 {
		for k, v := range podSelector {
			if pod.Labels[k] != v {
				return false
			}
		}
	}

	return true
}

// findDrainCandidates identifies nodes below thresholds using OR logic
func (r *NodeDrainPolicyReconciler) findDrainCandidates(infos []drainNodeInfo, policy *slumlordv1alpha1.SlumlordNodeDrainPolicy) []drainNodeInfo {
	var candidates []drainNodeInfo
	for _, info := range infos {
		isCandidate := false

		if policy.Spec.Thresholds.CPURequestPercent != nil {
			if info.cpuRequestPercent < *policy.Spec.Thresholds.CPURequestPercent {
				isCandidate = true
			}
		}
		if policy.Spec.Thresholds.MemoryRequestPercent != nil {
			if info.memRequestPercent < *policy.Spec.Thresholds.MemoryRequestPercent {
				isCandidate = true
			}
		}

		if isCandidate {
			candidates = append(candidates, info)
		}
	}
	return candidates
}

// cordonNode marks a node as unschedulable
func (r *NodeDrainPolicyReconciler) cordonNode(ctx context.Context, node *corev1.Node) error {
	patch := client.MergeFrom(node.DeepCopy())
	node.Spec.Unschedulable = true
	return r.Patch(ctx, node, patch)
}

// uncordonNode marks a node as schedulable again
func (r *NodeDrainPolicyReconciler) uncordonNode(ctx context.Context, nodeName string) error {
	var node corev1.Node
	if err := r.Get(ctx, client.ObjectKey{Name: nodeName}, &node); err != nil {
		return err
	}
	patch := client.MergeFrom(node.DeepCopy())
	node.Spec.Unschedulable = false
	return r.Patch(ctx, &node, patch)
}

// evictPodForDrain creates an Eviction for a pod with optional grace period
func (r *NodeDrainPolicyReconciler) evictPodForDrain(ctx context.Context, pod *corev1.Pod, gracePeriod *int64) error {
	eviction := &policyv1.Eviction{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pod.Name,
			Namespace: pod.Namespace,
		},
	}
	if gracePeriod != nil {
		eviction.DeleteOptions = &metav1.DeleteOptions{
			GracePeriodSeconds: gracePeriod,
		}
	}
	return r.SubResource("eviction").Create(ctx, &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pod.Name,
			Namespace: pod.Namespace,
		},
	}, eviction)
}

// waitForDrain polls until all non-DaemonSet pods are gone from the node or timeout
func (r *NodeDrainPolicyReconciler) waitForDrain(ctx context.Context, nodeName string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		var podList corev1.PodList
		if err := r.List(ctx, &podList); err != nil {
			return false
		}

		remaining := 0
		for i := range podList.Items {
			pod := &podList.Items[i]
			if pod.Spec.NodeName != nodeName {
				continue
			}
			if pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed {
				continue
			}
			if pod.DeletionTimestamp != nil {
				continue
			}
			// Skip DaemonSet pods — they stay
			isDaemonSet := false
			for _, ref := range pod.OwnerReferences {
				if ref.Kind == "DaemonSet" {
					isDaemonSet = true
					break
				}
			}
			if isDaemonSet {
				continue
			}
			// Skip mirror pods
			if _, ok := pod.Annotations["kubernetes.io/config.mirror"]; ok {
				continue
			}
			remaining++
		}

		if remaining == 0 {
			return true
		}

		select {
		case <-ctx.Done():
			return false
		case <-time.After(2 * time.Second):
		}
	}
	return false
}

// deleteNode removes the node object from the cluster
func (r *NodeDrainPolicyReconciler) deleteNode(ctx context.Context, nodeName string) error {
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: nodeName,
		},
	}
	return r.Delete(ctx, node)
}

// setCondition sets the Ready condition on the policy status
func (r *NodeDrainPolicyReconciler) setCondition(policy *slumlordv1alpha1.SlumlordNodeDrainPolicy, status metav1.ConditionStatus, reason, message string) {
	meta.SetStatusCondition(&policy.Status.Conditions, metav1.Condition{
		Type:               "Ready",
		Status:             status,
		ObservedGeneration: policy.Generation,
		Reason:             reason,
		Message:            message,
	})
}

// isNodeReady checks if a node has the Ready condition set to True
func isNodeReady(node *corev1.Node) bool {
	for _, cond := range node.Status.Conditions {
		if cond.Type == corev1.NodeReady {
			return cond.Status == corev1.ConditionTrue
		}
	}
	return false
}

// SetupWithManager sets up the controller with the Manager
func (r *NodeDrainPolicyReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&slumlordv1alpha1.SlumlordNodeDrainPolicy{}).
		Complete(r)
}
