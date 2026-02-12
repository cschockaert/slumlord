package controller

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	crcontroller "sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/log"
	crmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"

	slumlordv1alpha1 "github.com/cschockaert/slumlord/api/v1alpha1"
)

// SleepScheduleReconciler reconciles a SlumlordSleepSchedule object
type SleepScheduleReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder events.EventRecorder
}

var cnpgClusterGVK = schema.GroupVersionKind{
	Group:   "postgresql.cnpg.io",
	Version: "v1",
	Kind:    "Cluster",
}

const hibernationAnnotation = "cnpg.io/hibernation"

var fluxHelmReleaseGVK = schema.GroupVersionKind{
	Group:   "helm.toolkit.fluxcd.io",
	Version: "v2",
	Kind:    "HelmRelease",
}

var fluxKustomizationGVK = schema.GroupVersionKind{
	Group:   "kustomize.toolkit.fluxcd.io",
	Version: "v1",
	Kind:    "Kustomization",
}

var promThanosRulerGVK = schema.GroupVersionKind{
	Group:   "monitoring.coreos.com",
	Version: "v1",
	Kind:    "ThanosRuler",
}

var promAlertmanagerGVK = schema.GroupVersionKind{
	Group:   "monitoring.coreos.com",
	Version: "v1",
	Kind:    "Alertmanager",
}

var promPrometheusGVK = schema.GroupVersionKind{
	Group:   "monitoring.coreos.com",
	Version: "v1",
	Kind:    "Prometheus",
}

var mariadbGVK = schema.GroupVersionKind{
	Group:   "k8s.mariadb.com",
	Version: "v1alpha1",
	Kind:    "MariaDB",
}

var mariadbMaxScaleGVK = schema.GroupVersionKind{
	Group:   "k8s.mariadb.com",
	Version: "v1alpha1",
	Kind:    "MaxScale",
}

// Prometheus metrics
var (
	reconcileActionsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "slumlord_reconcile_actions_total",
			Help: "Total number of reconcile actions performed by the sleep schedule controller",
		},
		[]string{"action"},
	)

	wakeDurationSeconds = prometheus.NewHistogram(
		prometheus.HistogramOpts{
			Name:    "slumlord_wake_duration_seconds",
			Help:    "Duration of wake operations in seconds",
			Buckets: []float64{0.1, 0.5, 1, 2, 5, 10, 30, 60, 120, 300},
		},
	)

	managedWorkloadsGauge = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "slumlord_managed_workloads",
			Help: "Number of workloads managed per sleep schedule",
		},
		[]string{"namespace", "schedule"},
	)
)

func init() {
	crmetrics.Registry.MustRegister(reconcileActionsTotal, wakeDurationSeconds, managedWorkloadsGauge)
}

// Requeue and verification intervals
const (
	idleRequeueInterval        = 5 * time.Minute
	approachingRequeueInterval = 30 * time.Second
	approachingWindow          = 10 * time.Minute
	verifyWindow               = 10 * time.Minute
	verifyRequeueInterval      = 30 * time.Second
)

// +kubebuilder:rbac:groups=slumlord.io,resources=slumlordsleepschedules,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=slumlord.io,resources=slumlordsleepschedules/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=apps,resources=statefulsets,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=batch,resources=cronjobs,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=postgresql.cnpg.io,resources=clusters,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=helm.toolkit.fluxcd.io,resources=helmreleases,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=kustomize.toolkit.fluxcd.io,resources=kustomizations,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=monitoring.coreos.com,resources=thanosrulers;alertmanagers;prometheuses,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=k8s.mariadb.com,resources=mariadbs;maxscales,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

