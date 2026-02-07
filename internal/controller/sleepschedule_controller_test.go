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
	})
	mapper.Add(schema.GroupVersionKind{Group: "postgresql.cnpg.io", Version: "v1", Kind: "Cluster"}, meta.RESTScopeNamespace)
	mapper.Add(schema.GroupVersionKind{Group: "helm.toolkit.fluxcd.io", Version: "v2", Kind: "HelmRelease"}, meta.RESTScopeNamespace)
	mapper.Add(schema.GroupVersionKind{Group: "kustomize.toolkit.fluxcd.io", Version: "v1", Kind: "Kustomization"}, meta.RESTScopeNamespace)
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
