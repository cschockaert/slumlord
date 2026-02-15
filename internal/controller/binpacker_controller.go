package controller

import (
	"context"
	"fmt"
	"sort"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	slumlordv1alpha1 "github.com/cschockaert/slumlord/api/v1alpha1"
)

const binPackerFinalizer = "slumlord.io/binpacker-finalizer"

var defaultExcludeNamespaces = []string{"kube-system", "kube-public", "kube-node-lease"}

const defaultBinPackerInterval = 6 * time.Minute

// BinPackerReconciler reconciles a SlumlordBinPacker object
type BinPackerReconciler struct {
	client.Client
	Scheme                   *runtime.Scheme
	DefaultReconcileInterval time.Duration
}

func (r *BinPackerReconciler) reconcileInterval(packer *slumlordv1alpha1.SlumlordBinPacker) time.Duration {
	if packer.Spec.ReconcileInterval != nil {
		return packer.Spec.ReconcileInterval.Duration
	}
	if r.DefaultReconcileInterval > 0 {
		return r.DefaultReconcileInterval
	}
	return defaultBinPackerInterval
}

// nodeInfo holds computed utilization data for a node
type nodeInfo struct {
	name              string
	cpuAllocatable    resource.Quantity
	memAllocatable    resource.Quantity
	cpuRequests       resource.Quantity
	memRequests       resource.Quantity
	cpuRequestPercent int32
	memRequestPercent int32
	evictablePods     []corev1.Pod
	node              corev1.Node
	scheduledRequests resourcePair // tracks total requests including simulated placements
}

// resourcePair holds CPU and memory resource quantities
type resourcePair struct {
	cpu resource.Quantity
	mem resource.Quantity
}

// workloadRef identifies a replicating controller (Deployment or StatefulSet).
type workloadRef struct {
	Namespace string
	Kind      string // "Deployment" or "StatefulSet"
	Name      string
}

// safetyContext holds pre-computed disruption budgets for all workloads in candidates.
type safetyContext struct {
	podWorkloads     map[string]workloadRef // "ns/podName" → workload
	disruptionBudget map[workloadRef]int32  // remaining budget per workload
}

// resolveWorkload returns the replicating controller for a pod, or nil if not backed by one.
func resolveWorkload(pod *corev1.Pod, rsMap map[string]*appsv1.ReplicaSet) *workloadRef {
	for _, ref := range pod.OwnerReferences {
		switch ref.Kind {
		case "StatefulSet":
			return &workloadRef{Namespace: pod.Namespace, Kind: "StatefulSet", Name: ref.Name}
		case "ReplicaSet":
			rs := rsMap[fmt.Sprintf("%s/%s", pod.Namespace, ref.Name)]
			if rs == nil {
				return nil
			}
			for _, rsRef := range rs.OwnerReferences {
				if rsRef.Kind == "Deployment" {
					return &workloadRef{Namespace: pod.Namespace, Kind: "Deployment", Name: rsRef.Name}
				}
			}
			return nil // bare RS with no Deployment parent
		}
	}
	return nil
}

// +kubebuilder:rbac:groups=slumlord.io,resources=slumlordbinpackers,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=slumlord.io,resources=slumlordbinpackers/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=slumlord.io,resources=slumlordbinpackers/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=nodes,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=pods/eviction,verbs=create
// +kubebuilder:rbac:groups=policy,resources=poddisruptionbudgets,verbs=get;list;watch
// +kubebuilder:rbac:groups=apps,resources=replicasets,verbs=get;list;watch
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch
// +kubebuilder:rbac:groups=apps,resources=statefulsets,verbs=get;list;watch

