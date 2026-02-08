package controller

import (
	"context"
	"fmt"
	"path"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
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

// IdleDetectorReconciler reconciles a SlumlordIdleDetector object
type IdleDetectorReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=slumlord.io,resources=slumlordidledetectors,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=slumlord.io,resources=slumlordidledetectors/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=slumlord.io,resources=slumlordidledetectors/finalizers,verbs=update
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=apps,resources=statefulsets,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=batch,resources=cronjobs,verbs=get;list;watch;update;patch
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

	// Warn that metrics collection is not yet implemented
	logger.V(1).Info("Metrics collection is not yet implemented; idle detection uses stubs that always return not-idle")

	// Parse idle duration
	idleDuration, err := time.ParseDuration(detector.Spec.IdleDuration)
	if err != nil {
		logger.Error(err, "Failed to parse idle duration", "idleDuration", detector.Spec.IdleDuration)
		return ctrl.Result{RequeueAfter: 5 * time.Minute}, err
	}

	// Check workloads and detect idle ones
	idleWorkloads, err := r.detectIdleWorkloads(ctx, &detector, idleDuration)
	if err != nil {
		logger.Error(err, "Failed to detect idle workloads")
		return ctrl.Result{RequeueAfter: 5 * time.Minute}, err
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
			return ctrl.Result{RequeueAfter: 5 * time.Minute}, err
		}
	}

	// Update status
	if err := r.Status().Update(ctx, &detector); err != nil {
		return ctrl.Result{}, err
	}

	// Requeue to check again in 5 minutes
	return ctrl.Result{RequeueAfter: 5 * time.Minute}, nil
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

// checkWorkloadMetrics checks if a workload is idle based on resource usage
// TODO: This is a stub - implement actual metrics collection from metrics-server or Prometheus
func (r *IdleDetectorReconciler) checkWorkloadMetrics(ctx context.Context, kind, name, namespace string, thresholds slumlordv1alpha1.IdleThresholds) (bool, *int32, *int32) {
	// TODO: Implement metrics collection via metrics-server API or Prometheus
	// For now, return false (not idle) to avoid accidental scaling
	return false, nil, nil
}

// checkCronJobMetrics checks if a CronJob is idle
// TODO: This is a stub - implement actual idle detection for CronJobs
func (r *IdleDetectorReconciler) checkCronJobMetrics(ctx context.Context, name, namespace string, thresholds slumlordv1alpha1.IdleThresholds) (bool, *int32, *int32) {
	// TODO: Implement CronJob idle detection
	// For now, return false (not idle) to avoid accidental suspension
	return false, nil, nil
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
