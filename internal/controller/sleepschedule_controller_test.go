package controller

import (
	"context"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	slumlordv1alpha1 "github.com/cschockaert/slumlord/api/v1alpha1"
)

func TestDaysToDisplay(t *testing.T) {
	tests := []struct {
		name string
		days []int
		want string
	}{
		{name: "nil days", days: nil, want: "Every day"},
		{name: "empty days", days: []int{}, want: "Every day"},
		{name: "weekdays", days: []int{1, 2, 3, 4, 5}, want: "Mon-Fri"},
		{name: "weekend", days: []int{0, 6}, want: "Sun,Sat"},
		{name: "non-consecutive", days: []int{1, 3, 5}, want: "Mon,Wed,Fri"},
		{name: "single day", days: []int{3}, want: "Wed"},
		{name: "two consecutive", days: []int{1, 2}, want: "Mon,Tue"},
		{name: "three consecutive", days: []int{1, 2, 3}, want: "Mon-Wed"},
		{name: "all days", days: []int{0, 1, 2, 3, 4, 5, 6}, want: "Sun-Sat"},
		{name: "mixed range and single", days: []int{1, 2, 3, 5}, want: "Mon-Wed,Fri"},
		{name: "unsorted input", days: []int{5, 1, 3}, want: "Mon,Wed,Fri"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := daysToDisplay(tt.days)
			if got != tt.want {
				t.Errorf("daysToDisplay(%v) = %q, want %q", tt.days, got, tt.want)
			}
		})
	}
}

func TestShouldBeSleepingAt(t *testing.T) {
	tests := []struct {
		name     string
		schedule slumlordv1alpha1.SleepWindow
		now      time.Time
		want     bool
	}{
		{
			name: "inside sleep window same day",
			schedule: slumlordv1alpha1.SleepWindow{
				Start:    "09:00",
				End:      "17:00",
				Timezone: "UTC",
			},
			now:  time.Date(2024, 1, 15, 12, 0, 0, 0, time.UTC), // Monday 12:00
			want: true,
		},
		{
			name: "outside sleep window same day - before",
			schedule: slumlordv1alpha1.SleepWindow{
				Start:    "09:00",
				End:      "17:00",
				Timezone: "UTC",
			},
			now:  time.Date(2024, 1, 15, 8, 0, 0, 0, time.UTC), // Monday 08:00
			want: false,
		},
		{
			name: "outside sleep window same day - after",
			schedule: slumlordv1alpha1.SleepWindow{
				Start:    "09:00",
				End:      "17:00",
				Timezone: "UTC",
			},
			now:  time.Date(2024, 1, 15, 18, 0, 0, 0, time.UTC), // Monday 18:00
			want: false,
		},
		{
			name: "overnight schedule - during night",
			schedule: slumlordv1alpha1.SleepWindow{
				Start:    "22:00",
				End:      "06:00",
				Timezone: "UTC",
			},
			now:  time.Date(2024, 1, 15, 23, 0, 0, 0, time.UTC), // 23:00
			want: true,
		},
		{
			name: "overnight schedule - during early morning",
			schedule: slumlordv1alpha1.SleepWindow{
				Start:    "22:00",
				End:      "06:00",
				Timezone: "UTC",
			},
			now:  time.Date(2024, 1, 15, 3, 0, 0, 0, time.UTC), // 03:00
			want: true,
		},
		{
			name: "overnight schedule - during day",
			schedule: slumlordv1alpha1.SleepWindow{
				Start:    "22:00",
				End:      "06:00",
				Timezone: "UTC",
			},
			now:  time.Date(2024, 1, 15, 12, 0, 0, 0, time.UTC), // 12:00
			want: false,
		},
		{
			name: "day filter - allowed day",
			schedule: slumlordv1alpha1.SleepWindow{
				Start:    "09:00",
				End:      "17:00",
				Timezone: "UTC",
				Days:     []int{1, 2, 3, 4, 5}, // weekdays
			},
			now:  time.Date(2024, 1, 15, 12, 0, 0, 0, time.UTC), // Monday
			want: true,
		},
		{
			name: "day filter - not allowed day",
			schedule: slumlordv1alpha1.SleepWindow{
				Start:    "09:00",
				End:      "17:00",
				Timezone: "UTC",
				Days:     []int{1, 2, 3, 4, 5}, // weekdays
			},
			now:  time.Date(2024, 1, 14, 12, 0, 0, 0, time.UTC), // Sunday
			want: false,
		},
		{
			name: "overnight with day filter - early morning next day should still sleep",
			schedule: slumlordv1alpha1.SleepWindow{
				Start:    "22:00",
				End:      "06:00",
				Timezone: "UTC",
				Days:     []int{1}, // Monday
			},
			now:  time.Date(2024, 1, 16, 3, 0, 0, 0, time.UTC), // Tuesday 03:00, sleep started Monday
			want: true,
		},
		{
			name: "overnight with day filter - evening of allowed day",
			schedule: slumlordv1alpha1.SleepWindow{
				Start:    "22:00",
				End:      "06:00",
				Timezone: "UTC",
				Days:     []int{1}, // Monday
			},
			now:  time.Date(2024, 1, 15, 23, 0, 0, 0, time.UTC), // Monday 23:00
			want: true,
		},
		{
			name: "overnight with day filter - early morning of non-allowed day (sleep didn't start previous day)",
			schedule: slumlordv1alpha1.SleepWindow{
				Start:    "22:00",
				End:      "06:00",
				Timezone: "UTC",
				Days:     []int{1}, // Monday
			},
			now:  time.Date(2024, 1, 18, 3, 0, 0, 0, time.UTC), // Thursday 03:00, Wednesday not in days
			want: false,
		},
		{
			name: "exact start time does not trigger",
			schedule: slumlordv1alpha1.SleepWindow{
				Start:    "22:00",
				End:      "06:00",
				Timezone: "UTC",
			},
			now:  time.Date(2024, 1, 15, 22, 0, 0, 0, time.UTC),
			want: false,
		},
		{
			name: "exact end time does not trigger",
			schedule: slumlordv1alpha1.SleepWindow{
				Start:    "22:00",
				End:      "06:00",
				Timezone: "UTC",
			},
			now:  time.Date(2024, 1, 15, 6, 0, 0, 0, time.UTC),
			want: false,
		},
		{
			name: "invalid timezone falls back to UTC",
			schedule: slumlordv1alpha1.SleepWindow{
				Start:    "09:00",
				End:      "17:00",
				Timezone: "Invalid/Zone",
			},
			now:  time.Date(2024, 1, 15, 12, 0, 0, 0, time.UTC),
			want: true,
		},
		{
			name: "invalid start time returns false",
			schedule: slumlordv1alpha1.SleepWindow{
				Start:    "invalid",
				End:      "17:00",
				Timezone: "UTC",
			},
			now:  time.Date(2024, 1, 15, 12, 0, 0, 0, time.UTC),
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &SleepScheduleReconciler{}
			schedule := &slumlordv1alpha1.SlumlordSleepSchedule{
				Spec: slumlordv1alpha1.SlumlordSleepScheduleSpec{
					Schedule: tt.schedule,
				},
			}

			got := r.shouldBeSleepingAt(schedule, tt.now)
			if got != tt.want {
				t.Errorf("shouldBeSleepingAt() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestShouldManageType(t *testing.T) {
	tests := []struct {
		name     string
		types    []string
		kind     string
		expected bool
	}{
		{
			name:     "empty types manages all",
			types:    nil,
			kind:     "Deployment",
			expected: true,
		},
		{
			name:     "explicit type match",
			types:    []string{"Deployment", "StatefulSet"},
			kind:     "Deployment",
			expected: true,
		},
		{
			name:     "explicit type no match",
			types:    []string{"StatefulSet"},
			kind:     "Deployment",
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &SleepScheduleReconciler{}
			schedule := &slumlordv1alpha1.SlumlordSleepSchedule{
				Spec: slumlordv1alpha1.SlumlordSleepScheduleSpec{
					Selector: slumlordv1alpha1.WorkloadSelector{
						Types: tt.types,
					},
				},
			}

			got := r.shouldManageType(schedule, tt.kind)
			if got != tt.expected {
				t.Errorf("shouldManageType() = %v, want %v", got, tt.expected)
			}
		})
	}
}

// neverSleepingSchedule returns a SleepWindow guaranteed to never be active,
// regardless of when the test runs. It picks a day 3 days from now (UTC) so
// it's never today or yesterday (overnight carry-over).
func neverSleepingSchedule() slumlordv1alpha1.SleepWindow {
	neverDay := (int(time.Now().UTC().Weekday()) + 3) % 7
	return slumlordv1alpha1.SleepWindow{
		Start:    "12:00",
		End:      "12:01",
		Timezone: "UTC",
		Days:     []int{neverDay},
	}
}

func newTestScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(s))
	utilruntime.Must(slumlordv1alpha1.AddToScheme(s))
	return s
}

func newCustomRESTMapper() meta.RESTMapper {
	mapper := meta.NewDefaultRESTMapper([]schema.GroupVersion{
		{Group: "", Version: "v1"},
		{Group: "apps", Version: "v1"},
		{Group: "batch", Version: "v1"},
		{Group: "slumlord.io", Version: "v1alpha1"},
		{Group: "postgresql.cnpg.io", Version: "v1"},
		{Group: "helm.toolkit.fluxcd.io", Version: "v2"},
		{Group: "kustomize.toolkit.fluxcd.io", Version: "v1"},
		{Group: "monitoring.coreos.com", Version: "v1"},
		{Group: "k8s.mariadb.com", Version: "v1alpha1"},
	})
	mapper.Add(schema.GroupVersionKind{Group: "postgresql.cnpg.io", Version: "v1", Kind: "Cluster"}, meta.RESTScopeNamespace)
	mapper.Add(schema.GroupVersionKind{Group: "helm.toolkit.fluxcd.io", Version: "v2", Kind: "HelmRelease"}, meta.RESTScopeNamespace)
	mapper.Add(schema.GroupVersionKind{Group: "kustomize.toolkit.fluxcd.io", Version: "v1", Kind: "Kustomization"}, meta.RESTScopeNamespace)
	mapper.Add(schema.GroupVersionKind{Group: "monitoring.coreos.com", Version: "v1", Kind: "ThanosRuler"}, meta.RESTScopeNamespace)
	mapper.Add(schema.GroupVersionKind{Group: "monitoring.coreos.com", Version: "v1", Kind: "Alertmanager"}, meta.RESTScopeNamespace)
	mapper.Add(schema.GroupVersionKind{Group: "monitoring.coreos.com", Version: "v1", Kind: "Prometheus"}, meta.RESTScopeNamespace)
	mapper.Add(schema.GroupVersionKind{Group: "k8s.mariadb.com", Version: "v1alpha1", Kind: "MariaDB"}, meta.RESTScopeNamespace)
	mapper.Add(schema.GroupVersionKind{Group: "k8s.mariadb.com", Version: "v1alpha1", Kind: "MaxScale"}, meta.RESTScopeNamespace)
	return mapper
}

func TestReconcile_ScalesDownDeployment(t *testing.T) {
	ctx := context.Background()
	scheme := newTestScheme()
	namespace := "test-namespace"

	// Create a deployment with 3 replicas
	replicas := int32(3)
	deploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-deploy",
			Namespace: namespace,
			Labels:    map[string]string{"app": "test"},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app": "test"},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{"app": "test"},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{Name: "test", Image: "nginx"},
					},
				},
			},
		},
	}

	// Create sleep schedule that's always active
	schedule := &slumlordv1alpha1.SlumlordSleepSchedule{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-schedule",
			Namespace: namespace,
		},
		Spec: slumlordv1alpha1.SlumlordSleepScheduleSpec{
			Selector: slumlordv1alpha1.WorkloadSelector{
				MatchLabels: map[string]string{"app": "test"},
				Types:       []string{"Deployment"},
			},
			Schedule: slumlordv1alpha1.SleepWindow{
				Start:    "00:00",
				End:      "23:59",
				Timezone: "UTC",
			},
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(deploy, schedule).
		WithStatusSubresource(schedule).
		Build()

	reconciler := &SleepScheduleReconciler{
		Client: fakeClient,
		Scheme: scheme,
	}

	// Reconcile
	_, err := reconciler.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{
			Name:      schedule.Name,
			Namespace: schedule.Namespace,
		},
	})
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	// Verify deployment scaled to 0
	var updatedDeploy appsv1.Deployment
	if err := fakeClient.Get(ctx, types.NamespacedName{Name: deploy.Name, Namespace: deploy.Namespace}, &updatedDeploy); err != nil {
		t.Fatalf("Failed to get deployment: %v", err)
	}
	if *updatedDeploy.Spec.Replicas != 0 {
		t.Errorf("Expected replicas = 0, got %d", *updatedDeploy.Spec.Replicas)
	}

	// Verify status updated
	var updatedSchedule slumlordv1alpha1.SlumlordSleepSchedule
	if err := fakeClient.Get(ctx, types.NamespacedName{Name: schedule.Name, Namespace: schedule.Namespace}, &updatedSchedule); err != nil {
		t.Fatalf("Failed to get schedule: %v", err)
	}
	if !updatedSchedule.Status.Sleeping {
		t.Error("Expected status.sleeping = true")
	}
	if len(updatedSchedule.Status.ManagedWorkloads) != 1 {
		t.Errorf("Expected 1 managed workload, got %d", len(updatedSchedule.Status.ManagedWorkloads))
	}
	if *updatedSchedule.Status.ManagedWorkloads[0].OriginalReplicas != 3 {
		t.Errorf("Expected original replicas = 3, got %d", *updatedSchedule.Status.ManagedWorkloads[0].OriginalReplicas)
	}
}