// Reconcile handles the reconciliation loop for SlumlordSleepSchedule
func (r *SleepScheduleReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Fetch the SlumlordSleepSchedule
	var schedule slumlordv1alpha1.SlumlordSleepSchedule
	if err := r.Get(ctx, req.NamespacedName, &schedule); err != nil {
		// Clean up gauge for deleted schedules to prevent cardinality leak
		managedWorkloadsGauge.DeleteLabelValues(req.Namespace, req.Name)
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Handle suspend
	if schedule.Spec.Suspend {
		if schedule.Status.Sleeping {
			logger.Info("Schedule suspended, waking sleeping workloads")
			if err := r.wakeWorkloads(ctx, &schedule); err != nil {
				return ctrl.Result{RequeueAfter: time.Minute}, err
			}
			schedule.Status.Sleeping = false
			ts := metav1.Now()
			schedule.Status.LastTransitionTime = &ts
		}
		r.setScheduleCondition(&schedule, metav1.ConditionFalse, "Suspended", "Schedule is suspended")
		if err := r.Status().Update(ctx, &schedule); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: 1 * time.Minute}, nil
	}

	// Determine if we should be sleeping
	now := time.Now()
	shouldSleep := r.shouldBeSleepingAt(&schedule, now)

	// Compute human-readable days display
	daysDisplay := daysToDisplay(schedule.Spec.Schedule.Days)
	statusChanged := shouldSleep != schedule.Status.Sleeping
	daysChanged := schedule.Status.DaysDisplay != daysDisplay

	// Only log at INFO level when something changes; use V(1) for no-ops
	if statusChanged {
		logger.Info("Reconciling sleep schedule", "shouldSleep", shouldSleep, "currentlySleeping", schedule.Status.Sleeping)
	} else {
		logger.V(1).Info("Reconciling sleep schedule", "shouldSleep", shouldSleep, "currentlySleeping", schedule.Status.Sleeping)
	}

	// If state needs to change, update workloads
	if statusChanged {
		if shouldSleep {
			r.Recorder.Eventf(&schedule, nil, corev1.EventTypeNormal, "SleepStarted", "SleepTransition",
				"Starting sleep transition for schedule %s/%s", schedule.Namespace, schedule.Name)
			reconcileActionsTotal.WithLabelValues("sleep").Inc()

			if err := r.sleepWorkloads(ctx, &schedule); err != nil {
				logger.Error(err, "Failed to sleep workloads")
				return ctrl.Result{RequeueAfter: time.Minute}, err
			}

			r.Recorder.Eventf(&schedule, nil, corev1.EventTypeNormal, "SleepCompleted", "SleepTransition",
				"Completed sleep transition for schedule %s/%s (%d workloads)", schedule.Namespace, schedule.Name, len(schedule.Status.ManagedWorkloads))
		} else {
			r.Recorder.Eventf(&schedule, nil, corev1.EventTypeNormal, "WakeStarted", "WakeTransition",
				"Starting wake transition for schedule %s/%s", schedule.Namespace, schedule.Name)
			reconcileActionsTotal.WithLabelValues("wake").Inc()

			wakeStart := time.Now()
			if err := r.wakeWorkloads(ctx, &schedule); err != nil {
				logger.Error(err, "Failed to wake workloads")
				return ctrl.Result{RequeueAfter: time.Minute}, err
			}
			wakeDurationSeconds.Observe(time.Since(wakeStart).Seconds())

			r.Recorder.Eventf(&schedule, nil, corev1.EventTypeNormal, "WakeCompleted", "WakeTransition",
				"Completed wake transition for schedule %s/%s", schedule.Namespace, schedule.Name)
		}
	} else if !shouldSleep && !schedule.Status.Sleeping {
		// Steady-state awake: check for workload desync within verification window
		if r.shouldVerifyWorkloads(&schedule, now) {
			reconcileActionsTotal.WithLabelValues("verify").Inc()
			if desynced, err := r.verifyAndCorrectWorkloads(ctx, &schedule); err != nil {
				logger.Error(err, "Failed to verify workload state")
				return ctrl.Result{RequeueAfter: verifyRequeueInterval}, err
			} else if desynced {
				logger.Info("Corrected desynced workloads", "schedule", schedule.Name)
				r.Recorder.Eventf(&schedule, nil, corev1.EventTypeWarning, "DesyncCorrected", "WorkloadVerify",
					"Detected and corrected desynced workloads for schedule %s/%s", schedule.Namespace, schedule.Name)
			}
		} else if len(schedule.Status.ManagedWorkloads) > 0 {
			// Verify window expired, clear managed workloads
			schedule.Status.ManagedWorkloads = nil
			if err := r.Status().Update(ctx, &schedule); err != nil {
				return ctrl.Result{}, err
			}
			reconcileActionsTotal.WithLabelValues("noop").Inc()
		} else {
			reconcileActionsTotal.WithLabelValues("noop").Inc()
		}
	} else {
		reconcileActionsTotal.WithLabelValues("noop").Inc()
	}

	// Update managed workloads gauge
	managedWorkloadsGauge.WithLabelValues(schedule.Namespace, schedule.Name).Set(float64(len(schedule.Status.ManagedWorkloads)))

	// Set Ready condition for active schedule
	if shouldSleep {
		r.setScheduleCondition(&schedule, metav1.ConditionTrue, "Sleeping", "Workloads are sleeping")
	} else {
		r.setScheduleCondition(&schedule, metav1.ConditionTrue, "Active", "Schedule is active")
	}

	if statusChanged || daysChanged {
		schedule.Status.DaysDisplay = daysDisplay
		if statusChanged {
			ts := metav1.Now()
			schedule.Status.Sleeping = shouldSleep
			schedule.Status.LastTransitionTime = &ts
		}
	}
	if err := r.Status().Update(ctx, &schedule); err != nil {
		return ctrl.Result{}, err
	}

	// Smart requeue interval
	requeueAfter := r.computeRequeueInterval(&schedule, now)
	return ctrl.Result{RequeueAfter: requeueAfter}, nil
}

