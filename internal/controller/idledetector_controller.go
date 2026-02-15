package controller

import (
	"context"
	"fmt"
	"path"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	metricsv1beta1 "k8s.io/metrics/pkg/apis/metrics/v1beta1"
	metricsclient "k8s.io/metrics/pkg/client/clientset/versioned"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	slumlordv1alpha1 "github.com/cschockaert/slumlord/api/v1alpha1"
)

const idleDetectorFinalizer = "slumlord.io/idle-detector-finalizer"

// supportedIdleDetectorTypes lists the workload types supported by the idle detector
var supportedIdleDetectorTypes = map[string]bool{
	"Deployment":  true,
	"StatefulSet": true,
	"CronJob":     true,
}

const defaultIdleDetectorInterval = 5 * time.Minute

// IdleDetectorReconciler reconciles a SlumlordIdleDetector object
type IdleDetectorReconciler struct {
	client.Client
	Scheme                   *runtime.Scheme
	MetricsClient            metricsclient.Interface // nil = degraded mode (no metrics)
	DefaultReconcileInterval time.Duration
}

func (r *IdleDetectorReconciler) reconcileInterval(detector *slumlordv1alpha1.SlumlordIdleDetector) time.Duration {
	if detector.Spec.ReconcileInterval != nil {
		return detector.Spec.ReconcileInterval.Duration
	}
	if r.DefaultReconcileInterval > 0 {
		return r.DefaultReconcileInterval
	}
	return defaultIdleDetectorInterval
}

// +kubebuilder:rbac:groups=slumlord.io,resources=slumlordidledetectors,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=slumlord.io,resources=slumlordidledetectors/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=slumlord.io,resources=slumlordidledetectors/finalizers,verbs=update
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=apps,resources=statefulsets,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=batch,resources=cronjobs,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;patch
// +kubebuilder:rbac:groups=metrics.k8s.io,resources=pods,verbs=get;list

// Reconcile handles the reconciliation loop for SlumlordIdleDetector
func (r *IdleDetectorReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Fetch the SlumlordIdleDetector
	var detector slumlordv1alpha1.SlumlordIdleDetector
	if err := r.Get(ctx, req.NamespacedName, &detector); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Handle deletion - restore workloads before removing finalizer
	if !detector.DeletionTimestamp.IsZero() {
		if controllerutil.ContainsFinalizer(&detector, idleDetectorFinalizer) {
			logger.Info("Restoring workloads before deletion")
			if err := r.restoreScaledWorkloads(ctx, &detector); err != nil {
				// Persist partial restore progress before returning error
				if statusErr := r.Status().Update(ctx, &detector); statusErr != nil {
					logger.Error(statusErr, "Failed to persist restore progress")
				}
				return ctrl.Result{}, err
			}

			if err := r.restoreResizedWorkloads(ctx, &detector); err != nil {
				if statusErr := r.Status().Update(ctx, &detector); statusErr != nil {
					logger.Error(statusErr, "Failed to persist resize restore progress")
				}
				return ctrl.Result{}, err
			}

			controllerutil.RemoveFinalizer(&detector, idleDetectorFinalizer)
			if err := r.Update(ctx, &detector); err != nil {
				return ctrl.Result{}, err
			}
		}
		return ctrl.Result{}, nil
	}

	// Add finalizer if not present
	if !controllerutil.ContainsFinalizer(&detector, idleDetectorFinalizer) {
		controllerutil.AddFinalizer(&detector, idleDetectorFinalizer)
		if err := r.Update(ctx, &detector); err != nil {
			return ctrl.Result{}, err
		}
	}

	logger.Info("Reconciling idle detector", "name", detector.Name, "action", detector.Spec.Action)

	// Warn about unsupported types in selector
	for _, t := range detector.Spec.Selector.Types {
		if !supportedIdleDetectorTypes[t] {
			logger.Info("Unsupported workload type for idle detector, skipping", "type", t)
		}
	}

	if detector.Spec.Action == "resize" {
		for _, t := range detector.Spec.Selector.Types {
			if t == "CronJob" {
				logger.Info("CronJob type in selector is ignored for resize action (no long-running pods to resize)")
			}
		}
	}

	if r.MetricsClient == nil {
		logger.V(1).Info("MetricsClient is nil; idle detection running in degraded mode (always returns not-idle)")
	}

	// Parse idle duration
	idleDuration, err := time.ParseDuration(detector.Spec.IdleDuration)
	if err != nil {
		logger.Error(err, "Failed to parse idle duration", "idleDuration", detector.Spec.IdleDuration)
		return ctrl.Result{RequeueAfter: r.reconcileInterval(&detector)}, err
	}

	// Check workloads and detect idle ones
	idleWorkloads, err := r.detectIdleWorkloads(ctx, &detector, idleDuration)
	if err != nil {
		logger.Error(err, "Failed to detect idle workloads")
		return ctrl.Result{RequeueAfter: r.reconcileInterval(&detector)}, err
	}

	// Update idle workloads in status
	detector.Status.IdleWorkloads = idleWorkloads
	now := metav1.Now()
	detector.Status.LastCheckTime = &now

	// If action is "scale", scale down idle workloads that have been idle long enough
	if detector.Spec.Action == "scale" {
		if err := r.scaleDownIdleWorkloads(ctx, &detector, idleDuration); err != nil {
			logger.Error(err, "Failed to scale down idle workloads")
			// Persist any status changes accumulated so far
			if statusErr := r.Status().Update(ctx, &detector); statusErr != nil {
				logger.Error(statusErr, "Failed to persist status after scale error")
			}
			return ctrl.Result{RequeueAfter: r.reconcileInterval(&detector)}, err
		}
	}

	if detector.Spec.Action == "resize" {
		if err := r.resizeIdleWorkloads(ctx, &detector, idleDuration); err != nil {
			logger.Error(err, "Failed to resize idle workloads")
			if statusErr := r.Status().Update(ctx, &detector); statusErr != nil {
				logger.Error(statusErr, "Failed to persist status after resize error")
			}
			return ctrl.Result{RequeueAfter: r.reconcileInterval(&detector)}, err
		}
	}

	// Update status
	if err := r.Status().Update(ctx, &detector); err != nil {
		return ctrl.Result{}, err
	}

	// Requeue to check again in 5 minutes
	return ctrl.Result{RequeueAfter: r.reconcileInterval(&detector)}, nil
}