func TestReconcile_SuspendsCronJob(t *testing.T) {
	ctx := context.Background()
	scheme := newTestScheme()
	namespace := "test-namespace"

	// Create a cronjob
	suspend := false
	cj := &batchv1.CronJob{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-cronjob",
			Namespace: namespace,
			Labels:    map[string]string{"app": "test"},
		},
		Spec: batchv1.CronJobSpec{
			Schedule: "*/5 * * * *",
			Suspend:  &suspend,
			JobTemplate: batchv1.JobTemplateSpec{
				Spec: batchv1.JobSpec{
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							RestartPolicy: corev1.RestartPolicyOnFailure,
							Containers: []corev1.Container{
								{Name: "test", Image: "busybox"},
							},
						},
					},
				},
			},
		},
	}

	// Create sleep schedule
	schedule := &slumlordv1alpha1.SlumlordSleepSchedule{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-schedule",
			Namespace: namespace,
		},
		Spec: slumlordv1alpha1.SlumlordSleepScheduleSpec{
			Selector: slumlordv1alpha1.WorkloadSelector{
				MatchLabels: map[string]string{"app": "test"},
				Types:       []string{"CronJob"},
			},
			Schedule: slumlordv1alpha1.SleepWindow{
				Start:    "00:00",
				End:      "23:59",
				Timezone: "UTC",
			},
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(cj, schedule).
		WithStatusSubresource(schedule).
		Build()

	reconciler := &SleepScheduleReconciler{
		Client: fakeClient,
		Scheme: scheme,
	}

	// Reconcile
	_, err := reconciler.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{
			Name:      schedule.Name,
			Namespace: schedule.Namespace,
		},
	})
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	// Verify cronjob suspended
	var updatedCJ batchv1.CronJob
	if err := fakeClient.Get(ctx, types.NamespacedName{Name: cj.Name, Namespace: cj.Namespace}, &updatedCJ); err != nil {
		t.Fatalf("Failed to get cronjob: %v", err)
	}
	if !*updatedCJ.Spec.Suspend {
		t.Error("Expected cronjob to be suspended")
	}
}

func TestReconcile_HibernatesCNPGCluster(t *testing.T) {
	ctx := context.Background()
	scheme := newTestScheme()
	namespace := "test-namespace"

	// Create CNPG cluster as unstructured
	cluster := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "postgresql.cnpg.io/v1",
			"kind":       "Cluster",
			"metadata": map[string]interface{}{
				"name":      "test-pg-cluster",
				"namespace": namespace,
				"labels": map[string]interface{}{
					"app": "test",
				},
			},
			"spec": map[string]interface{}{
				"instances": int64(3),
			},
		},
	}

	// Create sleep schedule targeting CNPG Clusters
	schedule := &slumlordv1alpha1.SlumlordSleepSchedule{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-schedule",
			Namespace: namespace,
		},
		Spec: slumlordv1alpha1.SlumlordSleepScheduleSpec{
			Selector: slumlordv1alpha1.WorkloadSelector{
				MatchLabels: map[string]string{"app": "test"},
				Types:       []string{"Cluster"},
			},
			Schedule: slumlordv1alpha1.SleepWindow{
				Start:    "00:00",
				End:      "23:59",
				Timezone: "UTC",
			},
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithRESTMapper(newCustomRESTMapper()).
		WithObjects(cluster, schedule).
		WithStatusSubresource(schedule).
		Build()

	reconciler := &SleepScheduleReconciler{
		Client: fakeClient,
		Scheme: scheme,
	}

	// Reconcile
	_, err := reconciler.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{
			Name:      schedule.Name,
			Namespace: schedule.Namespace,
		},
	})
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	// Verify cluster has hibernation annotation
	updatedCluster := &unstructured.Unstructured{}
	updatedCluster.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "postgresql.cnpg.io",
		Version: "v1",
		Kind:    "Cluster",
	})
	if err := fakeClient.Get(ctx, types.NamespacedName{Name: "test-pg-cluster", Namespace: namespace}, updatedCluster); err != nil {
		t.Fatalf("Failed to get CNPG cluster: %v", err)
	}

	annotations := updatedCluster.GetAnnotations()
	if annotations == nil || annotations["cnpg.io/hibernation"] != "on" {
		t.Errorf("Expected cnpg.io/hibernation=on annotation, got %v", annotations)
	}

	// Verify status updated
	var updatedSchedule slumlordv1alpha1.SlumlordSleepSchedule
	if err := fakeClient.Get(ctx, types.NamespacedName{Name: schedule.Name, Namespace: schedule.Namespace}, &updatedSchedule); err != nil {
		t.Fatalf("Failed to get schedule: %v", err)
	}
	if !updatedSchedule.Status.Sleeping {
		t.Error("Expected status.sleeping = true")
	}
	if len(updatedSchedule.Status.ManagedWorkloads) != 1 {
		t.Errorf("Expected 1 managed workload, got %d", len(updatedSchedule.Status.ManagedWorkloads))
	}
	if updatedSchedule.Status.ManagedWorkloads[0].Kind != "Cluster" {
		t.Errorf("Expected managed workload kind = Cluster, got %s", updatedSchedule.Status.ManagedWorkloads[0].Kind)
	}
}