// shouldBeSleepingAt determines if workloads should be sleeping at a specific time
func (r *SleepScheduleReconciler) shouldBeSleepingAt(schedule *slumlordv1alpha1.SlumlordSleepSchedule, t time.Time) bool {
	loc := time.UTC
	if schedule.Spec.Schedule.Timezone != "" {
		if l, err := time.LoadLocation(schedule.Spec.Schedule.Timezone); err == nil {
			loc = l
		}
	}

	now := t.In(loc)

	// Parse start and end times first (needed for overnight day-check logic)
	startTime, err := time.ParseInLocation("15:04", schedule.Spec.Schedule.Start, loc)
	if err != nil {
		return false
	}
	endTime, err := time.ParseInLocation("15:04", schedule.Spec.Schedule.End, loc)
	if err != nil {
		return false
	}

	// Set to today's date
	startTime = time.Date(now.Year(), now.Month(), now.Day(), startTime.Hour(), startTime.Minute(), 0, 0, loc)
	endTime = time.Date(now.Year(), now.Month(), now.Day(), endTime.Hour(), endTime.Minute(), 0, 0, loc)

	isOvernight := endTime.Before(startTime)

	// Check if the relevant day is in the allowed days.
	// For overnight schedules, the sleep window belongs to the day it STARTED.
	// So at 3AM Tuesday (early morning portion), we check Monday (previous day).
	if len(schedule.Spec.Schedule.Days) > 0 {
		checkDay := int(now.Weekday())
		if isOvernight && now.Before(endTime) {
			// We're in the early morning portion of an overnight schedule.
			// The sleep started the previous day, so check that day.
			checkDay = (checkDay + 6) % 7 // equivalent to (checkDay - 1 + 7) % 7
		}
		dayAllowed := false
		for _, d := range schedule.Spec.Schedule.Days {
			if d == checkDay {
				dayAllowed = true
				break
			}
		}
		if !dayAllowed {
			return false
		}
	}

	// Handle overnight schedules (e.g., 22:00 to 06:00)
	if isOvernight {
		// We're in overnight mode
		return now.After(startTime) || now.Before(endTime)
	}

	// Normal same-day schedule
	return now.After(startTime) && now.Before(endTime)
}

// shouldVerifyWorkloads returns true if we're within the verification window after the last wake transition.
func (r *SleepScheduleReconciler) shouldVerifyWorkloads(schedule *slumlordv1alpha1.SlumlordSleepSchedule, now time.Time) bool {
	if schedule.Status.LastTransitionTime == nil {
		return false
	}
	if len(schedule.Status.ManagedWorkloads) == 0 {
		return false
	}
	return now.Sub(schedule.Status.LastTransitionTime.Time) <= verifyWindow
}

