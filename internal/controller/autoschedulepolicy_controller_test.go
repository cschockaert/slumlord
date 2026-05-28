package controller

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	slumlordv1alpha1 "github.com/cschockaert/slumlord/api/v1alpha1"
)

func newAutoPolicyScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(s))
	utilruntime.Must(slumlordv1alpha1.AddToScheme(s))
	return s
}

func newAutoPolicyReconciler(scheme *runtime.Scheme, fakeClient client.Client) *AutoSchedulePolicyReconciler {
	return &AutoSchedulePolicyReconciler{
		Client:   fakeClient,
		Scheme:   scheme,
		Recorder: events.NewFakeRecorder(100),
	}
}

func sampleProfile() slumlordv1alpha1.AutoScheduleProfile {
	return slumlordv1alpha1.AutoScheduleProfile{
		Selector: slumlordv1alpha1.WorkloadSelector{
			Types: []string{"Deployment", "StatefulSet"},
		},
		Schedules: []slumlordv1alpha1.SleepWindow{{
			Start:    "19:00",
			End:      "07:00",
			Days:     []int{1, 2, 3, 4, 5},
			Timezone: "Europe/Paris",
		}},
	}
}

func samplePolicy(name string) *slumlordv1alpha1.SlumlordAutoSchedulePolicy {
	return &slumlordv1alpha1.SlumlordAutoSchedulePolicy{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: slumlordv1alpha1.SlumlordAutoSchedulePolicySpec{
			Profiles: map[string]slumlordv1alpha1.AutoScheduleProfile{
				"nightly-strict": sampleProfile(),
			},
		},
	}
}

func nsWithProfile(name, profile string) *corev1.Namespace {
	return &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
			Labels: map[string]string{
				slumlordv1alpha1.DefaultProfileLabel: profile,
			},
		},
	}
}

// --- helper unit tests ---

func TestResolvedProfileLabel_Default(t *testing.T) {
	p := samplePolicy("p")
	if got := p.ResolvedProfileLabel(); got != slumlordv1alpha1.DefaultProfileLabel {
		t.Errorf("ResolvedProfileLabel() = %q, want %q", got, slumlordv1alpha1.DefaultProfileLabel)
	}
}

func TestResolvedProfileLabel_Override(t *testing.T) {
	p := samplePolicy("p")
	p.Spec.ProfileLabel = "custom.io/profile"
	if got := p.ResolvedProfileLabel(); got != "custom.io/profile" {
		t.Errorf("ResolvedProfileLabel() = %q, want %q", got, "custom.io/profile")
	}
}

func TestResolvedGeneratedName_Default(t *testing.T) {
	p := samplePolicy("p")
	if got := p.ResolvedGeneratedName(); got != slumlordv1alpha1.DefaultGeneratedScheduleName {
		t.Errorf("ResolvedGeneratedName() = %q, want %q", got, slumlordv1alpha1.DefaultGeneratedScheduleName)
	}
}

func TestNamespaceMatches_DefaultSelectorEnrolsByLabelPresence(t *testing.T) {
	p := samplePolicy("p")
	tests := []struct {
		name string
		ns   *corev1.Namespace
		want bool
	}{
		{"has profile label", nsWithProfile("acme", "nightly-strict"), true},
		{"no profile label", &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "kube-system"}}, false},
		{"empty profile label value", &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "x", Labels: map[string]string{slumlordv1alpha1.DefaultProfileLabel: ""}}}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := namespaceMatchesPolicy(p, tt.ns)
			if err != nil {
				t.Fatalf("namespaceMatchesPolicy err = %v", err)
			}
			if got != tt.want {
				t.Errorf("namespaceMatchesPolicy = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestNamespaceMatches_ExplicitSelectorOverridesDefault(t *testing.T) {
	p := samplePolicy("p")
	p.Spec.NamespaceSelector = &metav1.LabelSelector{
		MatchLabels: map[string]string{"team": "platform"},
	}
	hit := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
		Name:   "acme",
		Labels: map[string]string{"team": "platform", slumlordv1alpha1.DefaultProfileLabel: "nightly-strict"},
	}}
	miss := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
		Name:   "other",
		Labels: map[string]string{slumlordv1alpha1.DefaultProfileLabel: "nightly-strict"},
	}}
	got, err := namespaceMatchesPolicy(p, hit)
	if err != nil || !got {
		t.Errorf("expected hit match, got=%v err=%v", got, err)
	}
	got, err = namespaceMatchesPolicy(p, miss)
	if err != nil || got {
		t.Errorf("expected miss not to match, got=%v err=%v", got, err)
	}
}