func TestReconcile_SuspendsFluxHelmRelease(t *testing.T) {
	ctx := context.Background()
	scheme := newTestScheme()
	namespace := "test-namespace"

	// Create FluxCD HelmRelease as unstructured
	hr := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "helm.toolkit.fluxcd.io/v2",
			"kind":       "HelmRelease",
			"metadata": map[string]interface{}{
				"name":      "test-helmrelease",
				"namespace": namespace,
				"labels": map[string]interface{}{
					"app": "test",
				},
			},
			"spec": map[string]interface{}{
				"interval": "5m",
				"chart": map[string]interface{}{
					"spec": map[string]interface{}{
						"chart":   "my-chart",
						"version": "1.0.0",
					},
				},
				"suspend": false,
			},
		},
	}

	// Create sleep schedule targeting HelmRelease
	schedule := &slumlordv1alpha1.SlumlordSleepSchedule{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-schedule",
			Namespace: namespace,
		},
		Spec: slumlordv1alpha1.SlumlordSleepScheduleSpec{
			Selector: slumlordv1alpha1.WorkloadSelector{
				MatchLabels: map[string]string{"app": "test"},
				Types:       []string{"HelmRelease"},
			},
			Schedule: slumlordv1alpha1.SleepWindow{
				Start:    "00:00",
				End:      "23:59",
				Timezone: "UTC",
			},
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithRESTMapper(newCustomRESTMapper()).
		WithObjects(hr, schedule).
		WithStatusSubresource(schedule).
		Build()

	reconciler := &SleepScheduleReconciler{
		Client: fakeClient,
		Scheme: scheme,
	}

	// Reconcile
	_, err := reconciler.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{
			Name:      schedule.Name,
			Namespace: schedule.Namespace,
		},
	})
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	// Verify HelmRelease is suspended
	updatedHR := &unstructured.Unstructured{}
	updatedHR.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "helm.toolkit.fluxcd.io",
		Version: "v2",
		Kind:    "HelmRelease",
	})
	if err := fakeClient.Get(ctx, types.NamespacedName{Name: "test-helmrelease", Namespace: namespace}, updatedHR); err != nil {
		t.Fatalf("Failed to get HelmRelease: %v", err)
	}

	spec, ok := updatedHR.Object["spec"].(map[string]interface{})
	if !ok {
		t.Fatal("Expected spec to be a map")
	}
	suspend, ok := spec["suspend"].(bool)
	if !ok || !suspend {
		t.Errorf("Expected spec.suspend = true, got %v", spec["suspend"])
	}

	// Verify status
	var updatedSchedule slumlordv1alpha1.SlumlordSleepSchedule
	if err := fakeClient.Get(ctx, types.NamespacedName{Name: schedule.Name, Namespace: schedule.Namespace}, &updatedSchedule); err != nil {
		t.Fatalf("Failed to get schedule: %v", err)
	}
	if !updatedSchedule.Status.Sleeping {
		t.Error("Expected status.sleeping = true")
	}
	if len(updatedSchedule.Status.ManagedWorkloads) != 1 {
		t.Errorf("Expected 1 managed workload, got %d", len(updatedSchedule.Status.ManagedWorkloads))
	}
	if updatedSchedule.Status.ManagedWorkloads[0].Kind != "HelmRelease" {
		t.Errorf("Expected kind = HelmRelease, got %s", updatedSchedule.Status.ManagedWorkloads[0].Kind)
	}
}

func TestReconcile_SuspendsFluxKustomization(t *testing.T) {
	ctx := context.Background()
	scheme := newTestScheme()
	namespace := "test-namespace"

	// Create FluxCD Kustomization as unstructured
	ks := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "kustomize.toolkit.fluxcd.io/v1",
			"kind":       "Kustomization",
			"metadata": map[string]interface{}{
				"name":      "test-kustomization",
				"namespace": namespace,
				"labels": map[string]interface{}{
					"app": "test",
				},
			},
			"spec": map[string]interface{}{
				"interval": "10m",
				"path":     "./clusters/production",
				"sourceRef": map[string]interface{}{
					"kind": "GitRepository",
					"name": "flux-system",
				},
				"suspend": false,
			},
		},
	}

	// Create sleep schedule targeting Kustomization
	schedule := &slumlordv1alpha1.SlumlordSleepSchedule{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-schedule",
			Namespace: namespace,
		},
		Spec: slumlordv1alpha1.SlumlordSleepScheduleSpec{
			Selector: slumlordv1alpha1.WorkloadSelector{
				MatchLabels: map[string]string{"app": "test"},
				Types:       []string{"Kustomization"},
			},
			Schedule: slumlordv1alpha1.SleepWindow{
				Start:    "00:00",
				End:      "23:59",
				Timezone: "UTC",
			},
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithRESTMapper(newCustomRESTMapper()).
		WithObjects(ks, schedule).
		WithStatusSubresource(schedule).
		Build()

	reconciler := &SleepScheduleReconciler{
		Client: fakeClient,
		Scheme: scheme,
	}

	// Reconcile
	_, err := reconciler.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{
			Name:      schedule.Name,
			Namespace: schedule.Namespace,
		},
	})
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	// Verify Kustomization is suspended
	updatedKS := &unstructured.Unstructured{}
	updatedKS.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "kustomize.toolkit.fluxcd.io",
		Version: "v1",
		Kind:    "Kustomization",
	})
	if err := fakeClient.Get(ctx, types.NamespacedName{Name: "test-kustomization", Namespace: namespace}, updatedKS); err != nil {
		t.Fatalf("Failed to get Kustomization: %v", err)
	}

	spec, ok := updatedKS.Object["spec"].(map[string]interface{})
	if !ok {
		t.Fatal("Expected spec to be a map")
	}
	suspend, ok := spec["suspend"].(bool)
	if !ok || !suspend {
		t.Errorf("Expected spec.suspend = true, got %v", spec["suspend"])
	}

	// Verify status
	var updatedSchedule slumlordv1alpha1.SlumlordSleepSchedule
	if err := fakeClient.Get(ctx, types.NamespacedName{Name: schedule.Name, Namespace: schedule.Namespace}, &updatedSchedule); err != nil {
		t.Fatalf("Failed to get schedule: %v", err)
	}
	if !updatedSchedule.Status.Sleeping {
		t.Error("Expected status.sleeping = true")
	}
	if len(updatedSchedule.Status.ManagedWorkloads) != 1 {
		t.Errorf("Expected 1 managed workload, got %d", len(updatedSchedule.Status.ManagedWorkloads))
	}
	if updatedSchedule.Status.ManagedWorkloads[0].Kind != "Kustomization" {
		t.Errorf("Expected kind = Kustomization, got %s", updatedSchedule.Status.ManagedWorkloads[0].Kind)
	}
}

func TestReconcile_WakesDeployment(t *testing.T) {
	ctx := context.Background()
	scheme := newTestScheme()
	namespace := "test-namespace"

	// Create a deployment scaled to 0 (sleeping state)
	zero := int32(0)
	deploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-deploy",
			Namespace: namespace,
			Labels:    map[string]string{"app": "test"},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &zero,
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app": "test"},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{"app": "test"},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{Name: "test", Image: "nginx"},
					},
				},
			},
		},
	}

	// Create schedule that is NOT in sleep window
	originalReplicas := int32(3)
	schedule := &slumlordv1alpha1.SlumlordSleepSchedule{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-schedule",
			Namespace: namespace,
		},
		Spec: slumlordv1alpha1.SlumlordSleepScheduleSpec{
			Selector: slumlordv1alpha1.WorkloadSelector{
				MatchLabels: map[string]string{"app": "test"},
				Types:       []string{"Deployment"},
			},
			Schedule: neverSleepingSchedule(),
		},
		Status: slumlordv1alpha1.SlumlordSleepScheduleStatus{
			Sleeping: true,
			ManagedWorkloads: []slumlordv1alpha1.ManagedWorkload{
				{
					Kind:             "Deployment",
					Name:             "test-deploy",
					OriginalReplicas: &originalReplicas,
				},
			},
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(deploy, schedule).
		WithStatusSubresource(schedule).
		Build()

	// Pre-set the status on the fake client (status subresource requires separate update)
	if err := fakeClient.Status().Update(ctx, schedule); err != nil {
		t.Fatalf("Failed to set initial status: %v", err)
	}

	reconciler := &SleepScheduleReconciler{
		Client: fakeClient,
		Scheme: scheme,
	}

	// Reconcile
	_, err := reconciler.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{
			Name:      schedule.Name,
			Namespace: schedule.Namespace,
		},
	})
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	// Verify deployment is scaled back to 3
	var updatedDeploy appsv1.Deployment
	if err := fakeClient.Get(ctx, types.NamespacedName{Name: deploy.Name, Namespace: deploy.Namespace}, &updatedDeploy); err != nil {
		t.Fatalf("Failed to get deployment: %v", err)
	}
	if *updatedDeploy.Spec.Replicas != 3 {
		t.Errorf("Expected replicas = 3, got %d", *updatedDeploy.Spec.Replicas)
	}

	// Verify status.Sleeping = false and managedWorkloads is cleared
	var updatedSchedule slumlordv1alpha1.SlumlordSleepSchedule
	if err := fakeClient.Get(ctx, types.NamespacedName{Name: schedule.Name, Namespace: schedule.Namespace}, &updatedSchedule); err != nil {
		t.Fatalf("Failed to get schedule: %v", err)
	}
	if updatedSchedule.Status.Sleeping {
		t.Error("Expected status.sleeping = false")
	}
	if len(updatedSchedule.Status.ManagedWorkloads) != 0 {
		t.Errorf("Expected 0 managed workloads, got %d", len(updatedSchedule.Status.ManagedWorkloads))
	}
}