// verifyAndCorrectWorkloads checks if managed workloads are in the correct (awake) state
// using efficient List calls. Returns true if any correction was made.
func (r *SleepScheduleReconciler) verifyAndCorrectWorkloads(ctx context.Context, schedule *slumlordv1alpha1.SlumlordSleepSchedule) (bool, error) {
	logger := log.FromContext(ctx)

	if len(schedule.Status.ManagedWorkloads) == 0 {
		return false, nil
	}

	// Build a lookup set from ManagedWorkloads
	type workloadKey struct{ kind, name string }
	managedSet := make(map[workloadKey]slumlordv1alpha1.ManagedWorkload, len(schedule.Status.ManagedWorkloads))
	for _, mw := range schedule.Status.ManagedWorkloads {
		managedSet[workloadKey{mw.Kind, mw.Name}] = mw
	}

	desynced := false

	// Check Deployments
	if r.shouldManageType(schedule, "Deployment") {
		var deployments appsv1.DeploymentList
		if err := r.List(ctx, &deployments, client.InNamespace(schedule.Namespace), client.MatchingLabels(schedule.Spec.Selector.MatchLabels)); err != nil {
			return false, err
		}
		for i := range deployments.Items {
			d := &deployments.Items[i]
			if mw, ok := managedSet[workloadKey{"Deployment", d.Name}]; ok {
				if d.Spec.Replicas != nil && *d.Spec.Replicas == 0 && mw.OriginalReplicas != nil && *mw.OriginalReplicas > 0 {
					logger.Info("Deployment desynced after wake", "name", d.Name, "expected", *mw.OriginalReplicas)
					desynced = true
				}
			}
		}
	}

	// Check StatefulSets
	if r.shouldManageType(schedule, "StatefulSet") {
		var statefulsets appsv1.StatefulSetList
		if err := r.List(ctx, &statefulsets, client.InNamespace(schedule.Namespace), client.MatchingLabels(schedule.Spec.Selector.MatchLabels)); err != nil {
			return false, err
		}
		for i := range statefulsets.Items {
			s := &statefulsets.Items[i]
			if mw, ok := managedSet[workloadKey{"StatefulSet", s.Name}]; ok {
				if s.Spec.Replicas != nil && *s.Spec.Replicas == 0 && mw.OriginalReplicas != nil && *mw.OriginalReplicas > 0 {
					logger.Info("StatefulSet desynced after wake", "name", s.Name, "expected", *mw.OriginalReplicas)
					desynced = true
				}
			}
		}
	}

	// Check CronJobs
	if r.shouldManageType(schedule, "CronJob") {
		var cronjobs batchv1.CronJobList
		if err := r.List(ctx, &cronjobs, client.InNamespace(schedule.Namespace), client.MatchingLabels(schedule.Spec.Selector.MatchLabels)); err != nil {
			return false, err
		}
		for i := range cronjobs.Items {
			cj := &cronjobs.Items[i]
			if mw, ok := managedSet[workloadKey{"CronJob", cj.Name}]; ok {
				if cj.Spec.Suspend != nil && *cj.Spec.Suspend && mw.OriginalSuspend != nil && !*mw.OriginalSuspend {
					logger.Info("CronJob desynced after wake", "name", cj.Name)
					desynced = true
				}
			}
		}
	}

	// Check suspend-based CRDs (FluxCD, MariaDB)
	for _, check := range []struct {
		kind string
		gvk  schema.GroupVersionKind
	}{
		{"HelmRelease", fluxHelmReleaseGVK},
		{"Kustomization", fluxKustomizationGVK},
		{"MariaDB", mariadbGVK},
		{"MaxScale", mariadbMaxScaleGVK},
	} {
		if !r.shouldManageType(schedule, check.kind) {
			continue
		}
		resourceList := &unstructured.UnstructuredList{}
		resourceList.SetGroupVersionKind(schema.GroupVersionKind{
			Group: check.gvk.Group, Version: check.gvk.Version, Kind: check.gvk.Kind + "List",
		})
		if err := r.List(ctx, resourceList, client.InNamespace(schedule.Namespace), client.MatchingLabels(schedule.Spec.Selector.MatchLabels)); err != nil {
			if apimeta.IsNoMatchError(err) {
				continue
			}
			return false, err
		}
		for i := range resourceList.Items {
			res := &resourceList.Items[i]
			if mw, ok := managedSet[workloadKey{check.kind, res.GetName()}]; ok {
				spec, _ := res.Object["spec"].(map[string]interface{})
				if spec != nil {
					if suspended, ok := spec["suspend"].(bool); ok && suspended && mw.OriginalSuspend != nil && !*mw.OriginalSuspend {
						logger.Info("Resource desynced (still suspended)", "kind", check.kind, "name", res.GetName())
						desynced = true
					}
				}
			}
		}
	}

	// Check CNPG Clusters
	if r.shouldManageType(schedule, "Cluster") {
		clusterList := &unstructured.UnstructuredList{}
		clusterList.SetGroupVersionKind(schema.GroupVersionKind{
			Group: cnpgClusterGVK.Group, Version: cnpgClusterGVK.Version, Kind: "ClusterList",
		})
		if err := r.List(ctx, clusterList, client.InNamespace(schedule.Namespace), client.MatchingLabels(schedule.Spec.Selector.MatchLabels)); err != nil {
			if !apimeta.IsNoMatchError(err) {
				return false, err
			}
		} else {
			for i := range clusterList.Items {
				c := &clusterList.Items[i]
				if _, ok := managedSet[workloadKey{"Cluster", c.GetName()}]; ok {
					annotations := c.GetAnnotations()
					if annotations != nil && annotations[hibernationAnnotation] == "on" {
						logger.Info("CNPG Cluster desynced (still hibernated)", "name", c.GetName())
						desynced = true
					}
				}
			}
		}
	}

	// Check replica-based CRDs (Prometheus Operator)
	for _, check := range []struct {
		kind string
		gvk  schema.GroupVersionKind
	}{
		{"ThanosRuler", promThanosRulerGVK},
		{"Alertmanager", promAlertmanagerGVK},
		{"Prometheus", promPrometheusGVK},
	} {
		if !r.shouldManageType(schedule, check.kind) {
			continue
		}
		resourceList := &unstructured.UnstructuredList{}
		resourceList.SetGroupVersionKind(schema.GroupVersionKind{
			Group: check.gvk.Group, Version: check.gvk.Version, Kind: check.gvk.Kind + "List",
		})
		if err := r.List(ctx, resourceList, client.InNamespace(schedule.Namespace), client.MatchingLabels(schedule.Spec.Selector.MatchLabels)); err != nil {
			if apimeta.IsNoMatchError(err) {
				continue
			}
			return false, err
		}
		for i := range resourceList.Items {
			res := &resourceList.Items[i]
			if mw, ok := managedSet[workloadKey{check.kind, res.GetName()}]; ok {
				spec, _ := res.Object["spec"].(map[string]interface{})
				if spec != nil {
					var currentReplicas int64
					switch v := spec["replicas"].(type) {
					case int64:
						currentReplicas = v
					case float64:
						currentReplicas = int64(v)
					}
					if currentReplicas == 0 && mw.OriginalReplicas != nil && *mw.OriginalReplicas > 0 {
						logger.Info("Resource desynced (still at 0 replicas)", "kind", check.kind, "name", res.GetName())
						desynced = true
					}
				}
			}
		}
	}

	if desynced {
		logger.Info("Re-running wake to correct desynced workloads", "schedule", schedule.Name)
		if err := r.wakeWorkloads(ctx, schedule); err != nil {
			return false, err
		}
		return true, nil
	}

	return false, nil
}

// computeRequeueInterval returns the optimal requeue duration based on the schedule state.
func (r *SleepScheduleReconciler) computeRequeueInterval(schedule *slumlordv1alpha1.SlumlordSleepSchedule, now time.Time) time.Duration {
	// During verification window, requeue quickly to detect desync
	if !schedule.Status.Sleeping && r.shouldVerifyWorkloads(schedule, now) {
		return verifyRequeueInterval
	}

	// Check time until next transition
	next := r.timeUntilNextTransition(schedule, now)
	if next > 0 && next <= approachingWindow {
		return approachingRequeueInterval
	}

	return idleRequeueInterval
}

