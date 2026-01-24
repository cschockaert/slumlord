package controller

import (
	"context"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	slumlordv1alpha1 "github.com/cschockaert/slumlord/api/v1alpha1"
)

// SleepScheduleReconciler reconciles a SlumlordSleepSchedule object
type SleepScheduleReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=slumlord.io,resources=slumlordsleepschedules,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=slumlord.io,resources=slumlordsleepschedules/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=apps,resources=statefulsets,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=batch,resources=cronjobs,verbs=get;list;watch;update;patch

// Reconcile handles the reconciliation loop for SlumlordSleepSchedule
func (r *SleepScheduleReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Fetch the SlumlordSleepSchedule
	var schedule slumlordv1alpha1.SlumlordSleepSchedule
	if err := r.Get(ctx, req.NamespacedName, &schedule); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Determine if we should be sleeping
	shouldSleep := r.shouldBeSleeping(&schedule)
	logger.Info("Reconciling sleep schedule", "shouldSleep", shouldSleep, "currentlySleeping", schedule.Status.Sleeping)

	// If state needs to change, update workloads
	if shouldSleep != schedule.Status.Sleeping {
		if shouldSleep {
			if err := r.sleepWorkloads(ctx, &schedule); err != nil {
				logger.Error(err, "Failed to sleep workloads")
				return ctrl.Result{RequeueAfter: time.Minute}, err
			}
		} else {
			if err := r.wakeWorkloads(ctx, &schedule); err != nil {
				logger.Error(err, "Failed to wake workloads")
				return ctrl.Result{RequeueAfter: time.Minute}, err
			}
		}

		// Update status
		now := ctrl.Now()
		schedule.Status.Sleeping = shouldSleep
		schedule.Status.LastTransitionTime = &now
		if err := r.Status().Update(ctx, &schedule); err != nil {
			return ctrl.Result{}, err
		}
	}

	// Requeue to check again in 1 minute
	return ctrl.Result{RequeueAfter: time.Minute}, nil
}

// shouldBeSleeping determines if workloads should be sleeping based on the schedule
func (r *SleepScheduleReconciler) shouldBeSleeping(schedule *slumlordv1alpha1.SlumlordSleepSchedule) bool {
	return r.shouldBeSleepingAt(schedule, time.Now())
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

	// Check if today is in the allowed days
	if len(schedule.Spec.Schedule.Days) > 0 {
		today := int(now.Weekday())
		dayAllowed := false
		for _, d := range schedule.Spec.Schedule.Days {
			if d == today {
				dayAllowed = true
				break
			}
		}
		if !dayAllowed {
			return false
		}
	}

	// Parse start and end times
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

	// Handle overnight schedules (e.g., 22:00 to 06:00)
	if endTime.Before(startTime) {
		// We're in overnight mode
		return now.After(startTime) || now.Before(endTime)
	}

	// Normal same-day schedule
	return now.After(startTime) && now.Before(endTime)
}

// sleepWorkloads scales down or suspends matched workloads
func (r *SleepScheduleReconciler) sleepWorkloads(ctx context.Context, schedule *slumlordv1alpha1.SlumlordSleepSchedule) error {
	logger := log.FromContext(ctx)
	schedule.Status.ManagedWorkloads = nil

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

	return nil
}

// wakeWorkloads restores workloads to their original state
func (r *SleepScheduleReconciler) wakeWorkloads(ctx context.Context, schedule *slumlordv1alpha1.SlumlordSleepSchedule) error {
	logger := log.FromContext(ctx)

	for _, managed := range schedule.Status.ManagedWorkloads {
		switch managed.Kind {
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
		}
	}

	schedule.Status.ManagedWorkloads = nil
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

// SetupWithManager sets up the controller with the Manager
func (r *SleepScheduleReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&slumlordv1alpha1.SlumlordSleepSchedule{}).
		Complete(r)
}