func TestReconcile_FullLifecycle(t *testing.T) {
	ctx := context.Background()
	scheme := newTestScheme()
	namespace := "test-namespace"

	// Create a deployment with 3 replicas
	replicas := int32(3)
	deploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-deploy",
			Namespace: namespace,
			Labels:    map[string]string{"app": "test"},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app": "test"},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{"app": "test"},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{Name: "test", Image: "nginx"},
					},
				},
			},
		},
	}

	// Create schedule that IS sleeping (covers almost the whole day)
	schedule := &slumlordv1alpha1.SlumlordSleepSchedule{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-schedule",
			Namespace: namespace,
		},
		Spec: slumlordv1alpha1.SlumlordSleepScheduleSpec{
			Selector: slumlordv1alpha1.WorkloadSelector{
				MatchLabels: map[string]string{"app": "test"},
				Types:       []string{"Deployment"},
			},
			Schedule: slumlordv1alpha1.SleepWindow{
				Start:    "00:00",
				End:      "23:59",
				Timezone: "UTC",
			},
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(deploy, schedule).
		WithStatusSubresource(schedule).
		Build()

	reconciler := &SleepScheduleReconciler{
		Client: fakeClient,
		Scheme: scheme,
	}

	nn := types.NamespacedName{Name: schedule.Name, Namespace: schedule.Namespace}

	// First reconcile: should sleep
	_, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: nn})
	if err != nil {
		t.Fatalf("First Reconcile() error = %v", err)
	}

	// Verify deployment scaled to 0
	var updatedDeploy appsv1.Deployment
	if err := fakeClient.Get(ctx, types.NamespacedName{Name: deploy.Name, Namespace: deploy.Namespace}, &updatedDeploy); err != nil {
		t.Fatalf("Failed to get deployment after sleep: %v", err)
	}
	if *updatedDeploy.Spec.Replicas != 0 {
		t.Errorf("After sleep: expected replicas = 0, got %d", *updatedDeploy.Spec.Replicas)
	}

	// Verify status.Sleeping = true
	var updatedSchedule slumlordv1alpha1.SlumlordSleepSchedule
	if err := fakeClient.Get(ctx, nn, &updatedSchedule); err != nil {
		t.Fatalf("Failed to get schedule after sleep: %v", err)
	}
	if !updatedSchedule.Status.Sleeping {
		t.Error("After sleep: expected status.sleeping = true")
	}

	// Change schedule to a window that is NOT sleeping
	wakeSchedule := neverSleepingSchedule()
	updatedSchedule.Spec.Schedule = wakeSchedule
	if err := fakeClient.Update(ctx, &updatedSchedule); err != nil {
		t.Fatalf("Failed to update schedule spec: %v", err)
	}

	// Second reconcile: should wake
	_, err = reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: nn})
	if err != nil {
		t.Fatalf("Second Reconcile() error = %v", err)
	}

	// Verify deployment restored to 3
	if err := fakeClient.Get(ctx, types.NamespacedName{Name: deploy.Name, Namespace: deploy.Namespace}, &updatedDeploy); err != nil {
		t.Fatalf("Failed to get deployment after wake: %v", err)
	}
	if *updatedDeploy.Spec.Replicas != 3 {
		t.Errorf("After wake: expected replicas = 3, got %d", *updatedDeploy.Spec.Replicas)
	}

	// Verify status.Sleeping = false
	if err := fakeClient.Get(ctx, nn, &updatedSchedule); err != nil {
		t.Fatalf("Failed to get schedule after wake: %v", err)
	}
	if updatedSchedule.Status.Sleeping {
		t.Error("After wake: expected status.sleeping = false")
	}
}

func TestReconcile_IdempotentSleep(t *testing.T) {
	ctx := context.Background()
	scheme := newTestScheme()
	namespace := "test-namespace"

	// Create a deployment already at 0 replicas
	zero := int32(0)
	deploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-deploy",
			Namespace: namespace,
			Labels:    map[string]string{"app": "test"},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &zero,
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app": "test"},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{"app": "test"},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{Name: "test", Image: "nginx"},
					},
				},
			},
		},
	}

	// Create schedule that's already sleeping with managedWorkloads populated
	originalReplicas := int32(3)
	schedule := &slumlordv1alpha1.SlumlordSleepSchedule{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-schedule",
			Namespace: namespace,
		},
		Spec: slumlordv1alpha1.SlumlordSleepScheduleSpec{
			Selector: slumlordv1alpha1.WorkloadSelector{
				MatchLabels: map[string]string{"app": "test"},
				Types:       []string{"Deployment"},
			},
			Schedule: slumlordv1alpha1.SleepWindow{
				Start:    "00:00",
				End:      "23:59",
				Timezone: "UTC",
			},
		},
		Status: slumlordv1alpha1.SlumlordSleepScheduleStatus{
			Sleeping: true,
			ManagedWorkloads: []slumlordv1alpha1.ManagedWorkload{
				{
					Kind:             "Deployment",
					Name:             "test-deploy",
					OriginalReplicas: &originalReplicas,
				},
			},
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(deploy, schedule).
		WithStatusSubresource(schedule).
		Build()

	// Pre-set the status on the fake client
	if err := fakeClient.Status().Update(ctx, schedule); err != nil {
		t.Fatalf("Failed to set initial status: %v", err)
	}

	reconciler := &SleepScheduleReconciler{
		Client: fakeClient,
		Scheme: scheme,
	}

	// Reconcile
	_, err := reconciler.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{
			Name:      schedule.Name,
			Namespace: schedule.Namespace,
		},
	})
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	// Verify deployment is still at 0 replicas
	var updatedDeploy appsv1.Deployment
	if err := fakeClient.Get(ctx, types.NamespacedName{Name: deploy.Name, Namespace: deploy.Namespace}, &updatedDeploy); err != nil {
		t.Fatalf("Failed to get deployment: %v", err)
	}
	if *updatedDeploy.Spec.Replicas != 0 {
		t.Errorf("Expected replicas = 0 (unchanged), got %d", *updatedDeploy.Spec.Replicas)
	}

	// Verify status unchanged: still sleeping with same managedWorkloads
	var updatedSchedule slumlordv1alpha1.SlumlordSleepSchedule
	if err := fakeClient.Get(ctx, types.NamespacedName{Name: schedule.Name, Namespace: schedule.Namespace}, &updatedSchedule); err != nil {
		t.Fatalf("Failed to get schedule: %v", err)
	}
	if !updatedSchedule.Status.Sleeping {
		t.Error("Expected status.sleeping = true (unchanged)")
	}
	if len(updatedSchedule.Status.ManagedWorkloads) != 1 {
		t.Errorf("Expected 1 managed workload (unchanged), got %d", len(updatedSchedule.Status.ManagedWorkloads))
	}
	if *updatedSchedule.Status.ManagedWorkloads[0].OriginalReplicas != 3 {
		t.Errorf("Expected originalReplicas = 3 (unchanged), got %d", *updatedSchedule.Status.ManagedWorkloads[0].OriginalReplicas)
	}
}

func TestReconcile_ScalesDownStatefulSet(t *testing.T) {
	ctx := context.Background()
	scheme := newTestScheme()
	namespace := "test-namespace"

	replicas := int32(3)
	sts := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-sts",
			Namespace: namespace,
			Labels:    map[string]string{"app": "test"},
		},
		Spec: appsv1.StatefulSetSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app": "test"},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{"app": "test"},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{Name: "test", Image: "nginx"},
					},
				},
			},
		},
	}

	schedule := &slumlordv1alpha1.SlumlordSleepSchedule{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-schedule",
			Namespace: namespace,
		},
		Spec: slumlordv1alpha1.SlumlordSleepScheduleSpec{
			Selector: slumlordv1alpha1.WorkloadSelector{
				MatchLabels: map[string]string{"app": "test"},
				Types:       []string{"StatefulSet"},
			},
			Schedule: slumlordv1alpha1.SleepWindow{
				Start:    "00:00",
				End:      "23:59",
				Timezone: "UTC",
			},
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(sts, schedule).
		WithStatusSubresource(schedule).
		Build()

	reconciler := &SleepScheduleReconciler{Client: fakeClient, Scheme: scheme}

	_, err := reconciler.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Name: schedule.Name, Namespace: schedule.Namespace},
	})
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	var updatedSts appsv1.StatefulSet
	if err := fakeClient.Get(ctx, types.NamespacedName{Name: sts.Name, Namespace: sts.Namespace}, &updatedSts); err != nil {
		t.Fatalf("Failed to get statefulset: %v", err)
	}
	if *updatedSts.Spec.Replicas != 0 {
		t.Errorf("Expected replicas = 0, got %d", *updatedSts.Spec.Replicas)
	}

	var updatedSchedule slumlordv1alpha1.SlumlordSleepSchedule
	if err := fakeClient.Get(ctx, types.NamespacedName{Name: schedule.Name, Namespace: schedule.Namespace}, &updatedSchedule); err != nil {
		t.Fatalf("Failed to get schedule: %v", err)
	}
	if !updatedSchedule.Status.Sleeping {
		t.Error("Expected status.sleeping = true")
	}
	if len(updatedSchedule.Status.ManagedWorkloads) != 1 {
		t.Errorf("Expected 1 managed workload, got %d", len(updatedSchedule.Status.ManagedWorkloads))
	}
	if *updatedSchedule.Status.ManagedWorkloads[0].OriginalReplicas != 3 {
		t.Errorf("Expected original replicas = 3, got %d", *updatedSchedule.Status.ManagedWorkloads[0].OriginalReplicas)
	}
}