// matchesSelector checks if a workload name matches the selector's MatchNames patterns
func matchesSelector(name string, selector slumlordv1alpha1.WorkloadSelector) bool {
	// If no MatchNames specified, all workloads match (label filtering already done at List level)
	if len(selector.MatchNames) == 0 {
		return true
	}

	for _, pattern := range selector.MatchNames {
		if matched, err := path.Match(pattern, name); err == nil && matched {
			return true
		}
	}
	return false
}

// listOptions returns the client list options for the detector's selector
func (r *IdleDetectorReconciler) listOptions(detector *slumlordv1alpha1.SlumlordIdleDetector) []client.ListOption {
	opts := []client.ListOption{client.InNamespace(detector.Namespace)}
	if len(detector.Spec.Selector.MatchLabels) > 0 {
		opts = append(opts, client.MatchingLabels(detector.Spec.Selector.MatchLabels))
	}
	return opts
}

// detectIdleWorkloads checks all targeted workloads and returns those that are idle
func (r *IdleDetectorReconciler) detectIdleWorkloads(ctx context.Context, detector *slumlordv1alpha1.SlumlordIdleDetector, idleDuration time.Duration) ([]slumlordv1alpha1.IdleWorkload, error) {
	logger := log.FromContext(ctx)
	var idleWorkloads []slumlordv1alpha1.IdleWorkload
	now := metav1.Now()

	// Handle Deployments
	if r.shouldManageType(detector, "Deployment") {
		var deployments appsv1.DeploymentList
		if err := r.List(ctx, &deployments, r.listOptions(detector)...); err != nil {
			return nil, err
		}

		for _, deploy := range deployments.Items {
			if !matchesSelector(deploy.Name, detector.Spec.Selector) {
				continue
			}

			// Skip already scaled down deployments
			if deploy.Spec.Replicas != nil && *deploy.Spec.Replicas == 0 {
				continue
			}

			isIdle, cpuPercent, memPercent := r.checkWorkloadMetrics(ctx, "Deployment", deploy.Name, deploy.Namespace, detector.Spec.Thresholds)

			if isIdle {
				// Check if already in idle list to preserve IdleSince
				idleSince := now
				for _, existing := range detector.Status.IdleWorkloads {
					if existing.Kind == "Deployment" && existing.Name == deploy.Name {
						idleSince = existing.IdleSince
						break
					}
				}

				idleWorkloads = append(idleWorkloads, slumlordv1alpha1.IdleWorkload{
					Kind:                 "Deployment",
					Name:                 deploy.Name,
					IdleSince:            idleSince,
					CurrentCPUPercent:    cpuPercent,
					CurrentMemoryPercent: memPercent,
				})
				logger.Info("Detected idle deployment", "name", deploy.Name, "idleSince", idleSince)
			}
		}
	}

	// Handle StatefulSets
	if r.shouldManageType(detector, "StatefulSet") {
		var statefulsets appsv1.StatefulSetList
		if err := r.List(ctx, &statefulsets, r.listOptions(detector)...); err != nil {
			return nil, err
		}

		for _, sts := range statefulsets.Items {
			if !matchesSelector(sts.Name, detector.Spec.Selector) {
				continue
			}

			// Skip already scaled down statefulsets
			if sts.Spec.Replicas != nil && *sts.Spec.Replicas == 0 {
				continue
			}

			isIdle, cpuPercent, memPercent := r.checkWorkloadMetrics(ctx, "StatefulSet", sts.Name, sts.Namespace, detector.Spec.Thresholds)

			if isIdle {
				idleSince := now
				for _, existing := range detector.Status.IdleWorkloads {
					if existing.Kind == "StatefulSet" && existing.Name == sts.Name {
						idleSince = existing.IdleSince
						break
					}
				}

				idleWorkloads = append(idleWorkloads, slumlordv1alpha1.IdleWorkload{
					Kind:                 "StatefulSet",
					Name:                 sts.Name,
					IdleSince:            idleSince,
					CurrentCPUPercent:    cpuPercent,
					CurrentMemoryPercent: memPercent,
				})
				logger.Info("Detected idle statefulset", "name", sts.Name, "idleSince", idleSince)
			}
		}
	}

	// Handle CronJobs
	if r.shouldManageType(detector, "CronJob") {
		var cronjobs batchv1.CronJobList
		if err := r.List(ctx, &cronjobs, r.listOptions(detector)...); err != nil {
			return nil, err
		}

		for _, cj := range cronjobs.Items {
			if !matchesSelector(cj.Name, detector.Spec.Selector) {
				continue
			}

			// Skip already suspended cronjobs
			if cj.Spec.Suspend != nil && *cj.Spec.Suspend {
				continue
			}

			isIdle, cpuPercent, memPercent := r.checkCronJobMetrics(ctx, cj.Name, cj.Namespace, detector.Spec.Thresholds)

			if isIdle {
				idleSince := now
				for _, existing := range detector.Status.IdleWorkloads {
					if existing.Kind == "CronJob" && existing.Name == cj.Name {
						idleSince = existing.IdleSince
						break
					}
				}

				idleWorkloads = append(idleWorkloads, slumlordv1alpha1.IdleWorkload{
					Kind:                 "CronJob",
					Name:                 cj.Name,
					IdleSince:            idleSince,
					CurrentCPUPercent:    cpuPercent,
					CurrentMemoryPercent: memPercent,
				})
				logger.Info("Detected idle cronjob", "name", cj.Name, "idleSince", idleSince)
			}
		}
	}

	return idleWorkloads, nil
}