func TestBuildSleepScheduleSpec_CopiesProfileFields(t *testing.T) {
	p := samplePolicy("p")
	prof := p.Spec.Profiles["nightly-strict"]
	prof.Suspend = true
	spec := buildSleepScheduleSpec(prof)
	if !spec.Suspend {
		t.Error("expected suspend true to propagate")
	}
	if len(spec.Schedules) != 1 {
		t.Fatalf("expected 1 schedule, got %d", len(spec.Schedules))
	}
	if spec.Schedules[0].Start != "19:00" || spec.Schedules[0].End != "07:00" {
		t.Errorf("schedule mismatch: %+v", spec.Schedules[0])
	}
	if len(spec.Selector.Types) != 2 {
		t.Errorf("expected 2 types, got %d", len(spec.Selector.Types))
	}
}

// --- reconcile end-to-end tests ---

func reconcilePolicy(t *testing.T, r *AutoSchedulePolicyReconciler, name string) {
	t.Helper()
	_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: name}})
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
}

func TestReconcile_CreatesSleepScheduleInMatchedNamespace(t *testing.T) {
	scheme := newAutoPolicyScheme()
	policy := samplePolicy("default")
	ns := nsWithProfile("acme-prod", "nightly-strict")

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(policy, ns).
		WithStatusSubresource(policy).
		Build()

	r := newAutoPolicyReconciler(scheme, c)
	reconcilePolicy(t, r, policy.Name)

	var ss slumlordv1alpha1.SlumlordSleepSchedule
	if err := c.Get(context.Background(), types.NamespacedName{
		Namespace: "acme-prod",
		Name:      slumlordv1alpha1.DefaultGeneratedScheduleName,
	}, &ss); err != nil {
		t.Fatalf("expected generated SleepSchedule, got err: %v", err)
	}
	if ss.Labels[slumlordv1alpha1.ManagedByLabel] != "default" {
		t.Errorf("missing managed-by label: %v", ss.Labels)
	}
	if len(ss.Spec.Schedules) != 1 || ss.Spec.Schedules[0].Start != "19:00" {
		t.Errorf("schedule spec mismatch: %+v", ss.Spec)
	}

	var updated slumlordv1alpha1.SlumlordAutoSchedulePolicy
	if err := c.Get(context.Background(), types.NamespacedName{Name: "default"}, &updated); err != nil {
		t.Fatalf("get policy: %v", err)
	}
	if updated.Status.MatchedNamespaces != 1 {
		t.Errorf("MatchedNamespaces = %d, want 1", updated.Status.MatchedNamespaces)
	}
	if updated.Status.GeneratedSchedules != 1 {
		t.Errorf("GeneratedSchedules = %d, want 1", updated.Status.GeneratedSchedules)
	}
}

func TestReconcile_AddsFinalizer(t *testing.T) {
	scheme := newAutoPolicyScheme()
	policy := samplePolicy("default")
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(policy).WithStatusSubresource(policy).Build()
	r := newAutoPolicyReconciler(scheme, c)

	reconcilePolicy(t, r, policy.Name)

	var updated slumlordv1alpha1.SlumlordAutoSchedulePolicy
	if err := c.Get(context.Background(), types.NamespacedName{Name: "default"}, &updated); err != nil {
		t.Fatalf("get policy: %v", err)
	}
	found := false
	for _, f := range updated.Finalizers {
		if f == slumlordv1alpha1.AutoSchedulePolicyFinalizer {
			found = true
		}
	}
	if !found {
		t.Errorf("expected finalizer %q, got %v", slumlordv1alpha1.AutoSchedulePolicyFinalizer, updated.Finalizers)
	}
}

