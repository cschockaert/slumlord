package controller

import (
	"context"
	"fmt"
	"sort"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	slumlordv1alpha1 "github.com/cschockaert/slumlord/api/v1alpha1"
)

const (
	defaultAutoScheduleInterval = 5 * time.Minute
	conflictPolicyOverlap       = "PolicyOverlap"
	conflictManualSchedule      = "ManualScheduleExists"
)

// AutoSchedulePolicyReconciler reconciles a SlumlordAutoSchedulePolicy object.
type AutoSchedulePolicyReconciler struct {
	client.Client
	Scheme                   *runtime.Scheme
	Recorder                 events.EventRecorder
	DefaultReconcileInterval time.Duration
}

// +kubebuilder:rbac:groups=slumlord.io,resources=slumlordautoschedulepolicies,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=slumlord.io,resources=slumlordautoschedulepolicies/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=slumlord.io,resources=slumlordautoschedulepolicies/finalizers,verbs=update
// +kubebuilder:rbac:groups=slumlord.io,resources=slumlordsleepschedules,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch

// Reconcile implements the AutoSchedulePolicy control loop.
func (r *AutoSchedulePolicyReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx).WithValues("policy", req.Name)

	var policy slumlordv1alpha1.SlumlordAutoSchedulePolicy
	if err := r.Get(ctx, req.NamespacedName, &policy); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !policy.DeletionTimestamp.IsZero() {
		if controllerutil.ContainsFinalizer(&policy, slumlordv1alpha1.AutoSchedulePolicyFinalizer) {
			if err := r.deleteAllManagedSchedules(ctx, &policy); err != nil {
				return ctrl.Result{}, err
			}
			controllerutil.RemoveFinalizer(&policy, slumlordv1alpha1.AutoSchedulePolicyFinalizer)
			if err := r.Update(ctx, &policy); err != nil {
				return ctrl.Result{}, err
			}
		}
		return ctrl.Result{}, nil
	}

	if !controllerutil.ContainsFinalizer(&policy, slumlordv1alpha1.AutoSchedulePolicyFinalizer) {
		controllerutil.AddFinalizer(&policy, slumlordv1alpha1.AutoSchedulePolicyFinalizer)
		if err := r.Update(ctx, &policy); err != nil {
			return ctrl.Result{}, err
		}
	}

	if policy.Spec.Suspend {
		logger.V(1).Info("Policy suspended; existing schedules left untouched")
		return r.requeue(&policy), nil
	}

	allNamespaces := &corev1.NamespaceList{}
	if err := r.List(ctx, allNamespaces); err != nil {
		return ctrl.Result{}, fmt.Errorf("list namespaces: %w", err)
	}

	otherPolicies, err := r.listOtherPolicies(ctx, policy.Name)
	if err != nil {
		return ctrl.Result{}, err
	}

	matched := []corev1.Namespace{}
	for i := range allNamespaces.Items {
		ns := &allNamespaces.Items[i]
		ok, err := namespaceMatchesPolicy(&policy, ns)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("evaluate namespaceSelector for %s: %w", ns.Name, err)
		}
		if ok {
			matched = append(matched, *ns)
		}
	}

	conflicts := []slumlordv1alpha1.NamespaceConflict{}
	missing := []slumlordv1alpha1.MissingProfile{}
	generated := int32(0)
	desiredInNS := map[string]bool{}

	for i := range matched {
		ns := &matched[i]
		profileName := ns.Labels[policy.ResolvedProfileLabel()]

		if overlap := overlappingPolicies(otherPolicies, ns); len(overlap) > 0 {
			conflicts = append(conflicts, slumlordv1alpha1.NamespaceConflict{
				Namespace:    ns.Name,
				Reason:       conflictPolicyOverlap,
				WithPolicies: overlap,
				Message:      "another SlumlordAutoSchedulePolicy also matches this namespace",
			})
			continue
		}

		profile, ok := policy.Spec.Profiles[profileName]
		if !ok {
			missing = append(missing, slumlordv1alpha1.MissingProfile{
				Namespace: ns.Name,
				Profile:   profileName,
			})
			continue
		}

		existing := &slumlordv1alpha1.SlumlordSleepSchedule{}
		key := types.NamespacedName{Namespace: ns.Name, Name: policy.ResolvedGeneratedName()}
		err := r.Get(ctx, key, existing)
		switch {
		case apierrors.IsNotFound(err):
			created := &slumlordv1alpha1.SlumlordSleepSchedule{
				ObjectMeta: metav1.ObjectMeta{
					Name:      policy.ResolvedGeneratedName(),
					Namespace: ns.Name,
					Labels:    map[string]string{slumlordv1alpha1.ManagedByLabel: policy.Name},
				},
				Spec: buildSleepScheduleSpec(profile),
			}
			if err := r.Create(ctx, created); err != nil {
				return ctrl.Result{}, fmt.Errorf("create SleepSchedule in %s: %w", ns.Name, err)
			}
			desiredInNS[ns.Name] = true
			generated++
		case err != nil:
			return ctrl.Result{}, fmt.Errorf("get SleepSchedule in %s: %w", ns.Name, err)
		default:
			if existing.Labels[slumlordv1alpha1.ManagedByLabel] != policy.Name {
				conflicts = append(conflicts, slumlordv1alpha1.NamespaceConflict{
					Namespace: ns.Name,
					Reason:    conflictManualSchedule,
					Message:   fmt.Sprintf("a manually-managed SleepSchedule named %q already exists", policy.ResolvedGeneratedName()),
				})
				continue
			}
			desired := buildSleepScheduleSpec(profile)
			if !sleepSpecEqual(existing.Spec, desired) {
				existing.Spec = desired
				if err := r.Update(ctx, existing); err != nil {
					return ctrl.Result{}, fmt.Errorf("update SleepSchedule in %s: %w", ns.Name, err)
				}
			}
			desiredInNS[ns.Name] = true
			generated++
		}
	}

	if err := r.deleteOrphanedSchedules(ctx, &policy, desiredInNS); err != nil {
		return ctrl.Result{}, err
	}

	return r.requeue(&policy), r.updateStatus(ctx, &policy, int32(len(matched)), generated, conflicts, missing)
}