// Reconcile handles the reconciliation loop for SlumlordBinPacker
func (r *BinPackerReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Fetch the SlumlordBinPacker
	var packer slumlordv1alpha1.SlumlordBinPacker
	if err := r.Get(ctx, req.NamespacedName, &packer); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Handle deletion — no restore needed, just remove finalizer
	if !packer.DeletionTimestamp.IsZero() {
		if controllerutil.ContainsFinalizer(&packer, binPackerFinalizer) {
			controllerutil.RemoveFinalizer(&packer, binPackerFinalizer)
			if err := r.Update(ctx, &packer); err != nil {
				return ctrl.Result{}, err
			}
		}
		return ctrl.Result{}, nil
	}

	// Add finalizer if not present
	if !controllerutil.ContainsFinalizer(&packer, binPackerFinalizer) {
		controllerutil.AddFinalizer(&packer, binPackerFinalizer)
		if err := r.Update(ctx, &packer); err != nil {
			return ctrl.Result{}, err
		}
	}

	logger.Info("Reconciling bin packer", "name", packer.Name, "action", packer.Spec.Action)

	// Check schedule window if set
	if packer.Spec.Schedule != nil {
		if !r.isInConsolidationWindow(&packer, time.Now()) {
			logger.Info("Outside consolidation window, skipping")
			return ctrl.Result{RequeueAfter: r.reconcileInterval(&packer)}, nil
		}
	}

	// Analyze nodes
	nodeInfos, rsMap, err := r.analyzeNodes(ctx, &packer)
	if err != nil {
		logger.Error(err, "Failed to analyze nodes")
		return ctrl.Result{RequeueAfter: r.reconcileInterval(&packer)}, err
	}

	// Identify candidate nodes (below thresholds)
	candidates := r.findCandidateNodes(nodeInfos, &packer)

	// Update status with analysis results
	now := metav1.Now()
	packer.Status.LastAnalysisTime = &now
	packer.Status.NodesAnalyzed = int32(len(nodeInfos))
	packer.Status.CandidateNodes = make([]slumlordv1alpha1.CandidateNode, 0, len(candidates))
	for _, c := range candidates {
		packer.Status.CandidateNodes = append(packer.Status.CandidateNodes, slumlordv1alpha1.CandidateNode{
			Name:                 c.name,
			CPURequestPercent:    c.cpuRequestPercent,
			MemoryRequestPercent: c.memRequestPercent,
			PodCount:             int32(len(c.evictablePods)),
		})
	}
	packer.Status.CandidateNodeCount = int32(len(candidates))

	// If report mode, update status and done
	if packer.Spec.Action == "report" {
		packer.Status.PodsEvictedThisCycle = 0
		packer.Status.ConsolidationPlan = nil
		if err := r.Status().Update(ctx, &packer); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: r.reconcileInterval(&packer)}, nil
	}

	// Build safety context (PDB + replica awareness)
	safety, err := r.buildSafetyContext(ctx, candidates, rsMap)
	if err != nil {
		logger.Error(err, "Failed to build safety context")
		return ctrl.Result{RequeueAfter: r.reconcileInterval(&packer)}, err
	}

	// Build consolidation plan
	plan := r.buildConsolidationPlan(candidates, nodeInfos, &packer, safety)
	packer.Status.ConsolidationPlan = plan

	// If dry run, update status with plan but don't evict
	if packer.Spec.DryRun {
		packer.Status.PodsEvictedThisCycle = 0
		if err := r.Status().Update(ctx, &packer); err != nil {
			return ctrl.Result{}, err
		}
		logger.Info("Dry run mode, plan built but no evictions", "plannedEvictions", len(plan))
		return ctrl.Result{RequeueAfter: r.reconcileInterval(&packer)}, nil
	}

	// Execute plan
	evicted, err := r.executePlan(ctx, &packer, plan)
	if err != nil {
		logger.Error(err, "Failed to execute consolidation plan", "evicted", evicted)
	}

	packer.Status.PodsEvictedThisCycle = int32(evicted)
	packer.Status.TotalPodsEvicted += int64(evicted)
	if evicted > 0 {
		packer.Status.LastConsolidationTime = &now
	}

	if err := r.Status().Update(ctx, &packer); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{RequeueAfter: r.reconcileInterval(&packer)}, nil
}