func TestReconcile_SkipsNamespaceWithUnknownProfile(t *testing.T) {
	scheme := newAutoPolicyScheme()
	policy := samplePolicy("default")
	ns := nsWithProfile("weird-ns", "does-not-exist")

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(policy, ns).
		WithStatusSubresource(policy).
		Build()

	r := newAutoPolicyReconciler(scheme, c)
	reconcilePolicy(t, r, policy.Name)

	var ss slumlordv1alpha1.SlumlordSleepSchedule
	err := c.Get(context.Background(), types.NamespacedName{
		Namespace: "weird-ns",
		Name:      slumlordv1alpha1.DefaultGeneratedScheduleName,
	}, &ss)
	if err == nil {
		t.Fatalf("did not expect a SleepSchedule for unknown profile")
	}

	var updated slumlordv1alpha1.SlumlordAutoSchedulePolicy
	if err := c.Get(context.Background(), types.NamespacedName{Name: "default"}, &updated); err != nil {
		t.Fatalf("get policy: %v", err)
	}
	if len(updated.Status.MissingProfiles) != 1 {
		t.Fatalf("expected 1 missing profile, got %+v", updated.Status.MissingProfiles)
	}
	if updated.Status.MissingProfiles[0].Profile != "does-not-exist" {
		t.Errorf("missing profile name mismatch: %+v", updated.Status.MissingProfiles[0])
	}
}

func TestReconcile_SkipsNamespaceWithManualSleepSchedule(t *testing.T) {
	scheme := newAutoPolicyScheme()
	policy := samplePolicy("default")
	ns := nsWithProfile("acme-prod", "nightly-strict")
	manual := &slumlordv1alpha1.SlumlordSleepSchedule{
		ObjectMeta: metav1.ObjectMeta{
			Name:      slumlordv1alpha1.DefaultGeneratedScheduleName,
			Namespace: "acme-prod",
		},
		Spec: slumlordv1alpha1.SlumlordSleepScheduleSpec{
			Selector:  slumlordv1alpha1.WorkloadSelector{Types: []string{"Deployment"}},
			Schedules: []slumlordv1alpha1.SleepWindow{{Start: "10:00", End: "18:00", Timezone: "UTC"}},
		},
	}

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(policy, ns, manual).
		WithStatusSubresource(policy).
		Build()

	r := newAutoPolicyReconciler(scheme, c)
	reconcilePolicy(t, r, policy.Name)

	var stillManual slumlordv1alpha1.SlumlordSleepSchedule
	if err := c.Get(context.Background(), types.NamespacedName{
		Namespace: "acme-prod",
		Name:      slumlordv1alpha1.DefaultGeneratedScheduleName,
	}, &stillManual); err != nil {
		t.Fatalf("manual schedule disappeared: %v", err)
	}
	if stillManual.Spec.Schedules[0].Start != "10:00" {
		t.Errorf("manual schedule was overwritten: %+v", stillManual.Spec)
	}

	var updated slumlordv1alpha1.SlumlordAutoSchedulePolicy
	_ = c.Get(context.Background(), types.NamespacedName{Name: "default"}, &updated)
	if len(updated.Status.Conflicts) != 1 || updated.Status.Conflicts[0].Reason != "ManualScheduleExists" {
		t.Errorf("expected ManualScheduleExists conflict, got %+v", updated.Status.Conflicts)
	}
}

func TestReconcile_DetectsMultiPolicyConflict(t *testing.T) {
	scheme := newAutoPolicyScheme()
	a := samplePolicy("policy-a")
	b := samplePolicy("policy-b")
	ns := nsWithProfile("acme-prod", "nightly-strict")

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(a, b, ns).
		WithStatusSubresource(a, b).
		Build()

	r := newAutoPolicyReconciler(scheme, c)
	reconcilePolicy(t, r, "policy-a")

	var updatedA slumlordv1alpha1.SlumlordAutoSchedulePolicy
	_ = c.Get(context.Background(), types.NamespacedName{Name: "policy-a"}, &updatedA)
	if len(updatedA.Status.Conflicts) != 1 || updatedA.Status.Conflicts[0].Reason != "PolicyOverlap" {
		t.Fatalf("expected PolicyOverlap conflict, got %+v", updatedA.Status.Conflicts)
	}
	if updatedA.Status.GeneratedSchedules != 0 {
		t.Errorf("expected 0 generated schedules on conflict, got %d", updatedA.Status.GeneratedSchedules)
	}

	var ss slumlordv1alpha1.SlumlordSleepSchedule
	if err := c.Get(context.Background(), types.NamespacedName{
		Namespace: "acme-prod",
		Name:      slumlordv1alpha1.DefaultGeneratedScheduleName,
	}, &ss); err == nil {
		t.Error("expected NO SleepSchedule when multiple policies conflict")
	}
}