// timeUntilNextTransition calculates the duration until the next sleep/wake boundary.
// Returns 0 if no transition is found within 24 hours.
func (r *SleepScheduleReconciler) timeUntilNextTransition(schedule *slumlordv1alpha1.SlumlordSleepSchedule, now time.Time) time.Duration {
	currentState := r.shouldBeSleepingAt(schedule, now)

	// Probe every minute for the next 24 hours to find when state changes.
	// This is O(1440) pure computation with no I/O.
	for i := 1; i <= 1440; i++ {
		probe := now.Add(time.Duration(i) * time.Minute)
		if r.shouldBeSleepingAt(schedule, probe) != currentState {
			return time.Duration(i) * time.Minute
		}
	}

	return 0
}

// sleepWorkloads scales down or suspends matched workloads
func (r *SleepScheduleReconciler) sleepWorkloads(ctx context.Context, schedule *slumlordv1alpha1.SlumlordSleepSchedule) error {
	logger := log.FromContext(ctx)
	schedule.Status.ManagedWorkloads = nil

	// Suspend operator-managed resources BEFORE scaling workloads to prevent reconciliation
	if r.shouldManageType(schedule, "HelmRelease") {
		if err := r.sleepSuspendResources(ctx, schedule, fluxHelmReleaseGVK, "HelmRelease"); err != nil {
			return err
		}
	}
	if r.shouldManageType(schedule, "Kustomization") {
		if err := r.sleepSuspendResources(ctx, schedule, fluxKustomizationGVK, "Kustomization"); err != nil {
			return err
		}
	}
	if r.shouldManageType(schedule, "MariaDB") {
		if err := r.sleepSuspendResources(ctx, schedule, mariadbGVK, "MariaDB"); err != nil {
			return err
		}
	}
	if r.shouldManageType(schedule, "MaxScale") {
		if err := r.sleepSuspendResources(ctx, schedule, mariadbMaxScaleGVK, "MaxScale"); err != nil {
			return err
		}
	}

	// Handle Deployments
	if r.shouldManageType(schedule, "Deployment") {
		var deployments appsv1.DeploymentList
		if err := r.List(ctx, &deployments, client.InNamespace(schedule.Namespace), client.MatchingLabels(schedule.Spec.Selector.MatchLabels)); err != nil {
			return err
		}

		for i := range deployments.Items {
			deploy := &deployments.Items[i]
			if deploy.Spec.Replicas != nil && *deploy.Spec.Replicas > 0 {
				original := *deploy.Spec.Replicas
				schedule.Status.ManagedWorkloads = append(schedule.Status.ManagedWorkloads, slumlordv1alpha1.ManagedWorkload{
					Kind:             "Deployment",
					Name:             deploy.Name,
					OriginalReplicas: &original,
				})

				zero := int32(0)
				deploy.Spec.Replicas = &zero
				if err := r.Update(ctx, deploy); err != nil {
					return err
				}
				logger.Info("Scaled down deployment", "name", deploy.Name, "originalReplicas", original)
			}
		}
	}

	// Handle StatefulSets
	if r.shouldManageType(schedule, "StatefulSet") {
		var statefulsets appsv1.StatefulSetList
		if err := r.List(ctx, &statefulsets, client.InNamespace(schedule.Namespace), client.MatchingLabels(schedule.Spec.Selector.MatchLabels)); err != nil {
			return err
		}

		for i := range statefulsets.Items {
			sts := &statefulsets.Items[i]
			if sts.Spec.Replicas != nil && *sts.Spec.Replicas > 0 {
				original := *sts.Spec.Replicas
				schedule.Status.ManagedWorkloads = append(schedule.Status.ManagedWorkloads, slumlordv1alpha1.ManagedWorkload{
					Kind:             "StatefulSet",
					Name:             sts.Name,
					OriginalReplicas: &original,
				})

				zero := int32(0)
				sts.Spec.Replicas = &zero
				if err := r.Update(ctx, sts); err != nil {
					return err
				}
				logger.Info("Scaled down statefulset", "name", sts.Name, "originalReplicas", original)
			}
		}
	}

	// Handle CronJobs
	if r.shouldManageType(schedule, "CronJob") {
		var cronjobs batchv1.CronJobList
		if err := r.List(ctx, &cronjobs, client.InNamespace(schedule.Namespace), client.MatchingLabels(schedule.Spec.Selector.MatchLabels)); err != nil {
			return err
		}

		for i := range cronjobs.Items {
			cj := &cronjobs.Items[i]
			if cj.Spec.Suspend == nil || !*cj.Spec.Suspend {
				original := false
				if cj.Spec.Suspend != nil {
					original = *cj.Spec.Suspend
				}
				schedule.Status.ManagedWorkloads = append(schedule.Status.ManagedWorkloads, slumlordv1alpha1.ManagedWorkload{
					Kind:            "CronJob",
					Name:            cj.Name,
					OriginalSuspend: &original,
				})

				suspend := true
				cj.Spec.Suspend = &suspend
				if err := r.Update(ctx, cj); err != nil {
					return err
				}
				logger.Info("Suspended cronjob", "name", cj.Name)
			}
		}
	}

	// Handle CNPG Clusters
	if r.shouldManageType(schedule, "Cluster") {
		clusterList := &unstructured.UnstructuredList{}
		clusterList.SetGroupVersionKind(schema.GroupVersionKind{
			Group:   cnpgClusterGVK.Group,
			Version: cnpgClusterGVK.Version,
			Kind:    cnpgClusterGVK.Kind + "List",
		})

		if err := r.List(ctx, clusterList, client.InNamespace(schedule.Namespace), client.MatchingLabels(schedule.Spec.Selector.MatchLabels)); err != nil {
			if apimeta.IsNoMatchError(err) {
				logger.Info("CNPG Cluster CRD not installed, skipping")
			} else {
				return err
			}
		} else {
			for i := range clusterList.Items {
				cluster := &clusterList.Items[i]
				annotations := cluster.GetAnnotations()
				currentHibernation := ""
				if annotations != nil {
					currentHibernation = annotations[hibernationAnnotation]
				}

				if currentHibernation != "on" {
					originalHibernation := currentHibernation
					schedule.Status.ManagedWorkloads = append(schedule.Status.ManagedWorkloads, slumlordv1alpha1.ManagedWorkload{
						Kind:                "Cluster",
						Name:                cluster.GetName(),
						OriginalHibernation: &originalHibernation,
					})

					if annotations == nil {
						annotations = make(map[string]string)
					}
					annotations[hibernationAnnotation] = "on"
					cluster.SetAnnotations(annotations)
					if err := r.Update(ctx, cluster); err != nil {
						return err
					}
					logger.Info("Hibernated CNPG cluster", "name", cluster.GetName())
				}
			}
		}
	}

	// Handle Prometheus Operator CRDs (ThanosRuler, Alertmanager, Prometheus)
	if r.shouldManageType(schedule, "ThanosRuler") {
		if err := r.sleepReplicaResources(ctx, schedule, promThanosRulerGVK, "ThanosRuler"); err != nil {
			return err
		}
	}
	if r.shouldManageType(schedule, "Alertmanager") {
		if err := r.sleepReplicaResources(ctx, schedule, promAlertmanagerGVK, "Alertmanager"); err != nil {
			return err
		}
	}
	if r.shouldManageType(schedule, "Prometheus") {
		if err := r.sleepReplicaResources(ctx, schedule, promPrometheusGVK, "Prometheus"); err != nil {
			return err
		}
	}

	return nil
}