func (r *AutoSchedulePolicyReconciler) requeue(policy *slumlordv1alpha1.SlumlordAutoSchedulePolicy) ctrl.Result {
	d := defaultAutoScheduleInterval
	if r.DefaultReconcileInterval > 0 {
		d = r.DefaultReconcileInterval
	}
	if policy.Spec.ReconcileInterval != nil {
		d = policy.Spec.ReconcileInterval.Duration
	}
	return ctrl.Result{RequeueAfter: d}
}

func (r *AutoSchedulePolicyReconciler) listOtherPolicies(ctx context.Context, selfName string) ([]slumlordv1alpha1.SlumlordAutoSchedulePolicy, error) {
	all := &slumlordv1alpha1.SlumlordAutoSchedulePolicyList{}
	if err := r.List(ctx, all); err != nil {
		return nil, fmt.Errorf("list policies: %w", err)
	}
	out := make([]slumlordv1alpha1.SlumlordAutoSchedulePolicy, 0, len(all.Items))
	for i := range all.Items {
		if all.Items[i].Name == selfName {
			continue
		}
		if !all.Items[i].DeletionTimestamp.IsZero() {
			continue
		}
		out = append(out, all.Items[i])
	}
	return out, nil
}

func overlappingPolicies(others []slumlordv1alpha1.SlumlordAutoSchedulePolicy, ns *corev1.Namespace) []string {
	var conflicts []string
	for i := range others {
		ok, err := namespaceMatchesPolicy(&others[i], ns)
		if err != nil || !ok {
			continue
		}
		conflicts = append(conflicts, others[i].Name)
	}
	sort.Strings(conflicts)
	return conflicts
}

func (r *AutoSchedulePolicyReconciler) deleteOrphanedSchedules(ctx context.Context, policy *slumlordv1alpha1.SlumlordAutoSchedulePolicy, keep map[string]bool) error {
	owned := &slumlordv1alpha1.SlumlordSleepScheduleList{}
	if err := r.List(ctx, owned, client.MatchingLabels{slumlordv1alpha1.ManagedByLabel: policy.Name}); err != nil {
		return fmt.Errorf("list managed SleepSchedules: %w", err)
	}
	for i := range owned.Items {
		ss := &owned.Items[i]
		if keep[ss.Namespace] && ss.Name == policy.ResolvedGeneratedName() {
			continue
		}
		if err := r.Delete(ctx, ss); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("delete orphan SleepSchedule %s/%s: %w", ss.Namespace, ss.Name, err)
		}
	}
	return nil
}