// checkWorkloadMetrics checks if a Deployment/StatefulSet is idle based on resource usage.
// Returns (isIdle, cpuPercent, memPercent). Gracefully returns not-idle when metrics are unavailable.
func (r *IdleDetectorReconciler) checkWorkloadMetrics(ctx context.Context, kind, name, namespace string, thresholds slumlordv1alpha1.IdleThresholds) (bool, *int32, *int32) {
	logger := log.FromContext(ctx)

	if r.MetricsClient == nil || (thresholds.CPUPercent == nil && thresholds.MemoryPercent == nil) {
		return false, nil, nil
	}

	pods, err := r.getPodsForWorkload(ctx, kind, name, namespace)
	if err != nil {
		logger.V(1).Info("Failed to get pods for workload", "kind", kind, "name", name, "error", err)
		return false, nil, nil
	}
	if len(pods) == 0 {
		return false, nil, nil
	}

	podMetricsList, err := r.MetricsClient.MetricsV1beta1().PodMetricses(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		logger.V(1).Info("Failed to list pod metrics", "namespace", namespace, "error", err)
		return false, nil, nil
	}

	cpuPct, memPct, hasData := computeUsagePercent(pods, podMetricsList.Items)
	if !hasData {
		return false, nil, nil
	}

	cpuInt := int32(cpuPct)
	memInt := int32(memPct)
	idle := isIdleByThresholds(cpuPct, memPct, thresholds)
	return idle, &cpuInt, &memInt
}

// checkCronJobMetrics checks if a CronJob is idle by examining its active Jobs' pod metrics.
// Returns not-idle when there are no active Jobs (no data = safe default).
func (r *IdleDetectorReconciler) checkCronJobMetrics(ctx context.Context, name, namespace string, thresholds slumlordv1alpha1.IdleThresholds) (bool, *int32, *int32) {
	logger := log.FromContext(ctx)

	if r.MetricsClient == nil || (thresholds.CPUPercent == nil && thresholds.MemoryPercent == nil) {
		return false, nil, nil
	}

	// List Jobs in namespace owned by this CronJob
	var jobList batchv1.JobList
	if err := r.List(ctx, &jobList, client.InNamespace(namespace)); err != nil {
		logger.V(1).Info("Failed to list jobs", "namespace", namespace, "error", err)
		return false, nil, nil
	}

	// Filter active Jobs owned by this CronJob
	var activePods []corev1.Pod
	for i := range jobList.Items {
		job := &jobList.Items[i]
		if !isOwnedByCronJob(job, name) {
			continue
		}
		// Only consider active Jobs (not completed/failed)
		if job.Status.Active == 0 {
			continue
		}
		// Get running pods for this Job
		selector, err := metav1.LabelSelectorAsSelector(job.Spec.Selector)
		if err != nil {
			continue
		}
		var podList corev1.PodList
		if err := r.List(ctx, &podList, client.InNamespace(namespace), client.MatchingLabelsSelector{Selector: selector}); err != nil {
			continue
		}
		for j := range podList.Items {
			if podList.Items[j].Status.Phase == corev1.PodRunning {
				activePods = append(activePods, podList.Items[j])
			}
		}
	}

	// No active Jobs = no data = not idle (safe default)
	if len(activePods) == 0 {
		return false, nil, nil
	}

	podMetricsList, err := r.MetricsClient.MetricsV1beta1().PodMetricses(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		logger.V(1).Info("Failed to list pod metrics", "namespace", namespace, "error", err)
		return false, nil, nil
	}

	cpuPct, memPct, hasData := computeUsagePercent(activePods, podMetricsList.Items)
	if !hasData {
		return false, nil, nil
	}

	cpuInt := int32(cpuPct)
	memInt := int32(memPct)
	idle := isIdleByThresholds(cpuPct, memPct, thresholds)
	return idle, &cpuInt, &memInt
}