// isInConsolidationWindow checks if the current time falls within the consolidation schedule.
// Reuses the same timezone/overnight/days logic as SleepScheduleReconciler.
func (r *BinPackerReconciler) isInConsolidationWindow(packer *slumlordv1alpha1.SlumlordBinPacker, t time.Time) bool {
	schedule := packer.Spec.Schedule
	if schedule == nil {
		return true
	}

	loc := time.UTC
	if schedule.Timezone != "" {
		if l, err := time.LoadLocation(schedule.Timezone); err == nil {
			loc = l
		}
	}

	now := t.In(loc)

	startTime, err := time.ParseInLocation("15:04", schedule.Start, loc)
	if err != nil {
		return false
	}
	endTime, err := time.ParseInLocation("15:04", schedule.End, loc)
	if err != nil {
		return false
	}

	startTime = time.Date(now.Year(), now.Month(), now.Day(), startTime.Hour(), startTime.Minute(), 0, 0, loc)
	endTime = time.Date(now.Year(), now.Month(), now.Day(), endTime.Hour(), endTime.Minute(), 0, 0, loc)

	isOvernight := endTime.Before(startTime)

	// Check day filter
	if len(schedule.Days) > 0 {
		checkDay := int(now.Weekday())
		if isOvernight && now.Before(endTime) {
			checkDay = (checkDay + 6) % 7
		}
		dayAllowed := false
		for _, d := range schedule.Days {
			if d == checkDay {
				dayAllowed = true
				break
			}
		}
		if !dayAllowed {
			return false
		}
	}

	if isOvernight {
		return now.After(startTime) || now.Before(endTime)
	}
	return now.After(startTime) && now.Before(endTime)
}