func (r *AutoSchedulePolicyReconciler) deleteAllManagedSchedules(ctx context.Context, policy *slumlordv1alpha1.SlumlordAutoSchedulePolicy) error {
	owned := &slumlordv1alpha1.SlumlordSleepScheduleList{}
	if err := r.List(ctx, owned, client.MatchingLabels{slumlordv1alpha1.ManagedByLabel: policy.Name}); err != nil {
		return fmt.Errorf("list managed SleepSchedules: %w", err)
	}
	for i := range owned.Items {
		ss := &owned.Items[i]
		if err := r.Delete(ctx, ss); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("delete managed SleepSchedule %s/%s: %w", ss.Namespace, ss.Name, err)
		}
	}
	return nil
}

func (r *AutoSchedulePolicyReconciler) updateStatus(ctx context.Context, policy *slumlordv1alpha1.SlumlordAutoSchedulePolicy, matched, generated int32, conflicts []slumlordv1alpha1.NamespaceConflict, missing []slumlordv1alpha1.MissingProfile) error {
	sort.Slice(conflicts, func(i, j int) bool { return conflicts[i].Namespace < conflicts[j].Namespace })
	sort.Slice(missing, func(i, j int) bool { return missing[i].Namespace < missing[j].Namespace })

	policy.Status.ObservedGeneration = policy.Generation
	policy.Status.MatchedNamespaces = matched
	policy.Status.GeneratedSchedules = generated
	policy.Status.Conflicts = conflicts
	policy.Status.MissingProfiles = missing

	cond := metav1.Condition{
		Type:               "Ready",
		Status:             metav1.ConditionTrue,
		Reason:             "Reconciled",
		Message:            fmt.Sprintf("%d namespace(s) matched, %d schedule(s) generated", matched, generated),
		LastTransitionTime: metav1.Now(),
		ObservedGeneration: policy.Generation,
	}
	if len(conflicts) > 0 || len(missing) > 0 {
		cond.Status = metav1.ConditionFalse
		cond.Reason = "PartialReconcile"
	}
	policy.Status.Conditions = []metav1.Condition{cond}

	return r.Status().Update(ctx, policy)
}

// namespaceMatchesPolicy evaluates whether ns is selected by the policy's
// namespaceSelector. When the selector is nil, the default selector
// {key=<profileLabel>, op=Exists} is used.
func namespaceMatchesPolicy(policy *slumlordv1alpha1.SlumlordAutoSchedulePolicy, ns *corev1.Namespace) (bool, error) {
	sel := policy.Spec.NamespaceSelector
	if sel == nil {
		sel = &metav1.LabelSelector{
			MatchExpressions: []metav1.LabelSelectorRequirement{{
				Key:      policy.ResolvedProfileLabel(),
				Operator: metav1.LabelSelectorOpExists,
			}},
		}
	}
	s, err := metav1.LabelSelectorAsSelector(sel)
	if err != nil {
		return false, err
	}
	return s.Matches(labels.Set(ns.Labels)), nil
}

// buildSleepScheduleSpec converts a profile into a SleepSchedule spec.
func buildSleepScheduleSpec(profile slumlordv1alpha1.AutoScheduleProfile) slumlordv1alpha1.SlumlordSleepScheduleSpec {
	return slumlordv1alpha1.SlumlordSleepScheduleSpec{
		Suspend:   profile.Suspend,
		Selector:  *profile.Selector.DeepCopy(),
		Schedules: append([]slumlordv1alpha1.SleepWindow(nil), profile.Schedules...),
	}
}

// sleepSpecEqual compares only the fields managed by the policy template. We
// don't compare status, finalizers, or transient client-side state.
func sleepSpecEqual(a, b slumlordv1alpha1.SlumlordSleepScheduleSpec) bool {
	if a.Suspend != b.Suspend {
		return false
	}
	if !workloadSelectorEqual(a.Selector, b.Selector) {
		return false
	}
	if len(a.Schedules) != len(b.Schedules) {
		return false
	}
	for i := range a.Schedules {
		if !sleepWindowEqual(a.Schedules[i], b.Schedules[i]) {
			return false
		}
	}
	return true
}