func TestReconcile_WakesStatefulSet(t *testing.T) {
	ctx := context.Background()
	scheme := newTestScheme()
	namespace := "test-namespace"

	zero := int32(0)
	sts := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-sts",
			Namespace: namespace,
			Labels:    map[string]string{"app": "test"},
		},
		Spec: appsv1.StatefulSetSpec{
			Replicas: &zero,
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app": "test"},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "test"}},
				Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "test", Image: "nginx"}}},
			},
		},
	}

	originalReplicas := int32(3)
	schedule := &slumlordv1alpha1.SlumlordSleepSchedule{
		ObjectMeta: metav1.ObjectMeta{Name: "test-schedule", Namespace: namespace},
		Spec: slumlordv1alpha1.SlumlordSleepScheduleSpec{
			Selector: slumlordv1alpha1.WorkloadSelector{
				MatchLabels: map[string]string{"app": "test"},
				Types:       []string{"StatefulSet"},
			},
			Schedule: neverSleepingSchedule(),
		},
		Status: slumlordv1alpha1.SlumlordSleepScheduleStatus{
			Sleeping: true,
			ManagedWorkloads: []slumlordv1alpha1.ManagedWorkload{
				{Kind: "StatefulSet", Name: "test-sts", OriginalReplicas: &originalReplicas},
			},
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(sts, schedule).
		WithStatusSubresource(schedule).
		Build()
	if err := fakeClient.Status().Update(ctx, schedule); err != nil {
		t.Fatalf("Failed to set initial status: %v", err)
	}

	reconciler := &SleepScheduleReconciler{Client: fakeClient, Scheme: scheme}
	_, err := reconciler.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Name: schedule.Name, Namespace: schedule.Namespace},
	})
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	var updatedSts appsv1.StatefulSet
	if err := fakeClient.Get(ctx, types.NamespacedName{Name: sts.Name, Namespace: sts.Namespace}, &updatedSts); err != nil {
		t.Fatalf("Failed to get statefulset: %v", err)
	}
	if *updatedSts.Spec.Replicas != 3 {
		t.Errorf("Expected replicas = 3, got %d", *updatedSts.Spec.Replicas)
	}
}

func TestReconcile_WakesCronJob(t *testing.T) {
	ctx := context.Background()
	scheme := newTestScheme()
	namespace := "test-namespace"

	suspended := true
	cj := &batchv1.CronJob{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-cronjob",
			Namespace: namespace,
			Labels:    map[string]string{"app": "test"},
		},
		Spec: batchv1.CronJobSpec{
			Schedule: "*/5 * * * *",
			Suspend:  &suspended,
			JobTemplate: batchv1.JobTemplateSpec{
				Spec: batchv1.JobSpec{
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							RestartPolicy: corev1.RestartPolicyOnFailure,
							Containers:    []corev1.Container{{Name: "test", Image: "busybox"}},
						},
					},
				},
			},
		},
	}

	originalSuspend := false
	schedule := &slumlordv1alpha1.SlumlordSleepSchedule{
		ObjectMeta: metav1.ObjectMeta{Name: "test-schedule", Namespace: namespace},
		Spec: slumlordv1alpha1.SlumlordSleepScheduleSpec{
			Selector: slumlordv1alpha1.WorkloadSelector{
				MatchLabels: map[string]string{"app": "test"},
				Types:       []string{"CronJob"},
			},
			Schedule: neverSleepingSchedule(),
		},
		Status: slumlordv1alpha1.SlumlordSleepScheduleStatus{
			Sleeping: true,
			ManagedWorkloads: []slumlordv1alpha1.ManagedWorkload{
				{Kind: "CronJob", Name: "test-cronjob", OriginalSuspend: &originalSuspend},
			},
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(cj, schedule).
		WithStatusSubresource(schedule).
		Build()
	if err := fakeClient.Status().Update(ctx, schedule); err != nil {
		t.Fatalf("Failed to set initial status: %v", err)
	}

	reconciler := &SleepScheduleReconciler{Client: fakeClient, Scheme: scheme}
	_, err := reconciler.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Name: schedule.Name, Namespace: schedule.Namespace},
	})
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	var updatedCJ batchv1.CronJob
	if err := fakeClient.Get(ctx, types.NamespacedName{Name: cj.Name, Namespace: cj.Namespace}, &updatedCJ); err != nil {
		t.Fatalf("Failed to get cronjob: %v", err)
	}
	if *updatedCJ.Spec.Suspend != false {
		t.Error("Expected cronjob to be unsuspended")
	}
}

func TestReconcile_WakesCNPGCluster(t *testing.T) {
	ctx := context.Background()
	scheme := newTestScheme()
	namespace := "test-namespace"

	cluster := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "postgresql.cnpg.io/v1",
			"kind":       "Cluster",
			"metadata": map[string]interface{}{
				"name":      "test-pg-cluster",
				"namespace": namespace,
				"labels":    map[string]interface{}{"app": "test"},
				"annotations": map[string]interface{}{
					"cnpg.io/hibernation": "on",
				},
			},
			"spec": map[string]interface{}{"instances": int64(3)},
		},
	}

	originalHibernation := ""
	schedule := &slumlordv1alpha1.SlumlordSleepSchedule{
		ObjectMeta: metav1.ObjectMeta{Name: "test-schedule", Namespace: namespace},
		Spec: slumlordv1alpha1.SlumlordSleepScheduleSpec{
			Selector: slumlordv1alpha1.WorkloadSelector{
				MatchLabels: map[string]string{"app": "test"},
				Types:       []string{"Cluster"},
			},
			Schedule: neverSleepingSchedule(),
		},
		Status: slumlordv1alpha1.SlumlordSleepScheduleStatus{
			Sleeping: true,
			ManagedWorkloads: []slumlordv1alpha1.ManagedWorkload{
				{Kind: "Cluster", Name: "test-pg-cluster", OriginalHibernation: &originalHibernation},
			},
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithRESTMapper(newCustomRESTMapper()).
		WithObjects(cluster, schedule).
		WithStatusSubresource(schedule).
		Build()
	if err := fakeClient.Status().Update(ctx, schedule); err != nil {
		t.Fatalf("Failed to set initial status: %v", err)
	}

	reconciler := &SleepScheduleReconciler{Client: fakeClient, Scheme: scheme}
	_, err := reconciler.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Name: schedule.Name, Namespace: schedule.Namespace},
	})
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	updatedCluster := &unstructured.Unstructured{}
	updatedCluster.SetGroupVersionKind(schema.GroupVersionKind{Group: "postgresql.cnpg.io", Version: "v1", Kind: "Cluster"})
	if err := fakeClient.Get(ctx, types.NamespacedName{Name: "test-pg-cluster", Namespace: namespace}, updatedCluster); err != nil {
		t.Fatalf("Failed to get CNPG cluster: %v", err)
	}
	annotations := updatedCluster.GetAnnotations()
	if _, exists := annotations["cnpg.io/hibernation"]; exists {
		t.Errorf("Expected cnpg.io/hibernation annotation to be removed, got %v", annotations)
	}
}

func TestReconcile_WakesFluxHelmRelease(t *testing.T) {
	ctx := context.Background()
	scheme := newTestScheme()
	namespace := "test-namespace"

	hr := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "helm.toolkit.fluxcd.io/v2",
			"kind":       "HelmRelease",
			"metadata": map[string]interface{}{
				"name":      "test-helmrelease",
				"namespace": namespace,
				"labels":    map[string]interface{}{"app": "test"},
			},
			"spec": map[string]interface{}{
				"interval": "5m",
				"chart": map[string]interface{}{
					"spec": map[string]interface{}{"chart": "my-chart", "version": "1.0.0"},
				},
				"suspend": true,
			},
		},
	}

	originalSuspend := false
	schedule := &slumlordv1alpha1.SlumlordSleepSchedule{
		ObjectMeta: metav1.ObjectMeta{Name: "test-schedule", Namespace: namespace},
		Spec: slumlordv1alpha1.SlumlordSleepScheduleSpec{
			Selector: slumlordv1alpha1.WorkloadSelector{
				MatchLabels: map[string]string{"app": "test"},
				Types:       []string{"HelmRelease"},
			},
			Schedule: neverSleepingSchedule(),
		},
		Status: slumlordv1alpha1.SlumlordSleepScheduleStatus{
			Sleeping: true,
			ManagedWorkloads: []slumlordv1alpha1.ManagedWorkload{
				{Kind: "HelmRelease", Name: "test-helmrelease", OriginalSuspend: &originalSuspend},
			},
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithRESTMapper(newCustomRESTMapper()).
		WithObjects(hr, schedule).
		WithStatusSubresource(schedule).
		Build()
	if err := fakeClient.Status().Update(ctx, schedule); err != nil {
		t.Fatalf("Failed to set initial status: %v", err)
	}

	reconciler := &SleepScheduleReconciler{Client: fakeClient, Scheme: scheme}
	_, err := reconciler.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Name: schedule.Name, Namespace: schedule.Namespace},
	})
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	updatedHR := &unstructured.Unstructured{}
	updatedHR.SetGroupVersionKind(schema.GroupVersionKind{Group: "helm.toolkit.fluxcd.io", Version: "v2", Kind: "HelmRelease"})
	if err := fakeClient.Get(ctx, types.NamespacedName{Name: "test-helmrelease", Namespace: namespace}, updatedHR); err != nil {
		t.Fatalf("Failed to get HelmRelease: %v", err)
	}
	spec := updatedHR.Object["spec"].(map[string]interface{})
	if suspend, ok := spec["suspend"].(bool); ok && suspend {
		t.Error("Expected HelmRelease to be unsuspended")
	}
}