// analyzeNodes lists nodes and computes their utilization based on pod requests.
func (r *BinPackerReconciler) analyzeNodes(ctx context.Context, packer *slumlordv1alpha1.SlumlordBinPacker) ([]nodeInfo, map[string]*appsv1.ReplicaSet, error) {
	// List nodes
	var nodeList corev1.NodeList
	if err := r.List(ctx, &nodeList); err != nil {
		return nil, nil, err
	}

	// List all pods
	var podList corev1.PodList
	if err := r.List(ctx, &podList); err != nil {
		return nil, nil, err
	}

	// Build pod-by-node map
	podsByNode := make(map[string][]corev1.Pod)
	for i := range podList.Items {
		pod := &podList.Items[i]
		if pod.Spec.NodeName != "" && pod.Status.Phase == corev1.PodRunning {
			podsByNode[pod.Spec.NodeName] = append(podsByNode[pod.Spec.NodeName], *pod)
		}
	}

	excludeNS := packer.Spec.ExcludeNamespaces
	if len(excludeNS) == 0 {
		excludeNS = defaultExcludeNamespaces
	}
	excludeNSSet := make(map[string]bool, len(excludeNS))
	for _, ns := range excludeNS {
		excludeNSSet[ns] = true
	}

	includeNSSet := make(map[string]bool, len(packer.Spec.Namespaces))
	for _, ns := range packer.Spec.Namespaces {
		includeNSSet[ns] = true
	}

	// List ReplicaSets for owner resolution
	var rsList appsv1.ReplicaSetList
	if err := r.List(ctx, &rsList); err != nil {
		return nil, nil, err
	}
	rsMap := make(map[string]*appsv1.ReplicaSet)
	for i := range rsList.Items {
		rs := &rsList.Items[i]
		rsMap[fmt.Sprintf("%s/%s", rs.Namespace, rs.Name)] = rs
	}

	var infos []nodeInfo
	for i := range nodeList.Items {
		node := &nodeList.Items[i]

		// Skip unschedulable nodes
		if node.Spec.Unschedulable {
			continue
		}

		// Apply nodeSelector filter
		if len(packer.Spec.NodeSelector) > 0 {
			match := true
			for k, v := range packer.Spec.NodeSelector {
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
		var evictable []corev1.Pod

		pods := podsByNode[node.Name]
		for j := range pods {
			pod := &pods[j]

			// Sum effective pod requests on this node (for utilization calculation)
			podCPU, podMem := podRequests(*pod)
			cpuReqs.Add(podCPU)
			memReqs.Add(podMem)

			// Check if pod is evictable (respecting NS filters)
			if r.isEvictable(pod, excludeNSSet, includeNSSet, rsMap) {
				evictable = append(evictable, *pod)
			}
		}

		info := nodeInfo{
			name:           node.Name,
			cpuAllocatable: cpuAlloc,
			memAllocatable: memAlloc,
			cpuRequests:    cpuReqs,
			memRequests:    memReqs,
			evictablePods:  evictable,
			node:           *node,
		}

		if cpuAlloc.MilliValue() > 0 {
			info.cpuRequestPercent = int32(cpuReqs.MilliValue() * 100 / cpuAlloc.MilliValue())
		}
		if memAlloc.Value() > 0 {
			info.memRequestPercent = int32(memReqs.Value() * 100 / memAlloc.Value())
		}

		// Initialize scheduled requests to current requests
		info.scheduledRequests = resourcePair{
			cpu: cpuReqs.DeepCopy(),
			mem: memReqs.DeepCopy(),
		}

		infos = append(infos, info)
	}

	return infos, rsMap, nil
}

// isEvictable returns true if a pod can be safely evicted.
func (r *BinPackerReconciler) isEvictable(
	pod *corev1.Pod,
	excludeNSSet, includeNSSet map[string]bool,
	rsMap map[string]*appsv1.ReplicaSet,
) bool {
	// Skip pods in excluded namespaces
	if excludeNSSet[pod.Namespace] {
		return false
	}

	// If include namespaces are specified, only include those
	if len(includeNSSet) > 0 && !includeNSSet[pod.Namespace] {
		return false
	}

	// Skip mirror pods (created by kubelet from static manifests)
	if _, ok := pod.Annotations["kubernetes.io/config.mirror"]; ok {
		return false
	}

	// Skip pods with no owner (orphan pods)
	if len(pod.OwnerReferences) == 0 {
		return false
	}

	// Skip DaemonSet pods
	for _, ref := range pod.OwnerReferences {
		if ref.Kind == "DaemonSet" {
			return false
		}
		// Check if owned by a ReplicaSet that is owned by nothing (orphan RS)
		// or owned by a DaemonSet (shouldn't happen, but be safe)
		if ref.Kind == "ReplicaSet" {
			rs := rsMap[fmt.Sprintf("%s/%s", pod.Namespace, ref.Name)]
			if rs != nil {
				for _, rsRef := range rs.OwnerReferences {
					if rsRef.Kind == "DaemonSet" {
						return false
					}
				}
			}
		}
	}

	// Skip pods not backed by a replicating controller (Deployment or StatefulSet)
	if resolveWorkload(pod, rsMap) == nil {
		return false
	}

	// Skip pods with hostPath volumes
	for _, vol := range pod.Spec.Volumes {
		if vol.HostPath != nil {
			return false
		}
	}

	// Skip terminating pods
	if pod.DeletionTimestamp != nil {
		return false
	}

	return true
}

// buildSafetyContext computes disruption budgets for all workloads referenced by candidate pods.
func (r *BinPackerReconciler) buildSafetyContext(ctx context.Context, candidates []nodeInfo, rsMap map[string]*appsv1.ReplicaSet) (*safetyContext, error) {
	safety := &safetyContext{
		podWorkloads:     make(map[string]workloadRef),
		disruptionBudget: make(map[workloadRef]int32),
	}

	// Collect unique workloads from candidate evictable pods
	workloads := make(map[workloadRef]bool)
	for _, c := range candidates {
		for _, pod := range c.evictablePods {
			wl := resolveWorkload(&pod, rsMap)
			if wl == nil {
				continue
			}
			safety.podWorkloads[fmt.Sprintf("%s/%s", pod.Namespace, pod.Name)] = *wl
			workloads[*wl] = true
		}
	}

	if len(workloads) == 0 {
		return safety, nil
	}

	// List PDBs
	var pdbList policyv1.PodDisruptionBudgetList
	if err := r.List(ctx, &pdbList); err != nil {
		return nil, err
	}

	// List Deployments and StatefulSets for replica counts
	var deployList appsv1.DeploymentList
	if err := r.List(ctx, &deployList); err != nil {
		return nil, err
	}
	var stsList appsv1.StatefulSetList
	if err := r.List(ctx, &stsList); err != nil {
		return nil, err
	}

	// Build lookup maps
	deployMap := make(map[workloadRef]*appsv1.Deployment)
	for i := range deployList.Items {
		d := &deployList.Items[i]
		deployMap[workloadRef{Namespace: d.Namespace, Kind: "Deployment", Name: d.Name}] = d
	}
	stsMap := make(map[workloadRef]*appsv1.StatefulSet)
	for i := range stsList.Items {
		s := &stsList.Items[i]
		stsMap[workloadRef{Namespace: s.Namespace, Kind: "StatefulSet", Name: s.Name}] = s
	}

	// For each workload, compute disruption budget
	for wl := range workloads {
		// Get pod template labels for PDB matching
		var podLabels map[string]string
		var readyReplicas int32

		switch wl.Kind {
		case "Deployment":
			d := deployMap[wl]
			if d != nil {
				podLabels = d.Spec.Template.Labels
				readyReplicas = d.Status.ReadyReplicas
			}
		case "StatefulSet":
			s := stsMap[wl]
			if s != nil {
				podLabels = s.Spec.Template.Labels
				readyReplicas = s.Status.ReadyReplicas
			}
		}

		// Match PDBs via label selector
		budget := int32(-1) // -1 means no PDB found yet
		for i := range pdbList.Items {
			pdb := &pdbList.Items[i]
			if pdb.Namespace != wl.Namespace {
				continue
			}
			if pdb.Spec.Selector == nil {
				continue
			}
			sel, err := metav1.LabelSelectorAsSelector(pdb.Spec.Selector)
			if err != nil {
				continue
			}
			if sel.Matches(labels.Set(podLabels)) {
				allowed := pdb.Status.DisruptionsAllowed
				if budget == -1 || allowed < budget {
					budget = allowed
				}
			}
		}

		if budget >= 0 {
			safety.disruptionBudget[wl] = budget
		} else {
			// No PDB: allow eviction only if at least 2 replicas are ready
			safety.disruptionBudget[wl] = readyReplicas - 1
		}
	}

	return safety, nil
}

// findCandidateNodes identifies nodes that are below the configured thresholds.
func (r *BinPackerReconciler) findCandidateNodes(infos []nodeInfo, packer *slumlordv1alpha1.SlumlordBinPacker) []nodeInfo {
	var candidates []nodeInfo
	for _, info := range infos {
		if len(info.evictablePods) == 0 {
			continue
		}

		isCandidate := false
		if packer.Spec.Thresholds.CPURequestPercent != nil && packer.Spec.Thresholds.MemoryRequestPercent != nil {
			isCandidate = info.cpuRequestPercent < *packer.Spec.Thresholds.CPURequestPercent &&
				info.memRequestPercent < *packer.Spec.Thresholds.MemoryRequestPercent
		} else if packer.Spec.Thresholds.CPURequestPercent != nil {
			isCandidate = info.cpuRequestPercent < *packer.Spec.Thresholds.CPURequestPercent
		} else if packer.Spec.Thresholds.MemoryRequestPercent != nil {
			isCandidate = info.memRequestPercent < *packer.Spec.Thresholds.MemoryRequestPercent
		}

		if isCandidate {
			candidates = append(candidates, info)
		}
	}

	// Sort by utilization (lowest first) for better consolidation
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].cpuRequestPercent+candidates[i].memRequestPercent <
			candidates[j].cpuRequestPercent+candidates[j].memRequestPercent
	})

	return candidates
}