// isOwnedByCronJob checks if a Job is owned by the named CronJob
func isOwnedByCronJob(job *batchv1.Job, cronJobName string) bool {
	for _, ref := range job.OwnerReferences {
		if ref.Kind == "CronJob" && ref.Name == cronJobName {
			return true
		}
	}
	return false
}

// getPodsForWorkload returns running pods for a Deployment or StatefulSet
func (r *IdleDetectorReconciler) getPodsForWorkload(ctx context.Context, kind, name, namespace string) ([]corev1.Pod, error) {
	var selector labels.Selector

	switch kind {
	case "Deployment":
		var deploy appsv1.Deployment
		if err := r.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, &deploy); err != nil {
			return nil, err
		}
		s, err := metav1.LabelSelectorAsSelector(deploy.Spec.Selector)
		if err != nil {
			return nil, err
		}
		selector = s
	case "StatefulSet":
		var sts appsv1.StatefulSet
		if err := r.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, &sts); err != nil {
			return nil, err
		}
		s, err := metav1.LabelSelectorAsSelector(sts.Spec.Selector)
		if err != nil {
			return nil, err
		}
		selector = s
	default:
		return nil, fmt.Errorf("unsupported workload kind: %s", kind)
	}

	var podList corev1.PodList
	if err := r.List(ctx, &podList, client.InNamespace(namespace), client.MatchingLabelsSelector{Selector: selector}); err != nil {
		return nil, err
	}

	var running []corev1.Pod
	for _, pod := range podList.Items {
		if pod.Status.Phase == corev1.PodRunning {
			running = append(running, pod)
		}
	}
	return running, nil
}

// computeUsagePercent aggregates CPU/memory usage across all pods compared to their requests.
// Returns (cpuPercent, memPercent, hasData). hasData is false if no matching metrics or no requests found.
func computeUsagePercent(pods []corev1.Pod, podMetrics []metricsv1beta1.PodMetrics) (float64, float64, bool) {
	// Build a set of pod names for quick lookup
	podNames := make(map[string]bool, len(pods))
	for _, p := range pods {
		podNames[p.Name] = true
	}

	// Aggregate requests from pod specs
	var totalCPUReq, totalMemReq resource.Quantity
	for _, pod := range pods {
		for _, c := range pod.Spec.Containers {
			if req, ok := c.Resources.Requests[corev1.ResourceCPU]; ok {
				totalCPUReq.Add(req)
			}
			if req, ok := c.Resources.Requests[corev1.ResourceMemory]; ok {
				totalMemReq.Add(req)
			}
		}
	}

	// No requests = can't compute percentage
	if totalCPUReq.IsZero() && totalMemReq.IsZero() {
		return 0, 0, false
	}

	// Aggregate actual usage from metrics
	var totalCPUUsage, totalMemUsage resource.Quantity
	matched := false
	for _, pm := range podMetrics {
		if !podNames[pm.Name] {
			continue
		}
		matched = true
		for _, c := range pm.Containers {
			if cpu, ok := c.Usage[corev1.ResourceCPU]; ok {
				totalCPUUsage.Add(cpu)
			}
			if mem, ok := c.Usage[corev1.ResourceMemory]; ok {
				totalMemUsage.Add(mem)
			}
		}
	}

	if !matched {
		return 0, 0, false
	}

	var cpuPct, memPct float64
	if !totalCPUReq.IsZero() {
		cpuPct = float64(totalCPUUsage.MilliValue()) / float64(totalCPUReq.MilliValue()) * 100
	}
	if !totalMemReq.IsZero() {
		memPct = float64(totalMemUsage.Value()) / float64(totalMemReq.Value()) * 100
	}

	return cpuPct, memPct, true
}

// isIdleByThresholds returns true if usage is below the configured thresholds.
// Both CPU and memory must be below their thresholds if both are set.
// If only one is set, only that metric is checked.
// If neither is set, returns false (not idle).
func isIdleByThresholds(cpuPct, memPct float64, thresholds slumlordv1alpha1.IdleThresholds) bool {
	if thresholds.CPUPercent == nil && thresholds.MemoryPercent == nil {
		return false
	}

	if thresholds.CPUPercent != nil && thresholds.MemoryPercent != nil {
		return cpuPct < float64(*thresholds.CPUPercent) && memPct < float64(*thresholds.MemoryPercent)
	}

	if thresholds.CPUPercent != nil {
		return cpuPct < float64(*thresholds.CPUPercent)
	}

	return memPct < float64(*thresholds.MemoryPercent)
}