func workloadSelectorEqual(a, b slumlordv1alpha1.WorkloadSelector) bool {
	if !stringSliceEqual(a.Types, b.Types) {
		return false
	}
	if !stringSliceEqual(a.MatchNames, b.MatchNames) {
		return false
	}
	if len(a.MatchLabels) != len(b.MatchLabels) {
		return false
	}
	for k, v := range a.MatchLabels {
		if b.MatchLabels[k] != v {
			return false
		}
	}
	return true
}

func sleepWindowEqual(a, b slumlordv1alpha1.SleepWindow) bool {
	if a.Start != b.Start || a.End != b.End || a.Timezone != b.Timezone {
		return false
	}
	return intSliceEqual(a.Days, b.Days)
}

func stringSliceEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func intSliceEqual(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// SetupWithManager wires up watches for the policy itself, namespaces (label
// changes), SleepSchedule children (drift detection), and peer policies (so a
// new/updated peer that creates an overlap re-reconciles all sides).
func (r *AutoSchedulePolicyReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&slumlordv1alpha1.SlumlordAutoSchedulePolicy{}).
		Watches(
			&corev1.Namespace{},
			handler.EnqueueRequestsFromMapFunc(r.namespaceToPolicy),
			builder.WithPredicates(predicate.LabelChangedPredicate{}),
		).
		Watches(
			&slumlordv1alpha1.SlumlordSleepSchedule{},
			handler.EnqueueRequestsFromMapFunc(r.sleepScheduleToPolicy),
		).
		Watches(
			&slumlordv1alpha1.SlumlordAutoSchedulePolicy{},
			handler.EnqueueRequestsFromMapFunc(r.peerPolicyToPolicy),
		).
		Complete(r)
}

// namespaceToPolicy maps a Namespace event to the subset of policies whose
// selector evaluates true for that namespace. Pre-filtering here avoids
// O(M) reconciles per Namespace event on large clusters.
func (r *AutoSchedulePolicyReconciler) namespaceToPolicy(ctx context.Context, obj client.Object) []reconcile.Request {
	ns, ok := obj.(*corev1.Namespace)
	if !ok {
		return nil
	}
	policies := &slumlordv1alpha1.SlumlordAutoSchedulePolicyList{}
	if err := r.List(ctx, policies); err != nil {
		return nil
	}
	out := make([]reconcile.Request, 0, len(policies.Items))
	for i := range policies.Items {
		matches, err := namespaceMatchesPolicy(&policies.Items[i], ns)
		if err != nil || !matches {
			continue
		}
		out = append(out, reconcile.Request{NamespacedName: types.NamespacedName{Name: policies.Items[i].Name}})
	}
	return out
}

// peerPolicyToPolicy enqueues every OTHER policy when a policy changes. This
// keeps Status.Conflicts bidirectional: if policy A overlaps with B, both A
// and B will reconcile and surface the conflict.
func (r *AutoSchedulePolicyReconciler) peerPolicyToPolicy(ctx context.Context, obj client.Object) []reconcile.Request {
	policies := &slumlordv1alpha1.SlumlordAutoSchedulePolicyList{}
	if err := r.List(ctx, policies); err != nil {
		return nil
	}
	out := make([]reconcile.Request, 0, len(policies.Items))
	for i := range policies.Items {
		if policies.Items[i].Name == obj.GetName() {
			continue
		}
		out = append(out, reconcile.Request{NamespacedName: types.NamespacedName{Name: policies.Items[i].Name}})
	}
	return out
}

// sleepScheduleToPolicy maps a SleepSchedule event back to its managing policy
// via the managed-by label. SleepSchedules without that label are ignored.
func (r *AutoSchedulePolicyReconciler) sleepScheduleToPolicy(_ context.Context, obj client.Object) []reconcile.Request {
	name := obj.GetLabels()[slumlordv1alpha1.ManagedByLabel]
	if name == "" {
		return nil
	}
	return []reconcile.Request{{NamespacedName: types.NamespacedName{Name: name}}}
}