// buildConsolidationPlan creates a list of planned evictions.
func (r *BinPackerReconciler) buildConsolidationPlan(
	candidates []nodeInfo,
	allNodes []nodeInfo,
	packer *slumlordv1alpha1.SlumlordBinPacker,
	safety *safetyContext,
) []slumlordv1alpha1.PlannedEviction {
	var plan []slumlordv1alpha1.PlannedEviction

	maxEvictions := int32(0)
	if packer.Spec.MaxEvictionsPerCycle != nil {
		maxEvictions = *packer.Spec.MaxEvictionsPerCycle
	}

	// Build target node list (non-candidate, schedulable nodes)
	candidateSet := make(map[string]bool)
	for _, c := range candidates {
		candidateSet[c.name] = true
	}

	// Use pointers to track mutable state during simulation
	targetNodes := make([]*nodeInfo, 0)
	for i := range allNodes {
		if !candidateSet[allNodes[i].name] {
			targetNodes = append(targetNodes, &allNodes[i])
		}
	}

	for _, candidate := range candidates {
		for _, pod := range candidate.evictablePods {
			if maxEvictions > 0 && int32(len(plan)) >= maxEvictions {
				return plan
			}

			// Check disruption budget
			podKey := fmt.Sprintf("%s/%s", pod.Namespace, pod.Name)
			if wl, ok := safety.podWorkloads[podKey]; ok {
				if safety.disruptionBudget[wl] <= 0 {
					continue
				}
			}

			targetName := r.findTargetNode(pod, targetNodes)
			if targetName == "" {
				continue
			}

			// Decrement budget after planning the eviction
			if wl, ok := safety.podWorkloads[podKey]; ok {
				safety.disruptionBudget[wl]--
			}

			plan = append(plan, slumlordv1alpha1.PlannedEviction{
				PodName:    pod.Name,
				Namespace:  pod.Namespace,
				SourceNode: candidate.name,
				TargetNode: targetName,
				Reason:     fmt.Sprintf("node %s below utilization threshold (cpu=%d%%, mem=%d%%)", candidate.name, candidate.cpuRequestPercent, candidate.memRequestPercent),
			})
		}
	}

	return plan
}