// scaleDownIdleWorkloads scales down workloads that have been idle for longer than idleDuration.
// After each successful scale-down, status is persisted immediately to avoid losing
// original state if a subsequent operation fails.
func (r *IdleDetectorReconciler) scaleDownIdleWorkloads(ctx context.Context, detector *slumlordv1alpha1.SlumlordIdleDetector, idleDuration time.Duration) error {
	logger := log.FromContext(ctx)
	now := time.Now()

	for _, idle := range detector.Status.IdleWorkloads {
		// Check if idle long enough
		if now.Sub(idle.IdleSince.Time) < idleDuration {
			logger.Info("Workload not idle long enough", "kind", idle.Kind, "name", idle.Name,
				"idleSince", idle.IdleSince, "required", idleDuration)
			continue
		}

		// Check if already scaled
		alreadyScaled := false
		for _, scaled := range detector.Status.ScaledWorkloads {
			if scaled.Kind == idle.Kind && scaled.Name == idle.Name {
				alreadyScaled = true
				break
			}
		}
		if alreadyScaled {
			continue
		}

		// Scale down the workload
		var scaleErr error
		switch idle.Kind {
		case "Deployment":
			scaleErr = r.scaleDownDeployment(ctx, detector, idle.Name)
		case "StatefulSet":
			scaleErr = r.scaleDownStatefulSet(ctx, detector, idle.Name)
		case "CronJob":
			scaleErr = r.suspendCronJob(ctx, detector, idle.Name)
		}

		if scaleErr != nil {
			logger.Error(scaleErr, "Failed to scale down workload", "kind", idle.Kind, "name", idle.Name)
			continue
		}

		// Persist status immediately after each successful scale-down
		// so we don't lose original state if a later operation fails
		if err := r.Status().Update(ctx, detector); err != nil {
			return fmt.Errorf("failed to persist status after scaling %s/%s: %w", idle.Kind, idle.Name, err)
		}
	}

	return nil
}

// scaleDownDeployment scales a deployment to 0 replicas
func (r *IdleDetectorReconciler) scaleDownDeployment(ctx context.Context, detector *slumlordv1alpha1.SlumlordIdleDetector, name string) error {
	logger := log.FromContext(ctx)

	var deploy appsv1.Deployment
	if err := r.Get(ctx, client.ObjectKey{Namespace: detector.Namespace, Name: name}, &deploy); err != nil {
		return err
	}

	if deploy.Spec.Replicas != nil && *deploy.Spec.Replicas > 0 {
		original := *deploy.Spec.Replicas

		zero := int32(0)
		deploy.Spec.Replicas = &zero
		if err := r.Update(ctx, &deploy); err != nil {
			return err
		}

		detector.Status.ScaledWorkloads = append(detector.Status.ScaledWorkloads, slumlordv1alpha1.ScaledWorkload{
			Kind:             "Deployment",
			Name:             name,
			OriginalReplicas: &original,
			ScaledAt:         metav1.Now(),
		})
		logger.Info("Scaled down idle deployment", "name", name, "originalReplicas", original)
	}

	return nil
}

// scaleDownStatefulSet scales a statefulset to 0 replicas
func (r *IdleDetectorReconciler) scaleDownStatefulSet(ctx context.Context, detector *slumlordv1alpha1.SlumlordIdleDetector, name string) error {
	logger := log.FromContext(ctx)

	var sts appsv1.StatefulSet
	if err := r.Get(ctx, client.ObjectKey{Namespace: detector.Namespace, Name: name}, &sts); err != nil {
		return err
	}

	if sts.Spec.Replicas != nil && *sts.Spec.Replicas > 0 {
		original := *sts.Spec.Replicas

		zero := int32(0)
		sts.Spec.Replicas = &zero
		if err := r.Update(ctx, &sts); err != nil {
			return err
		}

		detector.Status.ScaledWorkloads = append(detector.Status.ScaledWorkloads, slumlordv1alpha1.ScaledWorkload{
			Kind:             "StatefulSet",
			Name:             name,
			OriginalReplicas: &original,
			ScaledAt:         metav1.Now(),
		})
		logger.Info("Scaled down idle statefulset", "name", name, "originalReplicas", original)
	}

	return nil
}

// suspendCronJob suspends a cronjob
func (r *IdleDetectorReconciler) suspendCronJob(ctx context.Context, detector *slumlordv1alpha1.SlumlordIdleDetector, name string) error {
	logger := log.FromContext(ctx)

	var cj batchv1.CronJob
	if err := r.Get(ctx, client.ObjectKey{Namespace: detector.Namespace, Name: name}, &cj); err != nil {
		return err
	}

	if cj.Spec.Suspend == nil || !*cj.Spec.Suspend {
		original := false
		if cj.Spec.Suspend != nil {
			original = *cj.Spec.Suspend
		}

		suspend := true
		cj.Spec.Suspend = &suspend
		if err := r.Update(ctx, &cj); err != nil {
			return err
		}

		detector.Status.ScaledWorkloads = append(detector.Status.ScaledWorkloads, slumlordv1alpha1.ScaledWorkload{
			Kind:            "CronJob",
			Name:            name,
			OriginalSuspend: &original,
			ScaledAt:        metav1.Now(),
		})
		logger.Info("Suspended idle cronjob", "name", name)
	}

	return nil
}