// wakeWorkloads restores workloads to their original state.
// ManagedWorkloads is preserved after wake for verification; cleared by Reconcile after verify window.
func (r *SleepScheduleReconciler) wakeWorkloads(ctx context.Context, schedule *slumlordv1alpha1.SlumlordSleepSchedule) error {
	logger := log.FromContext(ctx)

	// First pass: wake all workloads except suspend-based operators (FluxCD, MariaDB)
	for _, managed := range schedule.Status.ManagedWorkloads {
		switch managed.Kind {
		case "HelmRelease", "Kustomization", "MariaDB", "MaxScale":
			continue // handle in second pass after workloads are restored

		case "Deployment":
			var deploy appsv1.Deployment
			if err := r.Get(ctx, client.ObjectKey{Namespace: schedule.Namespace, Name: managed.Name}, &deploy); err != nil {
				logger.Error(err, "Failed to get deployment", "name", managed.Name)
				continue
			}
			if managed.OriginalReplicas != nil {
				deploy.Spec.Replicas = managed.OriginalReplicas
				if err := r.Update(ctx, &deploy); err != nil {
					return err
				}
				logger.Info("Scaled up deployment", "name", deploy.Name, "replicas", *managed.OriginalReplicas)
			}

		case "StatefulSet":
			var sts appsv1.StatefulSet
			if err := r.Get(ctx, client.ObjectKey{Namespace: schedule.Namespace, Name: managed.Name}, &sts); err != nil {
				logger.Error(err, "Failed to get statefulset", "name", managed.Name)
				continue
			}
			if managed.OriginalReplicas != nil {
				sts.Spec.Replicas = managed.OriginalReplicas
				if err := r.Update(ctx, &sts); err != nil {
					return err
				}
				logger.Info("Scaled up statefulset", "name", sts.Name, "replicas", *managed.OriginalReplicas)
			}

		case "CronJob":
			var cj batchv1.CronJob
			if err := r.Get(ctx, client.ObjectKey{Namespace: schedule.Namespace, Name: managed.Name}, &cj); err != nil {
				logger.Error(err, "Failed to get cronjob", "name", managed.Name)
				continue
			}
			if managed.OriginalSuspend != nil {
				cj.Spec.Suspend = managed.OriginalSuspend
				if err := r.Update(ctx, &cj); err != nil {
					return err
				}
				logger.Info("Resumed cronjob", "name", cj.Name)
			}

		case "Cluster":
			cluster := &unstructured.Unstructured{}
			cluster.SetGroupVersionKind(cnpgClusterGVK)
			if err := r.Get(ctx, client.ObjectKey{Namespace: schedule.Namespace, Name: managed.Name}, cluster); err != nil {
				logger.Error(err, "Failed to get CNPG cluster", "name", managed.Name)
				continue
			}
			annotations := cluster.GetAnnotations()
			if annotations == nil {
				annotations = make(map[string]string)
			}
			if managed.OriginalHibernation != nil && *managed.OriginalHibernation != "" {
				annotations[hibernationAnnotation] = *managed.OriginalHibernation
			} else {
				delete(annotations, hibernationAnnotation)
			}
			cluster.SetAnnotations(annotations)
			if err := r.Update(ctx, cluster); err != nil {
				return err
			}
			logger.Info("Woke CNPG cluster", "name", cluster.GetName())

		case "ThanosRuler":
			if err := r.wakeReplicaResource(ctx, schedule, managed, promThanosRulerGVK); err != nil {
				return err
			}
		case "Alertmanager":
			if err := r.wakeReplicaResource(ctx, schedule, managed, promAlertmanagerGVK); err != nil {
				return err
			}
		case "Prometheus":
			if err := r.wakeReplicaResource(ctx, schedule, managed, promPrometheusGVK); err != nil {
				return err
			}
		}
	}

	// Second pass: resume suspend-based operators AFTER workloads are restored
	for _, managed := range schedule.Status.ManagedWorkloads {
		switch managed.Kind {
		case "HelmRelease":
			if err := r.wakeSuspendResource(ctx, schedule, managed, fluxHelmReleaseGVK); err != nil {
				return err
			}
		case "Kustomization":
			if err := r.wakeSuspendResource(ctx, schedule, managed, fluxKustomizationGVK); err != nil {
				return err
			}
		case "MariaDB":
			if err := r.wakeSuspendResource(ctx, schedule, managed, mariadbGVK); err != nil {
				return err
			}
		case "MaxScale":
			if err := r.wakeSuspendResource(ctx, schedule, managed, mariadbMaxScaleGVK); err != nil {
				return err
			}
		}
	}

	// ManagedWorkloads intentionally NOT cleared here.
	// Reconcile will clear them after the verification window expires.
	return nil
}