func TestReconcile_UpdatesExistingManagedSchedule(t *testing.T) {
	scheme := newAutoPolicyScheme()
	policy := samplePolicy("default")
	ns := nsWithProfile("acme-prod", "nightly-strict")
	existing := &slumlordv1alpha1.SlumlordSleepSchedule{
		ObjectMeta: metav1.ObjectMeta{
			Name:      slumlordv1alpha1.DefaultGeneratedScheduleName,
			Namespace: "acme-prod",
			Labels:    map[string]string{slumlordv1alpha1.ManagedByLabel: "default"},
		},
		Spec: slumlordv1alpha1.SlumlordSleepScheduleSpec{
			Selector:  slumlordv1alpha1.WorkloadSelector{Types: []string{"Deployment"}},
			Schedules: []slumlordv1alpha1.SleepWindow{{Start: "01:00", End: "02:00", Timezone: "UTC"}},
		},
	}

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(policy, ns, existing).
		WithStatusSubresource(policy).
		Build()

	r := newAutoPolicyReconciler(scheme, c)
	reconcilePolicy(t, r, policy.Name)

	var ss slumlordv1alpha1.SlumlordSleepSchedule
	if err := c.Get(context.Background(), types.NamespacedName{
		Namespace: "acme-prod",
		Name:      slumlordv1alpha1.DefaultGeneratedScheduleName,
	}, &ss); err != nil {
		t.Fatalf("get schedule: %v", err)
	}
	if ss.Spec.Schedules[0].Start != "19:00" {
		t.Errorf("expected schedule to be updated to template, got Start=%q", ss.Spec.Schedules[0].Start)
	}
}

func TestReconcile_RemovesScheduleWhenNamespaceUnselects(t *testing.T) {
	scheme := newAutoPolicyScheme()
	policy := samplePolicy("default")
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "acme-prod"}} // no profile label
	orphan := &slumlordv1alpha1.SlumlordSleepSchedule{
		ObjectMeta: metav1.ObjectMeta{
			Name:      slumlordv1alpha1.DefaultGeneratedScheduleName,
			Namespace: "acme-prod",
			Labels:    map[string]string{slumlordv1alpha1.ManagedByLabel: "default"},
		},
		Spec: slumlordv1alpha1.SlumlordSleepScheduleSpec{
			Selector:  slumlordv1alpha1.WorkloadSelector{Types: []string{"Deployment"}},
			Schedules: []slumlordv1alpha1.SleepWindow{{Start: "01:00", End: "02:00", Timezone: "UTC"}},
		},
	}

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(policy, ns, orphan).
		WithStatusSubresource(policy).
		Build()

	r := newAutoPolicyReconciler(scheme, c)
	reconcilePolicy(t, r, policy.Name)

	var ss slumlordv1alpha1.SlumlordSleepSchedule
	err := c.Get(context.Background(), types.NamespacedName{
		Namespace: "acme-prod",
		Name:      slumlordv1alpha1.DefaultGeneratedScheduleName,
	}, &ss)
	if err == nil {
		t.Errorf("expected orphan schedule to be deleted, got %+v", ss)
	}
}