// restoreScaledWorkloads restores all workloads that were scaled down.
// Only successfully restored workloads are removed from the status; failed ones
// are retained for retry on the next reconciliation.
func (r *IdleDetectorReconciler) restoreScaledWorkloads(ctx context.Context, detector *slumlordv1alpha1.SlumlordIdleDetector) error {
	logger := log.FromContext(ctx)

	var remaining []slumlordv1alpha1.ScaledWorkload
	var lastErr error

	for _, scaled := range detector.Status.ScaledWorkloads {
		restored := false

		switch scaled.Kind {
		case "Deployment":
			var deploy appsv1.Deployment
			if err := r.Get(ctx, client.ObjectKey{Namespace: detector.Namespace, Name: scaled.Name}, &deploy); err != nil {
				logger.Error(err, "Failed to get deployment for restore", "name", scaled.Name)
				lastErr = err
				remaining = append(remaining, scaled)
				continue
			}
			if scaled.OriginalReplicas != nil {
				deploy.Spec.Replicas = scaled.OriginalReplicas
				if err := r.Update(ctx, &deploy); err != nil {
					logger.Error(err, "Failed to restore deployment", "name", scaled.Name)
					lastErr = err
					remaining = append(remaining, scaled)
					continue
				}
				logger.Info("Restored deployment", "name", scaled.Name, "replicas", *scaled.OriginalReplicas)
			}
			restored = true

		case "StatefulSet":
			var sts appsv1.StatefulSet
			if err := r.Get(ctx, client.ObjectKey{Namespace: detector.Namespace, Name: scaled.Name}, &sts); err != nil {
				logger.Error(err, "Failed to get statefulset for restore", "name", scaled.Name)
				lastErr = err
				remaining = append(remaining, scaled)
				continue
			}
			if scaled.OriginalReplicas != nil {
				sts.Spec.Replicas = scaled.OriginalReplicas
				if err := r.Update(ctx, &sts); err != nil {
					logger.Error(err, "Failed to restore statefulset", "name", scaled.Name)
					lastErr = err
					remaining = append(remaining, scaled)
					continue
				}
				logger.Info("Restored statefulset", "name", scaled.Name, "replicas", *scaled.OriginalReplicas)
			}
			restored = true

		case "CronJob":
			var cj batchv1.CronJob
			if err := r.Get(ctx, client.ObjectKey{Namespace: detector.Namespace, Name: scaled.Name}, &cj); err != nil {
				logger.Error(err, "Failed to get cronjob for restore", "name", scaled.Name)
				lastErr = err
				remaining = append(remaining, scaled)
				continue
			}
			if scaled.OriginalSuspend != nil {
				cj.Spec.Suspend = scaled.OriginalSuspend
				if err := r.Update(ctx, &cj); err != nil {
					logger.Error(err, "Failed to restore cronjob", "name", scaled.Name)
					lastErr = err
					remaining = append(remaining, scaled)
					continue
				}
				logger.Info("Restored cronjob", "name", scaled.Name)
			}
			restored = true
		}

		if !restored {
			remaining = append(remaining, scaled)
		}
	}

	detector.Status.ScaledWorkloads = remaining

	if lastErr != nil {
		return fmt.Errorf("some workloads failed to restore: %w", lastErr)
	}
	return nil
}