func TestReconcile_WakesFluxKustomization(t *testing.T) {
	ctx := context.Background()
	scheme := newTestScheme()
	namespace := "test-namespace"

	ks := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "kustomize.toolkit.fluxcd.io/v1",
			"kind":       "Kustomization",
			"metadata": map[string]interface{}{
				"name":      "test-kustomization",
				"namespace": namespace,
				"labels":    map[string]interface{}{"app": "test"},
			},
			"spec": map[string]interface{}{
				"interval":  "10m",
				"path":      "./clusters/production",
				"sourceRef": map[string]interface{}{"kind": "GitRepository", "name": "flux-system"},
				"suspend":   true,
			},
		},
	}

	originalSuspend := false
	schedule := &slumlordv1alpha1.SlumlordSleepSchedule{
		ObjectMeta: metav1.ObjectMeta{Name: "test-schedule", Namespace: namespace},
		Spec: slumlordv1alpha1.SlumlordSleepScheduleSpec{
			Selector: slumlordv1alpha1.WorkloadSelector{
				MatchLabels: map[string]string{"app": "test"},
				Types:       []string{"Kustomization"},
			},
			Schedule: neverSleepingSchedule(),
		},
		Status: slumlordv1alpha1.SlumlordSleepScheduleStatus{
			Sleeping: true,
			ManagedWorkloads: []slumlordv1alpha1.ManagedWorkload{
				{Kind: "Kustomization", Name: "test-kustomization", OriginalSuspend: &originalSuspend},
			},
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithRESTMapper(newCustomRESTMapper()).
		WithObjects(ks, schedule).
		WithStatusSubresource(schedule).
		Build()
	if err := fakeClient.Status().Update(ctx, schedule); err != nil {
		t.Fatalf("Failed to set initial status: %v", err)
	}

	reconciler := &SleepScheduleReconciler{Client: fakeClient, Scheme: scheme}
	_, err := reconciler.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Name: schedule.Name, Namespace: schedule.Namespace},
	})
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	updatedKS := &unstructured.Unstructured{}
	updatedKS.SetGroupVersionKind(schema.GroupVersionKind{Group: "kustomize.toolkit.fluxcd.io", Version: "v1", Kind: "Kustomization"})
	if err := fakeClient.Get(ctx, types.NamespacedName{Name: "test-kustomization", Namespace: namespace}, updatedKS); err != nil {
		t.Fatalf("Failed to get Kustomization: %v", err)
	}
	spec := updatedKS.Object["spec"].(map[string]interface{})
	if suspend, ok := spec["suspend"].(bool); ok && suspend {
		t.Error("Expected Kustomization to be unsuspended")
	}
}

func TestReconcile_ScalesDownThanosRuler(t *testing.T) {
	ctx := context.Background()
	scheme := newTestScheme()
	namespace := "test-namespace"

	tr := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "monitoring.coreos.com/v1",
			"kind":       "ThanosRuler",
			"metadata": map[string]interface{}{
				"name":      "test-thanos-ruler",
				"namespace": namespace,
				"labels":    map[string]interface{}{"app": "test"},
			},
			"spec": map[string]interface{}{
				"replicas": int64(2),
			},
		},
	}

	schedule := &slumlordv1alpha1.SlumlordSleepSchedule{
		ObjectMeta: metav1.ObjectMeta{Name: "test-schedule", Namespace: namespace},
		Spec: slumlordv1alpha1.SlumlordSleepScheduleSpec{
			Selector: slumlordv1alpha1.WorkloadSelector{
				MatchLabels: map[string]string{"app": "test"},
				Types:       []string{"ThanosRuler"},
			},
			Schedule: slumlordv1alpha1.SleepWindow{Start: "00:00", End: "23:59", Timezone: "UTC"},
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithRESTMapper(newCustomRESTMapper()).
		WithObjects(tr, schedule).
		WithStatusSubresource(schedule).
		Build()

	reconciler := &SleepScheduleReconciler{Client: fakeClient, Scheme: scheme}
	_, err := reconciler.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Name: schedule.Name, Namespace: schedule.Namespace},
	})
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	updated := &unstructured.Unstructured{}
	updated.SetGroupVersionKind(schema.GroupVersionKind{Group: "monitoring.coreos.com", Version: "v1", Kind: "ThanosRuler"})
	if err := fakeClient.Get(ctx, types.NamespacedName{Name: "test-thanos-ruler", Namespace: namespace}, updated); err != nil {
		t.Fatalf("Failed to get ThanosRuler: %v", err)
	}
	spec := updated.Object["spec"].(map[string]interface{})
	if replicas, ok := spec["replicas"].(int64); !ok || replicas != 0 {
		t.Errorf("Expected spec.replicas = 0, got %v", spec["replicas"])
	}

	var updatedSchedule slumlordv1alpha1.SlumlordSleepSchedule
	if err := fakeClient.Get(ctx, types.NamespacedName{Name: schedule.Name, Namespace: schedule.Namespace}, &updatedSchedule); err != nil {
		t.Fatalf("Failed to get schedule: %v", err)
	}
	if !updatedSchedule.Status.Sleeping {
		t.Error("Expected status.sleeping = true")
	}
	if len(updatedSchedule.Status.ManagedWorkloads) != 1 {
		t.Errorf("Expected 1 managed workload, got %d", len(updatedSchedule.Status.ManagedWorkloads))
	}
	if updatedSchedule.Status.ManagedWorkloads[0].Kind != "ThanosRuler" {
		t.Errorf("Expected kind = ThanosRuler, got %s", updatedSchedule.Status.ManagedWorkloads[0].Kind)
	}
	if *updatedSchedule.Status.ManagedWorkloads[0].OriginalReplicas != 2 {
		t.Errorf("Expected original replicas = 2, got %d", *updatedSchedule.Status.ManagedWorkloads[0].OriginalReplicas)
	}
}

func TestReconcile_ScalesDownAlertmanager(t *testing.T) {
	ctx := context.Background()
	scheme := newTestScheme()
	namespace := "test-namespace"

	am := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "monitoring.coreos.com/v1",
			"kind":       "Alertmanager",
			"metadata": map[string]interface{}{
				"name":      "test-alertmanager",
				"namespace": namespace,
				"labels":    map[string]interface{}{"app": "test"},
			},
			"spec": map[string]interface{}{
				"replicas": int64(3),
			},
		},
	}

	schedule := &slumlordv1alpha1.SlumlordSleepSchedule{
		ObjectMeta: metav1.ObjectMeta{Name: "test-schedule", Namespace: namespace},
		Spec: slumlordv1alpha1.SlumlordSleepScheduleSpec{
			Selector: slumlordv1alpha1.WorkloadSelector{
				MatchLabels: map[string]string{"app": "test"},
				Types:       []string{"Alertmanager"},
			},
			Schedule: slumlordv1alpha1.SleepWindow{Start: "00:00", End: "23:59", Timezone: "UTC"},
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithRESTMapper(newCustomRESTMapper()).
		WithObjects(am, schedule).
		WithStatusSubresource(schedule).
		Build()

	reconciler := &SleepScheduleReconciler{Client: fakeClient, Scheme: scheme}
	_, err := reconciler.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Name: schedule.Name, Namespace: schedule.Namespace},
	})
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	updated := &unstructured.Unstructured{}
	updated.SetGroupVersionKind(schema.GroupVersionKind{Group: "monitoring.coreos.com", Version: "v1", Kind: "Alertmanager"})
	if err := fakeClient.Get(ctx, types.NamespacedName{Name: "test-alertmanager", Namespace: namespace}, updated); err != nil {
		t.Fatalf("Failed to get Alertmanager: %v", err)
	}
	spec := updated.Object["spec"].(map[string]interface{})
	if replicas, ok := spec["replicas"].(int64); !ok || replicas != 0 {
		t.Errorf("Expected spec.replicas = 0, got %v", spec["replicas"])
	}

	var updatedSchedule slumlordv1alpha1.SlumlordSleepSchedule
	if err := fakeClient.Get(ctx, types.NamespacedName{Name: schedule.Name, Namespace: schedule.Namespace}, &updatedSchedule); err != nil {
		t.Fatalf("Failed to get schedule: %v", err)
	}
	if updatedSchedule.Status.ManagedWorkloads[0].Kind != "Alertmanager" {
		t.Errorf("Expected kind = Alertmanager, got %s", updatedSchedule.Status.ManagedWorkloads[0].Kind)
	}
	if *updatedSchedule.Status.ManagedWorkloads[0].OriginalReplicas != 3 {
		t.Errorf("Expected original replicas = 3, got %d", *updatedSchedule.Status.ManagedWorkloads[0].OriginalReplicas)
	}
}

func TestReconcile_ScalesDownPrometheus(t *testing.T) {
	ctx := context.Background()
	scheme := newTestScheme()
	namespace := "test-namespace"

	prom := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "monitoring.coreos.com/v1",
			"kind":       "Prometheus",
			"metadata": map[string]interface{}{
				"name":      "test-prometheus",
				"namespace": namespace,
				"labels":    map[string]interface{}{"app": "test"},
			},
			"spec": map[string]interface{}{
				"replicas": int64(2),
			},
		},
	}

	schedule := &slumlordv1alpha1.SlumlordSleepSchedule{
		ObjectMeta: metav1.ObjectMeta{Name: "test-schedule", Namespace: namespace},
		Spec: slumlordv1alpha1.SlumlordSleepScheduleSpec{
			Selector: slumlordv1alpha1.WorkloadSelector{
				MatchLabels: map[string]string{"app": "test"},
				Types:       []string{"Prometheus"},
			},
			Schedule: slumlordv1alpha1.SleepWindow{Start: "00:00", End: "23:59", Timezone: "UTC"},
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithRESTMapper(newCustomRESTMapper()).
		WithObjects(prom, schedule).
		WithStatusSubresource(schedule).
		Build()

	reconciler := &SleepScheduleReconciler{Client: fakeClient, Scheme: scheme}
	_, err := reconciler.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Name: schedule.Name, Namespace: schedule.Namespace},
	})
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	updated := &unstructured.Unstructured{}
	updated.SetGroupVersionKind(schema.GroupVersionKind{Group: "monitoring.coreos.com", Version: "v1", Kind: "Prometheus"})
	if err := fakeClient.Get(ctx, types.NamespacedName{Name: "test-prometheus", Namespace: namespace}, updated); err != nil {
		t.Fatalf("Failed to get Prometheus: %v", err)
	}
	spec := updated.Object["spec"].(map[string]interface{})
	if replicas, ok := spec["replicas"].(int64); !ok || replicas != 0 {
		t.Errorf("Expected spec.replicas = 0, got %v", spec["replicas"])
	}

	var updatedSchedule slumlordv1alpha1.SlumlordSleepSchedule
	if err := fakeClient.Get(ctx, types.NamespacedName{Name: schedule.Name, Namespace: schedule.Namespace}, &updatedSchedule); err != nil {
		t.Fatalf("Failed to get schedule: %v", err)
	}
	if updatedSchedule.Status.ManagedWorkloads[0].Kind != "Prometheus" {
		t.Errorf("Expected kind = Prometheus, got %s", updatedSchedule.Status.ManagedWorkloads[0].Kind)
	}
	if *updatedSchedule.Status.ManagedWorkloads[0].OriginalReplicas != 2 {
		t.Errorf("Expected original replicas = 2, got %d", *updatedSchedule.Status.ManagedWorkloads[0].OriginalReplicas)
	}
}