// findTargetNode finds a suitable target node for a pod using simulation.
func (r *BinPackerReconciler) findTargetNode(pod corev1.Pod, targets []*nodeInfo) string {
	podCPU, podMem := podRequests(pod)

	for _, target := range targets {
		if r.canFitOnNode(pod, target, podCPU, podMem) {
			// Update the simulated requests on the target
			target.scheduledRequests.cpu.Add(podCPU)
			target.scheduledRequests.mem.Add(podMem)
			return target.name
		}
	}
	return ""
}

// canFitOnNode checks if a pod can fit on a target node considering requests and taints.
func (r *BinPackerReconciler) canFitOnNode(pod corev1.Pod, target *nodeInfo, podCPU, podMem resource.Quantity) bool {
	// Check resource requests fit
	newCPU := target.scheduledRequests.cpu.DeepCopy()
	newCPU.Add(podCPU)
	if newCPU.Cmp(target.cpuAllocatable) > 0 {
		return false
	}

	newMem := target.scheduledRequests.mem.DeepCopy()
	newMem.Add(podMem)
	if newMem.Cmp(target.memAllocatable) > 0 {
		return false
	}

	// Check nodeSelector match
	if pod.Spec.NodeSelector != nil {
		for k, v := range pod.Spec.NodeSelector {
			if target.node.Labels[k] != v {
				return false
			}
		}
	}

	// Check taint/toleration
	if !toleratesTaints(pod, target.node) {
		return false
	}

	return true
}

// toleratesTaints checks if a pod tolerates all NoSchedule and NoExecute taints on a node.
func toleratesTaints(pod corev1.Pod, node corev1.Node) bool {
	for _, taint := range node.Spec.Taints {
		if taint.Effect != corev1.TaintEffectNoSchedule && taint.Effect != corev1.TaintEffectNoExecute {
			continue
		}
		if !podToleratesTaint(pod, taint) {
			return false
		}
	}
	return true
}