// resizeIdleWorkloads resizes pod requests for workloads that have been idle for longer than idleDuration.
// Only Deployments and StatefulSets are resized (CronJobs are skipped).
func (r *IdleDetectorReconciler) resizeIdleWorkloads(ctx context.Context, detector *slumlordv1alpha1.SlumlordIdleDetector, idleDuration time.Duration) error {
	logger := log.FromContext(ctx)
	now := time.Now()

	// Get resize config with defaults
	bufferPercent := int32(25)
	minCPU := resource.MustParse("50m")
	minMem := resource.MustParse("64Mi")
	if detector.Spec.Resize != nil {
		if detector.Spec.Resize.BufferPercent != nil {
			bufferPercent = *detector.Spec.Resize.BufferPercent
		}
		if detector.Spec.Resize.MinRequests != nil {
			if detector.Spec.Resize.MinRequests.CPU != nil {
				minCPU = *detector.Spec.Resize.MinRequests.CPU
			}
			if detector.Spec.Resize.MinRequests.Memory != nil {
				minMem = *detector.Spec.Resize.MinRequests.Memory
			}
		}
	}

	// Resize idle workloads
	for _, idle := range detector.Status.IdleWorkloads {
		// Skip CronJobs — they have no long-running pods to resize
		if idle.Kind == "CronJob" {
			continue
		}

		// Check if idle long enough
		if now.Sub(idle.IdleSince.Time) < idleDuration {
			continue
		}

		// Check if already resized
		alreadyResized := false
		for _, resized := range detector.Status.ResizedWorkloads {
			if resized.Kind == idle.Kind && resized.Name == idle.Name {
				alreadyResized = true
				break
			}
		}
		if alreadyResized {
			continue
		}

		// Get pods for this workload
		pods, err := r.getPodsForWorkload(ctx, idle.Kind, idle.Name, detector.Namespace)
		if err != nil {
			logger.Error(err, "Failed to get pods for resize", "kind", idle.Kind, "name", idle.Name)
			continue
		}
		if len(pods) == 0 {
			continue
		}

		// Get metrics to compute target requests
		podMetricsList, err := r.MetricsClient.MetricsV1beta1().PodMetricses(detector.Namespace).List(ctx, metav1.ListOptions{})
		if err != nil {
			logger.Error(err, "Failed to get pod metrics for resize", "kind", idle.Kind, "name", idle.Name)
			continue
		}

		// Build pod name set
		podNames := make(map[string]bool, len(pods))
		for _, p := range pods {
			podNames[p.Name] = true
		}

		// Capture original requests from the first pod (all pods of a workload have the same spec)
		originalRequests := make(corev1.ResourceList)
		for _, c := range pods[0].Spec.Containers {
			if cpu, ok := c.Resources.Requests[corev1.ResourceCPU]; ok {
				originalRequests[corev1.ResourceCPU] = cpu
			}
			if mem, ok := c.Resources.Requests[corev1.ResourceMemory]; ok {
				originalRequests[corev1.ResourceMemory] = mem
			}
		}

		// Aggregate actual usage from metrics
		var totalCPUUsage, totalMemUsage resource.Quantity
		matchedPods := 0
		for _, pm := range podMetricsList.Items {
			if !podNames[pm.Name] {
				continue
			}
			matchedPods++
			for _, c := range pm.Containers {
				if cpu, ok := c.Usage[corev1.ResourceCPU]; ok {
					totalCPUUsage.Add(cpu)
				}
				if mem, ok := c.Usage[corev1.ResourceMemory]; ok {
					totalMemUsage.Add(mem)
				}
			}
		}

		if matchedPods == 0 {
			logger.V(1).Info("No metrics found for pods, skipping resize", "kind", idle.Kind, "name", idle.Name)
			continue
		}

		// Compute per-pod average usage
		avgCPUMillis := totalCPUUsage.MilliValue() / int64(matchedPods)
		avgMemBytes := totalMemUsage.Value() / int64(matchedPods)

		// Compute target with buffer: target = usage * (1 + bufferPercent/100)
		targetCPUMillis := avgCPUMillis * int64(100+bufferPercent) / 100
		targetMemBytes := avgMemBytes * int64(100+bufferPercent) / 100

		// Apply floor (minRequests)
		if targetCPUMillis < minCPU.MilliValue() {
			targetCPUMillis = minCPU.MilliValue()
		}
		if targetMemBytes < minMem.Value() {
			targetMemBytes = minMem.Value()
		}

		// Cap: never increase beyond original requests
		if origCPU, ok := originalRequests[corev1.ResourceCPU]; ok {
			if targetCPUMillis > origCPU.MilliValue() {
				targetCPUMillis = origCPU.MilliValue()
			}
		}
		if origMem, ok := originalRequests[corev1.ResourceMemory]; ok {
			if targetMemBytes > origMem.Value() {
				targetMemBytes = origMem.Value()
			}
		}

		targetCPU := *resource.NewMilliQuantity(targetCPUMillis, resource.DecimalSI)
		targetMem := *resource.NewQuantity(targetMemBytes, resource.BinarySI)

		// Resize each pod
		resizedCount := int32(0)
		for i := range pods {
			pod := &pods[i]
			needsResize := false

			for _, c := range pod.Spec.Containers {
				if cpu, ok := c.Resources.Requests[corev1.ResourceCPU]; ok && targetCPU.Cmp(cpu) < 0 {
					needsResize = true
					break
				}
				if mem, ok := c.Resources.Requests[corev1.ResourceMemory]; ok && targetMem.Cmp(mem) < 0 {
					needsResize = true
					break
				}
			}

			if !needsResize {
				continue
			}

			patch := client.MergeFrom(pod.DeepCopy())
			for j := range pod.Spec.Containers {
				if pod.Spec.Containers[j].Resources.Requests == nil {
					pod.Spec.Containers[j].Resources.Requests = make(corev1.ResourceList)
				}
				pod.Spec.Containers[j].Resources.Requests[corev1.ResourceCPU] = targetCPU
				pod.Spec.Containers[j].Resources.Requests[corev1.ResourceMemory] = targetMem
				// DO NOT touch Limits
			}
			if err := r.Patch(ctx, pod, patch); err != nil {
				logger.Error(err, "Failed to resize pod", "pod", pod.Name)
				continue
			}
			resizedCount++
		}

		if resizedCount > 0 {
			currentRequests := corev1.ResourceList{
				corev1.ResourceCPU:    targetCPU,
				corev1.ResourceMemory: targetMem,
			}

			detector.Status.ResizedWorkloads = append(detector.Status.ResizedWorkloads, slumlordv1alpha1.ResizedWorkload{
				Kind:             idle.Kind,
				Name:             idle.Name,
				OriginalRequests: originalRequests,
				CurrentRequests:  currentRequests,
				ResizedAt:        metav1.Now(),
				PodCount:         resizedCount,
			})
			logger.Info("Resized idle workload pods", "kind", idle.Kind, "name", idle.Name,
				"pods", resizedCount, "targetCPU", targetCPU.String(), "targetMem", targetMem.String())

			// Persist status immediately after each resize
			if err := r.Status().Update(ctx, detector); err != nil {
				return fmt.Errorf("failed to persist status after resizing %s/%s: %w", idle.Kind, idle.Name, err)
			}
		}
	}

	// Restore workloads that are no longer idle
	if err := r.restoreNoLongerIdleResizedWorkloads(ctx, detector); err != nil {
		return err
	}

	return nil
}