func TestReconcile_FinalizerCleansUpOnPolicyDeletion(t *testing.T) {
	scheme := newAutoPolicyScheme()
	now := metav1.Now()
	policy := samplePolicy("default")
	policy.DeletionTimestamp = &now
	policy.Finalizers = []string{slumlordv1alpha1.AutoSchedulePolicyFinalizer}

	managed := &slumlordv1alpha1.SlumlordSleepSchedule{
		ObjectMeta: metav1.ObjectMeta{
			Name:      slumlordv1alpha1.DefaultGeneratedScheduleName,
			Namespace: "acme-prod",
			Labels:    map[string]string{slumlordv1alpha1.ManagedByLabel: "default"},
		},
		Spec: slumlordv1alpha1.SlumlordSleepScheduleSpec{
			Selector:  slumlordv1alpha1.WorkloadSelector{Types: []string{"Deployment"}},
			Schedules: []slumlordv1alpha1.SleepWindow{{Start: "01:00", End: "02:00", Timezone: "UTC"}},
		},
	}
	other := &slumlordv1alpha1.SlumlordSleepSchedule{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "manual",
			Namespace: "acme-prod",
		},
		Spec: slumlordv1alpha1.SlumlordSleepScheduleSpec{
			Selector:  slumlordv1alpha1.WorkloadSelector{Types: []string{"Deployment"}},
			Schedules: []slumlordv1alpha1.SleepWindow{{Start: "01:00", End: "02:00", Timezone: "UTC"}},
		},
	}

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(policy, managed, other).
		WithStatusSubresource(policy).
		Build()

	r := newAutoPolicyReconciler(scheme, c)
	reconcilePolicy(t, r, policy.Name)

	var managedAfter slumlordv1alpha1.SlumlordSleepSchedule
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: "acme-prod", Name: slumlordv1alpha1.DefaultGeneratedScheduleName}, &managedAfter); err == nil {
		t.Error("expected managed schedule to be deleted")
	}
	var otherAfter slumlordv1alpha1.SlumlordSleepSchedule
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: "acme-prod", Name: "manual"}, &otherAfter); err != nil {
		t.Errorf("expected manual schedule to survive: %v", err)
	}
}

func TestReconcile_SuspendDoesNotTouchSchedules(t *testing.T) {
	scheme := newAutoPolicyScheme()
	policy := samplePolicy("default")
	policy.Spec.Suspend = true

	ns := nsWithProfile("acme-prod", "nightly-strict")
	existing := &slumlordv1alpha1.SlumlordSleepSchedule{
		ObjectMeta: metav1.ObjectMeta{
			Name:      slumlordv1alpha1.DefaultGeneratedScheduleName,
			Namespace: "acme-prod",
			Labels:    map[string]string{slumlordv1alpha1.ManagedByLabel: "default"},
		},
		Spec: slumlordv1alpha1.SlumlordSleepScheduleSpec{
			Selector:  slumlordv1alpha1.WorkloadSelector{Types: []string{"Deployment"}},
			Schedules: []slumlordv1alpha1.SleepWindow{{Start: "01:00", End: "02:00", Timezone: "UTC"}},
		},
	}

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(policy, ns, existing).
		WithStatusSubresource(policy).
		Build()

	r := newAutoPolicyReconciler(scheme, c)
	reconcilePolicy(t, r, policy.Name)

	var ss slumlordv1alpha1.SlumlordSleepSchedule
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: "acme-prod", Name: slumlordv1alpha1.DefaultGeneratedScheduleName}, &ss); err != nil {
		t.Fatalf("get schedule: %v", err)
	}
	if ss.Spec.Schedules[0].Start != "01:00" {
		t.Errorf("suspended policy must not overwrite schedule, got Start=%q", ss.Spec.Schedules[0].Start)
	}
}

func TestNamespaceToPolicy_FiltersByMatch(t *testing.T) {
	scheme := newAutoPolicyScheme()
	matchingPolicy := samplePolicy("matches")
	otherPolicy := samplePolicy("other")
	otherPolicy.Spec.NamespaceSelector = &metav1.LabelSelector{
		MatchLabels: map[string]string{"team": "platform"},
	}
	ns := nsWithProfile("acme-prod", "nightly-strict")

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(matchingPolicy, otherPolicy, ns).
		Build()

	r := newAutoPolicyReconciler(scheme, c)
	requests := r.namespaceToPolicy(context.Background(), ns)
	if len(requests) != 1 || requests[0].Name != "matches" {
		t.Errorf("expected only matching policy to be enqueued, got %+v", requests)
	}
}

func TestPeerPolicyToPolicy_EnqueuesAllOthers(t *testing.T) {
	scheme := newAutoPolicyScheme()
	a := samplePolicy("a")
	b := samplePolicy("b")
	c := samplePolicy("c")

	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(a, b, c).Build()
	r := newAutoPolicyReconciler(scheme, cl)
	requests := r.peerPolicyToPolicy(context.Background(), a)
	names := map[string]bool{}
	for _, req := range requests {
		names[req.Name] = true
	}
	if names["a"] {
		t.Error("peer mapFunc must not enqueue the triggering policy itself")
	}
	if !names["b"] || !names["c"] {
		t.Errorf("expected peers b and c to be enqueued, got %+v", names)
	}
}