// shouldManageType checks if the schedule should manage a specific workload type
func (r *SleepScheduleReconciler) shouldManageType(schedule *slumlordv1alpha1.SlumlordSleepSchedule, kind string) bool {
	// If no types specified, manage all types
	if len(schedule.Spec.Selector.Types) == 0 {
		return true
	}

	for _, t := range schedule.Spec.Selector.Types {
		if t == kind {
			return true
		}
	}
	return false
}

// sleepSuspendResources suspends CRDs that use spec.suspend (FluxCD, MariaDB operator)
func (r *SleepScheduleReconciler) sleepSuspendResources(ctx context.Context, schedule *slumlordv1alpha1.SlumlordSleepSchedule, gvk schema.GroupVersionKind, kind string) error {
	logger := log.FromContext(ctx)

	resourceList := &unstructured.UnstructuredList{}
	resourceList.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   gvk.Group,
		Version: gvk.Version,
		Kind:    gvk.Kind + "List",
	})

	if err := r.List(ctx, resourceList, client.InNamespace(schedule.Namespace), client.MatchingLabels(schedule.Spec.Selector.MatchLabels)); err != nil {
		if apimeta.IsNoMatchError(err) {
			logger.Info("CRD not installed, skipping", "kind", kind)
			return nil
		}
		return err
	}

	for i := range resourceList.Items {
		resource := &resourceList.Items[i]
		spec, _ := resource.Object["spec"].(map[string]interface{})
		currentSuspend := false
		if spec != nil {
			if s, ok := spec["suspend"].(bool); ok {
				currentSuspend = s
			}
		}

		if !currentSuspend {
			original := currentSuspend
			schedule.Status.ManagedWorkloads = append(schedule.Status.ManagedWorkloads, slumlordv1alpha1.ManagedWorkload{
				Kind:            kind,
				Name:            resource.GetName(),
				OriginalSuspend: &original,
			})

			if spec == nil {
				spec = make(map[string]interface{})
				resource.Object["spec"] = spec
			}
			spec["suspend"] = true
			if err := r.Update(ctx, resource); err != nil {
				return err
			}
			logger.Info("Suspended resource", "kind", kind, "name", resource.GetName())
		}
	}

	return nil
}

// wakeSuspendResource resumes a single suspend-based resource
func (r *SleepScheduleReconciler) wakeSuspendResource(ctx context.Context, schedule *slumlordv1alpha1.SlumlordSleepSchedule, managed slumlordv1alpha1.ManagedWorkload, gvk schema.GroupVersionKind) error {
	logger := log.FromContext(ctx)

	resource := &unstructured.Unstructured{}
	resource.SetGroupVersionKind(gvk)
	if err := r.Get(ctx, client.ObjectKey{Namespace: schedule.Namespace, Name: managed.Name}, resource); err != nil {
		logger.Error(err, "Failed to get resource", "kind", managed.Kind, "name", managed.Name)
		return nil
	}

	spec, _ := resource.Object["spec"].(map[string]interface{})
	if spec == nil {
		spec = make(map[string]interface{})
		resource.Object["spec"] = spec
	}
	if managed.OriginalSuspend != nil {
		spec["suspend"] = *managed.OriginalSuspend
	} else {
		delete(spec, "suspend")
	}
	if err := r.Update(ctx, resource); err != nil {
		return err
	}
	logger.Info("Resumed resource", "kind", managed.Kind, "name", resource.GetName())
	return nil
}