// restoreNoLongerIdleResizedWorkloads restores original requests for workloads that are no longer idle.
func (r *IdleDetectorReconciler) restoreNoLongerIdleResizedWorkloads(ctx context.Context, detector *slumlordv1alpha1.SlumlordIdleDetector) error {
	logger := log.FromContext(ctx)

	// Build set of currently idle workload names
	idleSet := make(map[string]bool)
	for _, idle := range detector.Status.IdleWorkloads {
		idleSet[idle.Kind+"/"+idle.Name] = true
	}

	var remaining []slumlordv1alpha1.ResizedWorkload
	for _, resized := range detector.Status.ResizedWorkloads {
		key := resized.Kind + "/" + resized.Name
		if idleSet[key] {
			// Still idle, keep tracking
			remaining = append(remaining, resized)
			continue
		}

		// No longer idle — restore original requests
		logger.Info("Workload no longer idle, restoring original requests", "kind", resized.Kind, "name", resized.Name)
		if err := r.restorePodRequests(ctx, detector.Namespace, resized); err != nil {
			logger.Error(err, "Failed to restore pod requests", "kind", resized.Kind, "name", resized.Name)
			remaining = append(remaining, resized)
			continue
		}
	}

	detector.Status.ResizedWorkloads = remaining
	if err := r.Status().Update(ctx, detector); err != nil {
		return fmt.Errorf("failed to persist status after restoring resized workloads: %w", err)
	}
	return nil
}

// restorePodRequests restores the original resource requests on all pods of a workload.
func (r *IdleDetectorReconciler) restorePodRequests(ctx context.Context, namespace string, resized slumlordv1alpha1.ResizedWorkload) error {
	logger := log.FromContext(ctx)

	pods, err := r.getPodsForWorkload(ctx, resized.Kind, resized.Name, namespace)
	if err != nil {
		return err
	}

	for i := range pods {
		pod := &pods[i]
		patch := client.MergeFrom(pod.DeepCopy())
		for j := range pod.Spec.Containers {
			if pod.Spec.Containers[j].Resources.Requests == nil {
				pod.Spec.Containers[j].Resources.Requests = make(corev1.ResourceList)
			}
			if cpu, ok := resized.OriginalRequests[corev1.ResourceCPU]; ok {
				pod.Spec.Containers[j].Resources.Requests[corev1.ResourceCPU] = cpu
			}
			if mem, ok := resized.OriginalRequests[corev1.ResourceMemory]; ok {
				pod.Spec.Containers[j].Resources.Requests[corev1.ResourceMemory] = mem
			}
		}
		if err := r.Patch(ctx, pod, patch); err != nil {
			logger.Error(err, "Failed to restore pod requests", "pod", pod.Name)
			return err
		}
	}

	logger.Info("Restored original requests", "kind", resized.Kind, "name", resized.Name, "pods", len(pods))
	return nil
}

// restoreResizedWorkloads restores all workloads that had their pod requests resized.
func (r *IdleDetectorReconciler) restoreResizedWorkloads(ctx context.Context, detector *slumlordv1alpha1.SlumlordIdleDetector) error {
	logger := log.FromContext(ctx)

	var remaining []slumlordv1alpha1.ResizedWorkload
	var lastErr error

	for _, resized := range detector.Status.ResizedWorkloads {
		if err := r.restorePodRequests(ctx, detector.Namespace, resized); err != nil {
			logger.Error(err, "Failed to restore resized workload", "kind", resized.Kind, "name", resized.Name)
			lastErr = err
			remaining = append(remaining, resized)
			continue
		}
	}

	detector.Status.ResizedWorkloads = remaining

	if lastErr != nil {
		return fmt.Errorf("some resized workloads failed to restore: %w", lastErr)
	}
	return nil
}

// shouldManageType checks if the detector should manage a specific workload type
func (r *IdleDetectorReconciler) shouldManageType(detector *slumlordv1alpha1.SlumlordIdleDetector, kind string) bool {
	// If no types specified, manage all supported types
	if len(detector.Spec.Selector.Types) == 0 {
		return true
	}

	for _, t := range detector.Spec.Selector.Types {
		if t == kind {
			return true
		}
	}
	return false
}

// SetupWithManager sets up the controller with the Manager
func (r *IdleDetectorReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&slumlordv1alpha1.SlumlordIdleDetector{}).
		Complete(r)
}