func TestReconcile_WakesThanosRuler(t *testing.T) {
	ctx := context.Background()
	scheme := newTestScheme()
	namespace := "test-namespace"

	tr := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "monitoring.coreos.com/v1",
			"kind":       "ThanosRuler",
			"metadata": map[string]interface{}{
				"name":      "test-thanos-ruler",
				"namespace": namespace,
				"labels":    map[string]interface{}{"app": "test"},
			},
			"spec": map[string]interface{}{
				"replicas": int64(0),
			},
		},
	}

	originalReplicas := int32(2)
	schedule := &slumlordv1alpha1.SlumlordSleepSchedule{
		ObjectMeta: metav1.ObjectMeta{Name: "test-schedule", Namespace: namespace},
		Spec: slumlordv1alpha1.SlumlordSleepScheduleSpec{
			Selector: slumlordv1alpha1.WorkloadSelector{
				MatchLabels: map[string]string{"app": "test"},
				Types:       []string{"ThanosRuler"},
			},
			Schedule: neverSleepingSchedule(),
		},
		Status: slumlordv1alpha1.SlumlordSleepScheduleStatus{
			Sleeping: true,
			ManagedWorkloads: []slumlordv1alpha1.ManagedWorkload{
				{Kind: "ThanosRuler", Name: "test-thanos-ruler", OriginalReplicas: &originalReplicas},
			},
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithRESTMapper(newCustomRESTMapper()).
		WithObjects(tr, schedule).
		WithStatusSubresource(schedule).
		Build()
	if err := fakeClient.Status().Update(ctx, schedule); err != nil {
		t.Fatalf("Failed to set initial status: %v", err)
	}

	reconciler := &SleepScheduleReconciler{Client: fakeClient, Scheme: scheme}
	_, err := reconciler.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Name: schedule.Name, Namespace: schedule.Namespace},
	})
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	updated := &unstructured.Unstructured{}
	updated.SetGroupVersionKind(schema.GroupVersionKind{Group: "monitoring.coreos.com", Version: "v1", Kind: "ThanosRuler"})
	if err := fakeClient.Get(ctx, types.NamespacedName{Name: "test-thanos-ruler", Namespace: namespace}, updated); err != nil {
		t.Fatalf("Failed to get ThanosRuler: %v", err)
	}
	spec := updated.Object["spec"].(map[string]interface{})
	if replicas, ok := spec["replicas"].(int64); !ok || replicas != 2 {
		t.Errorf("Expected spec.replicas = 2, got %v", spec["replicas"])
	}
}

func TestReconcile_WakesAlertmanager(t *testing.T) {
	ctx := context.Background()
	scheme := newTestScheme()
	namespace := "test-namespace"

	am := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "monitoring.coreos.com/v1",
			"kind":       "Alertmanager",
			"metadata": map[string]interface{}{
				"name":      "test-alertmanager",
				"namespace": namespace,
				"labels":    map[string]interface{}{"app": "test"},
			},
			"spec": map[string]interface{}{
				"replicas": int64(0),
			},
		},
	}

	originalReplicas := int32(3)
	schedule := &slumlordv1alpha1.SlumlordSleepSchedule{
		ObjectMeta: metav1.ObjectMeta{Name: "test-schedule", Namespace: namespace},
		Spec: slumlordv1alpha1.SlumlordSleepScheduleSpec{
			Selector: slumlordv1alpha1.WorkloadSelector{
				MatchLabels: map[string]string{"app": "test"},
				Types:       []string{"Alertmanager"},
			},
			Schedule: neverSleepingSchedule(),
		},
		Status: slumlordv1alpha1.SlumlordSleepScheduleStatus{
			Sleeping: true,
			ManagedWorkloads: []slumlordv1alpha1.ManagedWorkload{
				{Kind: "Alertmanager", Name: "test-alertmanager", OriginalReplicas: &originalReplicas},
			},
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithRESTMapper(newCustomRESTMapper()).
		WithObjects(am, schedule).
		WithStatusSubresource(schedule).
		Build()
	if err := fakeClient.Status().Update(ctx, schedule); err != nil {
		t.Fatalf("Failed to set initial status: %v", err)
	}

	reconciler := &SleepScheduleReconciler{Client: fakeClient, Scheme: scheme}
	_, err := reconciler.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Name: schedule.Name, Namespace: schedule.Namespace},
	})
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	updated := &unstructured.Unstructured{}
	updated.SetGroupVersionKind(schema.GroupVersionKind{Group: "monitoring.coreos.com", Version: "v1", Kind: "Alertmanager"})
	if err := fakeClient.Get(ctx, types.NamespacedName{Name: "test-alertmanager", Namespace: namespace}, updated); err != nil {
		t.Fatalf("Failed to get Alertmanager: %v", err)
	}
	spec := updated.Object["spec"].(map[string]interface{})
	if replicas, ok := spec["replicas"].(int64); !ok || replicas != 3 {
		t.Errorf("Expected spec.replicas = 3, got %v", spec["replicas"])
	}
}

func TestReconcile_WakesPrometheus(t *testing.T) {
	ctx := context.Background()
	scheme := newTestScheme()
	namespace := "test-namespace"

	prom := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "monitoring.coreos.com/v1",
			"kind":       "Prometheus",
			"metadata": map[string]interface{}{
				"name":      "test-prometheus",
				"namespace": namespace,
				"labels":    map[string]interface{}{"app": "test"},
			},
			"spec": map[string]interface{}{
				"replicas": int64(0),
			},
		},
	}

	originalReplicas := int32(2)
	schedule := &slumlordv1alpha1.SlumlordSleepSchedule{
		ObjectMeta: metav1.ObjectMeta{Name: "test-schedule", Namespace: namespace},
		Spec: slumlordv1alpha1.SlumlordSleepScheduleSpec{
			Selector: slumlordv1alpha1.WorkloadSelector{
				MatchLabels: map[string]string{"app": "test"},
				Types:       []string{"Prometheus"},
			},
			Schedule: neverSleepingSchedule(),
		},
		Status: slumlordv1alpha1.SlumlordSleepScheduleStatus{
			Sleeping: true,
			ManagedWorkloads: []slumlordv1alpha1.ManagedWorkload{
				{Kind: "Prometheus", Name: "test-prometheus", OriginalReplicas: &originalReplicas},
			},
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithRESTMapper(newCustomRESTMapper()).
		WithObjects(prom, schedule).
		WithStatusSubresource(schedule).
		Build()
	if err := fakeClient.Status().Update(ctx, schedule); err != nil {
		t.Fatalf("Failed to set initial status: %v", err)
	}

	reconciler := &SleepScheduleReconciler{Client: fakeClient, Scheme: scheme}
	_, err := reconciler.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Name: schedule.Name, Namespace: schedule.Namespace},
	})
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	updated := &unstructured.Unstructured{}
	updated.SetGroupVersionKind(schema.GroupVersionKind{Group: "monitoring.coreos.com", Version: "v1", Kind: "Prometheus"})
	if err := fakeClient.Get(ctx, types.NamespacedName{Name: "test-prometheus", Namespace: namespace}, updated); err != nil {
		t.Fatalf("Failed to get Prometheus: %v", err)
	}
	spec := updated.Object["spec"].(map[string]interface{})
	if replicas, ok := spec["replicas"].(int64); !ok || replicas != 2 {
		t.Errorf("Expected spec.replicas = 2, got %v", spec["replicas"])
	}
}