// sleepReplicaResources scales down CRDs that use spec.replicas (e.g., Prometheus Operator CRDs)
func (r *SleepScheduleReconciler) sleepReplicaResources(ctx context.Context, schedule *slumlordv1alpha1.SlumlordSleepSchedule, gvk schema.GroupVersionKind, kind string) error {
	logger := log.FromContext(ctx)

	resourceList := &unstructured.UnstructuredList{}
	resourceList.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   gvk.Group,
		Version: gvk.Version,
		Kind:    gvk.Kind + "List",
	})

	if err := r.List(ctx, resourceList, client.InNamespace(schedule.Namespace), client.MatchingLabels(schedule.Spec.Selector.MatchLabels)); err != nil {
		if apimeta.IsNoMatchError(err) {
			logger.Info("CRD not installed, skipping", "kind", kind)
			return nil
		}
		return err
	}

	for i := range resourceList.Items {
		resource := &resourceList.Items[i]
		spec, _ := resource.Object["spec"].(map[string]interface{})
		if spec == nil {
			continue
		}

		var currentReplicas int32
		switch v := spec["replicas"].(type) {
		case int64:
			currentReplicas = int32(v)
		case float64:
			currentReplicas = int32(v)
		default:
			continue
		}

		if currentReplicas > 0 {
			original := currentReplicas
			schedule.Status.ManagedWorkloads = append(schedule.Status.ManagedWorkloads, slumlordv1alpha1.ManagedWorkload{
				Kind:             kind,
				Name:             resource.GetName(),
				OriginalReplicas: &original,
			})

			spec["replicas"] = int64(0)
			if err := r.Update(ctx, resource); err != nil {
				return err
			}
			logger.Info("Scaled down resource", "kind", kind, "name", resource.GetName(), "originalReplicas", original)
		}
	}

	return nil
}

// wakeReplicaResource restores spec.replicas on a CRD resource
func (r *SleepScheduleReconciler) wakeReplicaResource(ctx context.Context, schedule *slumlordv1alpha1.SlumlordSleepSchedule, managed slumlordv1alpha1.ManagedWorkload, gvk schema.GroupVersionKind) error {
	logger := log.FromContext(ctx)

	resource := &unstructured.Unstructured{}
	resource.SetGroupVersionKind(gvk)
	if err := r.Get(ctx, client.ObjectKey{Namespace: schedule.Namespace, Name: managed.Name}, resource); err != nil {
		logger.Error(err, "Failed to get resource", "kind", managed.Kind, "name", managed.Name)
		return nil
	}

	if managed.OriginalReplicas != nil {
		spec, _ := resource.Object["spec"].(map[string]interface{})
		if spec == nil {
			spec = make(map[string]interface{})
			resource.Object["spec"] = spec
		}
		spec["replicas"] = int64(*managed.OriginalReplicas)
		if err := r.Update(ctx, resource); err != nil {
			return err
		}
		logger.Info("Scaled up resource", "kind", managed.Kind, "name", managed.Name, "replicas", *managed.OriginalReplicas)
	}

	return nil
}

var dayNames = [7]string{"Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"}

// daysToDisplay converts a slice of day numbers (0=Sun..6=Sat) to a human-readable string.
// Consecutive weekdays are collapsed into ranges (e.g., "Mon-Fri").
func daysToDisplay(days []int) string {
	if len(days) == 0 {
		return "Every day"
	}

	sorted := make([]int, len(days))
	copy(sorted, days)
	sort.Ints(sorted)

	// Build ranges of consecutive days
	type dayRange struct{ start, end int }
	var ranges []dayRange
	start := sorted[0]
	end := sorted[0]
	for i := 1; i < len(sorted); i++ {
		if sorted[i] == end+1 {
			end = sorted[i]
		} else {
			ranges = append(ranges, dayRange{start, end})
			start = sorted[i]
			end = sorted[i]
		}
	}
	ranges = append(ranges, dayRange{start, end})

	// Format each range
	parts := make([]string, 0, len(ranges))
	for _, r := range ranges {
		if r.start == r.end {
			parts = append(parts, dayNames[r.start])
		} else if r.end-r.start == 1 {
			parts = append(parts, dayNames[r.start]+","+dayNames[r.end])
		} else {
			parts = append(parts, dayNames[r.start]+"-"+dayNames[r.end])
		}
	}
	return strings.Join(parts, ",")
}

// setScheduleCondition sets the Ready condition on a SlumlordSleepSchedule.
func (r *SleepScheduleReconciler) setScheduleCondition(
	schedule *slumlordv1alpha1.SlumlordSleepSchedule,
	status metav1.ConditionStatus, reason, message string,
) {
	apimeta.SetStatusCondition(&schedule.Status.Conditions, metav1.Condition{
		Type:               "Ready",
		Status:             status,
		ObservedGeneration: schedule.Generation,
		Reason:             reason,
		Message:            message,
	})
}

// SetupWithManager sets up the controller with the Manager
func (r *SleepScheduleReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&slumlordv1alpha1.SlumlordSleepSchedule{}).
		WithOptions(crcontroller.Options{MaxConcurrentReconciles: 5}).
		Complete(r)
}