// podToleratesTaint checks if a pod has a toleration that matches a taint.
func podToleratesTaint(pod corev1.Pod, taint corev1.Taint) bool {
	for _, toleration := range pod.Spec.Tolerations {
		if toleration.Operator == corev1.TolerationOpExists && toleration.Key == "" {
			return true // tolerates everything
		}
		if toleration.Key == taint.Key {
			if toleration.Operator == corev1.TolerationOpExists {
				if toleration.Effect == "" || toleration.Effect == taint.Effect {
					return true
				}
				continue
			}
			if toleration.Operator == corev1.TolerationOpEqual && toleration.Value == taint.Value {
				if toleration.Effect == "" || toleration.Effect == taint.Effect {
					return true
				}
			}
			// Default operator is Equal
			if toleration.Operator == "" && toleration.Value == taint.Value {
				if toleration.Effect == "" || toleration.Effect == taint.Effect {
					return true
				}
			}
		}
	}
	return false
}

// podRequests returns the effective CPU and memory requests for a pod.
// The kube-scheduler uses max(sum(initContainers), sum(containers)) per resource.
func podRequests(pod corev1.Pod) (resource.Quantity, resource.Quantity) {
	var cpu, mem resource.Quantity
	for _, c := range pod.Spec.Containers {
		if req, ok := c.Resources.Requests[corev1.ResourceCPU]; ok {
			cpu.Add(req)
		}
		if req, ok := c.Resources.Requests[corev1.ResourceMemory]; ok {
			mem.Add(req)
		}
	}
	// Account for init containers (kube-scheduler takes the max)
	var initCPU, initMem resource.Quantity
	for _, c := range pod.Spec.InitContainers {
		if req, ok := c.Resources.Requests[corev1.ResourceCPU]; ok {
			initCPU.Add(req)
		}
		if req, ok := c.Resources.Requests[corev1.ResourceMemory]; ok {
			initMem.Add(req)
		}
	}
	if initCPU.Cmp(cpu) > 0 {
		cpu = initCPU
	}
	if initMem.Cmp(mem) > 0 {
		mem = initMem
	}
	return cpu, mem
}

// executePlan evicts pods according to the consolidation plan.
func (r *BinPackerReconciler) executePlan(ctx context.Context, packer *slumlordv1alpha1.SlumlordBinPacker, plan []slumlordv1alpha1.PlannedEviction) (int, error) {
	logger := log.FromContext(ctx)
	evicted := 0

	for _, planned := range plan {
		if err := r.evictPod(ctx, planned.PodName, planned.Namespace); err != nil {
			if client.IgnoreNotFound(err) == nil {
				// Pod already gone — treat as success
				logger.Info("Pod already gone, skipping", "pod", planned.PodName, "namespace", planned.Namespace)
				continue
			}
			logger.Error(err, "Failed to evict pod", "pod", planned.PodName, "namespace", planned.Namespace)
			// Persist partial progress for safety (caller handles TotalPodsEvicted)
			packer.Status.PodsEvictedThisCycle = int32(evicted)
			if statusErr := r.Status().Update(ctx, packer); statusErr != nil {
				logger.Error(statusErr, "Failed to persist status after eviction error")
			}
			return evicted, err
		}

		evicted++
		logger.Info("Evicted pod", "pod", planned.PodName, "namespace", planned.Namespace,
			"sourceNode", planned.SourceNode, "targetNode", planned.TargetNode)
	}

	return evicted, nil
}

// evictPod creates an Eviction object for a pod. The API server enforces PDB constraints.
func (r *BinPackerReconciler) evictPod(ctx context.Context, name, namespace string) error {
	eviction := &policyv1.Eviction{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
	}
	return r.SubResource("eviction").Create(ctx, &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
	}, eviction)
}

// SetupWithManager sets up the controller with the Manager
func (r *BinPackerReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&slumlordv1alpha1.SlumlordBinPacker{}).
		Complete(r)
}