func TestReconcile_SuspendsMariaDB(t *testing.T) {
	ctx := context.Background()
	scheme := newTestScheme()
	namespace := "test-namespace"

	mdb := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "k8s.mariadb.com/v1alpha1",
			"kind":       "MariaDB",
			"metadata": map[string]interface{}{
				"name":      "test-mariadb",
				"namespace": namespace,
				"labels":    map[string]interface{}{"app": "test"},
			},
			"spec": map[string]interface{}{
				"replicas": int64(3),
				"suspend":  false,
			},
		},
	}

	schedule := &slumlordv1alpha1.SlumlordSleepSchedule{
		ObjectMeta: metav1.ObjectMeta{Name: "test-schedule", Namespace: namespace},
		Spec: slumlordv1alpha1.SlumlordSleepScheduleSpec{
			Selector: slumlordv1alpha1.WorkloadSelector{
				MatchLabels: map[string]string{"app": "test"},
				Types:       []string{"MariaDB"},
			},
			Schedule: slumlordv1alpha1.SleepWindow{Start: "00:00", End: "23:59", Timezone: "UTC"},
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithRESTMapper(newCustomRESTMapper()).
		WithObjects(mdb, schedule).
		WithStatusSubresource(schedule).
		Build()

	reconciler := &SleepScheduleReconciler{Client: fakeClient, Scheme: scheme}
	_, err := reconciler.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Name: schedule.Name, Namespace: schedule.Namespace},
	})
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	updated := &unstructured.Unstructured{}
	updated.SetGroupVersionKind(schema.GroupVersionKind{Group: "k8s.mariadb.com", Version: "v1alpha1", Kind: "MariaDB"})
	if err := fakeClient.Get(ctx, types.NamespacedName{Name: "test-mariadb", Namespace: namespace}, updated); err != nil {
		t.Fatalf("Failed to get MariaDB: %v", err)
	}
	spec := updated.Object["spec"].(map[string]interface{})
	if suspend, ok := spec["suspend"].(bool); !ok || !suspend {
		t.Errorf("Expected spec.suspend = true, got %v", spec["suspend"])
	}

	var updatedSchedule slumlordv1alpha1.SlumlordSleepSchedule
	if err := fakeClient.Get(ctx, types.NamespacedName{Name: schedule.Name, Namespace: schedule.Namespace}, &updatedSchedule); err != nil {
		t.Fatalf("Failed to get schedule: %v", err)
	}
	if !updatedSchedule.Status.Sleeping {
		t.Error("Expected status.sleeping = true")
	}
	if len(updatedSchedule.Status.ManagedWorkloads) != 1 {
		t.Errorf("Expected 1 managed workload, got %d", len(updatedSchedule.Status.ManagedWorkloads))
	}
	if updatedSchedule.Status.ManagedWorkloads[0].Kind != "MariaDB" {
		t.Errorf("Expected kind = MariaDB, got %s", updatedSchedule.Status.ManagedWorkloads[0].Kind)
	}
}

func TestReconcile_WakesMariaDB(t *testing.T) {
	ctx := context.Background()
	scheme := newTestScheme()
	namespace := "test-namespace"

	mdb := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "k8s.mariadb.com/v1alpha1",
			"kind":       "MariaDB",
			"metadata": map[string]interface{}{
				"name":      "test-mariadb",
				"namespace": namespace,
				"labels":    map[string]interface{}{"app": "test"},
			},
			"spec": map[string]interface{}{
				"replicas": int64(3),
				"suspend":  true,
			},
		},
	}

	originalSuspend := false
	schedule := &slumlordv1alpha1.SlumlordSleepSchedule{
		ObjectMeta: metav1.ObjectMeta{Name: "test-schedule", Namespace: namespace},
		Spec: slumlordv1alpha1.SlumlordSleepScheduleSpec{
			Selector: slumlordv1alpha1.WorkloadSelector{
				MatchLabels: map[string]string{"app": "test"},
				Types:       []string{"MariaDB"},
			},
			Schedule: neverSleepingSchedule(),
		},
		Status: slumlordv1alpha1.SlumlordSleepScheduleStatus{
			Sleeping: true,
			ManagedWorkloads: []slumlordv1alpha1.ManagedWorkload{
				{Kind: "MariaDB", Name: "test-mariadb", OriginalSuspend: &originalSuspend},
			},
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithRESTMapper(newCustomRESTMapper()).
		WithObjects(mdb, schedule).
		WithStatusSubresource(schedule).
		Build()
	if err := fakeClient.Status().Update(ctx, schedule); err != nil {
		t.Fatalf("Failed to set initial status: %v", err)
	}

	reconciler := &SleepScheduleReconciler{Client: fakeClient, Scheme: scheme}
	_, err := reconciler.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Name: schedule.Name, Namespace: schedule.Namespace},
	})
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	updated := &unstructured.Unstructured{}
	updated.SetGroupVersionKind(schema.GroupVersionKind{Group: "k8s.mariadb.com", Version: "v1alpha1", Kind: "MariaDB"})
	if err := fakeClient.Get(ctx, types.NamespacedName{Name: "test-mariadb", Namespace: namespace}, updated); err != nil {
		t.Fatalf("Failed to get MariaDB: %v", err)
	}
	spec := updated.Object["spec"].(map[string]interface{})
	if suspend, ok := spec["suspend"].(bool); ok && suspend {
		t.Error("Expected MariaDB to be unsuspended")
	}
}

func TestReconcile_SuspendsMaxScale(t *testing.T) {
	ctx := context.Background()
	scheme := newTestScheme()
	namespace := "test-namespace"

	ms := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "k8s.mariadb.com/v1alpha1",
			"kind":       "MaxScale",
			"metadata": map[string]interface{}{
				"name":      "test-maxscale",
				"namespace": namespace,
				"labels":    map[string]interface{}{"app": "test"},
			},
			"spec": map[string]interface{}{
				"replicas": int64(2),
				"suspend":  false,
			},
		},
	}

	schedule := &slumlordv1alpha1.SlumlordSleepSchedule{
		ObjectMeta: metav1.ObjectMeta{Name: "test-schedule", Namespace: namespace},
		Spec: slumlordv1alpha1.SlumlordSleepScheduleSpec{
			Selector: slumlordv1alpha1.WorkloadSelector{
				MatchLabels: map[string]string{"app": "test"},
				Types:       []string{"MaxScale"},
			},
			Schedule: slumlordv1alpha1.SleepWindow{Start: "00:00", End: "23:59", Timezone: "UTC"},
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithRESTMapper(newCustomRESTMapper()).
		WithObjects(ms, schedule).
		WithStatusSubresource(schedule).
		Build()

	reconciler := &SleepScheduleReconciler{Client: fakeClient, Scheme: scheme}
	_, err := reconciler.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Name: schedule.Name, Namespace: schedule.Namespace},
	})
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	updated := &unstructured.Unstructured{}
	updated.SetGroupVersionKind(schema.GroupVersionKind{Group: "k8s.mariadb.com", Version: "v1alpha1", Kind: "MaxScale"})
	if err := fakeClient.Get(ctx, types.NamespacedName{Name: "test-maxscale", Namespace: namespace}, updated); err != nil {
		t.Fatalf("Failed to get MaxScale: %v", err)
	}
	spec := updated.Object["spec"].(map[string]interface{})
	if suspend, ok := spec["suspend"].(bool); !ok || !suspend {
		t.Errorf("Expected spec.suspend = true, got %v", spec["suspend"])
	}

	var updatedSchedule slumlordv1alpha1.SlumlordSleepSchedule
	if err := fakeClient.Get(ctx, types.NamespacedName{Name: schedule.Name, Namespace: schedule.Namespace}, &updatedSchedule); err != nil {
		t.Fatalf("Failed to get schedule: %v", err)
	}
	if updatedSchedule.Status.ManagedWorkloads[0].Kind != "MaxScale" {
		t.Errorf("Expected kind = MaxScale, got %s", updatedSchedule.Status.ManagedWorkloads[0].Kind)
	}
}

func TestReconcile_WakesMaxScale(t *testing.T) {
	ctx := context.Background()
	scheme := newTestScheme()
	namespace := "test-namespace"

	ms := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "k8s.mariadb.com/v1alpha1",
			"kind":       "MaxScale",
			"metadata": map[string]interface{}{
				"name":      "test-maxscale",
				"namespace": namespace,
				"labels":    map[string]interface{}{"app": "test"},
			},
			"spec": map[string]interface{}{
				"replicas": int64(2),
				"suspend":  true,
			},
		},
	}

	originalSuspend := false
	schedule := &slumlordv1alpha1.SlumlordSleepSchedule{
		ObjectMeta: metav1.ObjectMeta{Name: "test-schedule", Namespace: namespace},
		Spec: slumlordv1alpha1.SlumlordSleepScheduleSpec{
			Selector: slumlordv1alpha1.WorkloadSelector{
				MatchLabels: map[string]string{"app": "test"},
				Types:       []string{"MaxScale"},
			},
			Schedule: neverSleepingSchedule(),
		},
		Status: slumlordv1alpha1.SlumlordSleepScheduleStatus{
			Sleeping: true,
			ManagedWorkloads: []slumlordv1alpha1.ManagedWorkload{
				{Kind: "MaxScale", Name: "test-maxscale", OriginalSuspend: &originalSuspend},
			},
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithRESTMapper(newCustomRESTMapper()).
		WithObjects(ms, schedule).
		WithStatusSubresource(schedule).
		Build()
	if err := fakeClient.Status().Update(ctx, schedule); err != nil {
		t.Fatalf("Failed to set initial status: %v", err)
	}

	reconciler := &SleepScheduleReconciler{Client: fakeClient, Scheme: scheme}
	_, err := reconciler.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Name: schedule.Name, Namespace: schedule.Namespace},
	})
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	updated := &unstructured.Unstructured{}
	updated.SetGroupVersionKind(schema.GroupVersionKind{Group: "k8s.mariadb.com", Version: "v1alpha1", Kind: "MaxScale"})
	if err := fakeClient.Get(ctx, types.NamespacedName{Name: "test-maxscale", Namespace: namespace}, updated); err != nil {
		t.Fatalf("Failed to get MaxScale: %v", err)
	}
	spec := updated.Object["spec"].(map[string]interface{})
	if suspend, ok := spec["suspend"].(bool); ok && suspend {
		t.Error("Expected MaxScale to be unsuspended")
	}
}

func TestReconcile_DeletedSchedule(t *testing.T) {
	ctx := context.Background()
	scheme := newTestScheme()

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		Build()

	reconciler := &SleepScheduleReconciler{
		Client: fakeClient,
		Scheme: scheme,
	}

	// Reconcile with a NamespacedName that doesn't exist
	_, err := reconciler.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{
			Name:      "nonexistent-schedule",
			Namespace: "nonexistent-namespace",
		},
	})
	if err != nil {
		t.Fatalf("Expected no error for deleted/nonexistent schedule, got: %v", err)
	}
}
