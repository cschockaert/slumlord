package controller

import (
	"context"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ktesting "k8s.io/client-go/testing"
	metricsv1beta1 "k8s.io/metrics/pkg/apis/metrics/v1beta1"
	metricsfake "k8s.io/metrics/pkg/client/clientset/versioned/fake"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	slumlordv1alpha1 "github.com/cschockaert/slumlord/api/v1alpha1"
)

func TestIdleDetector_ShouldManageType(t *testing.T) {
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
		{
			name:     "CronJob match",
			types:    []string{"CronJob"},
			kind:     "CronJob",
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &IdleDetectorReconciler{}
			detector := &slumlordv1alpha1.SlumlordIdleDetector{
				Spec: slumlordv1alpha1.SlumlordIdleDetectorSpec{
					Selector: slumlordv1alpha1.WorkloadSelector{
						Types: tt.types,
					},
				},
			}

			got := r.shouldManageType(detector, tt.kind)
			if got != tt.expected {
				t.Errorf("shouldManageType() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestIdleDetector_Reconcile_AddsFinalizer(t *testing.T) {
	ctx := context.Background()
	scheme := newTestScheme()
	namespace := "test-namespace"

	detector := &slumlordv1alpha1.SlumlordIdleDetector{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-detector",
			Namespace: namespace,
		},
		Spec: slumlordv1alpha1.SlumlordIdleDetectorSpec{
			Selector: slumlordv1alpha1.WorkloadSelector{
				MatchLabels: map[string]string{"app": "test"},
				Types:       []string{"Deployment"},
			},
			Thresholds:   slumlordv1alpha1.IdleThresholds{},
			IdleDuration: "30m",
			Action:       "alert",
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(detector).
		WithStatusSubresource(detector).
		Build()

	reconciler := &IdleDetectorReconciler{
		Client: fakeClient,
		Scheme: scheme,
	}

	_, err := reconciler.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{
			Name:      detector.Name,
			Namespace: detector.Namespace,
		},
	})
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	// Verify finalizer was added
	var updated slumlordv1alpha1.SlumlordIdleDetector
	if err := fakeClient.Get(ctx, types.NamespacedName{Name: detector.Name, Namespace: detector.Namespace}, &updated); err != nil {
		t.Fatalf("Failed to get detector: %v", err)
	}
	found := false
	for _, f := range updated.Finalizers {
		if f == idleDetectorFinalizer {
			found = true
			break
		}
	}
	if !found {
		t.Error("Expected finalizer to be added")
	}
}

func TestIdleDetector_Reconcile_UpdatesLastCheckTime(t *testing.T) {
	ctx := context.Background()
	scheme := newTestScheme()
	namespace := "test-namespace"

	detector := &slumlordv1alpha1.SlumlordIdleDetector{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "test-detector",
			Namespace:  namespace,
			Finalizers: []string{idleDetectorFinalizer},
		},
		Spec: slumlordv1alpha1.SlumlordIdleDetectorSpec{
			Selector: slumlordv1alpha1.WorkloadSelector{
				MatchLabels: map[string]string{"app": "test"},
				Types:       []string{"Deployment"},
			},
			Thresholds:   slumlordv1alpha1.IdleThresholds{},
			IdleDuration: "30m",
			Action:       "alert",
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(detector).
		WithStatusSubresource(detector).
		Build()

	reconciler := &IdleDetectorReconciler{
		Client: fakeClient,
		Scheme: scheme,
	}

	_, err := reconciler.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{
			Name:      detector.Name,
			Namespace: detector.Namespace,
		},
	})
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	// Verify lastCheckTime was set
	var updated slumlordv1alpha1.SlumlordIdleDetector
	if err := fakeClient.Get(ctx, types.NamespacedName{Name: detector.Name, Namespace: detector.Namespace}, &updated); err != nil {
		t.Fatalf("Failed to get detector: %v", err)
	}
	if updated.Status.LastCheckTime == nil {
		t.Error("Expected lastCheckTime to be set")
	}
}

func TestIdleDetector_Reconcile_DeletedDetector(t *testing.T) {
	ctx := context.Background()
	scheme := newTestScheme()

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		Build()

	reconciler := &IdleDetectorReconciler{
		Client: fakeClient,
		Scheme: scheme,
	}

	// Reconcile with a NamespacedName that doesn't exist
	_, err := reconciler.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{
			Name:      "nonexistent-detector",
			Namespace: "nonexistent-namespace",
		},
	})
	if err != nil {
		t.Fatalf("Expected no error for deleted/nonexistent detector, got: %v", err)
	}
}

func TestIdleDetector_Reconcile_InvalidIdleDuration(t *testing.T) {
	ctx := context.Background()
	scheme := newTestScheme()
	namespace := "test-namespace"

	detector := &slumlordv1alpha1.SlumlordIdleDetector{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "test-detector",
			Namespace:  namespace,
			Finalizers: []string{idleDetectorFinalizer},
		},
		Spec: slumlordv1alpha1.SlumlordIdleDetectorSpec{
			Selector: slumlordv1alpha1.WorkloadSelector{
				MatchLabels: map[string]string{"app": "test"},
			},
			Thresholds:   slumlordv1alpha1.IdleThresholds{},
			IdleDuration: "invalid",
			Action:       "alert",
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(detector).
		WithStatusSubresource(detector).
		Build()

	reconciler := &IdleDetectorReconciler{
		Client: fakeClient,
		Scheme: scheme,
	}

	_, err := reconciler.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{
			Name:      detector.Name,
			Namespace: detector.Namespace,
		},
	})
	if err == nil {
		t.Fatal("Expected error for invalid idle duration, got nil")
	}
}

func TestIdleDetector_ScaleDownDeployment(t *testing.T) {
	ctx := context.Background()
	scheme := newTestScheme()
	namespace := "test-namespace"

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

	detector := &slumlordv1alpha1.SlumlordIdleDetector{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-detector",
			Namespace: namespace,
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(deploy, detector).
		Build()

	reconciler := &IdleDetectorReconciler{
		Client: fakeClient,
		Scheme: scheme,
	}

	err := reconciler.scaleDownDeployment(ctx, detector, "test-deploy")
	if err != nil {
		t.Fatalf("scaleDownDeployment() error = %v", err)
	}

	// Verify deployment scaled to 0
	var updated appsv1.Deployment
	if err := fakeClient.Get(ctx, types.NamespacedName{Name: deploy.Name, Namespace: deploy.Namespace}, &updated); err != nil {
		t.Fatalf("Failed to get deployment: %v", err)
	}
	if *updated.Spec.Replicas != 0 {
		t.Errorf("Expected replicas = 0, got %d", *updated.Spec.Replicas)
	}

	// Verify original replicas stored in status
	if len(detector.Status.ScaledWorkloads) != 1 {
		t.Fatalf("Expected 1 scaled workload, got %d", len(detector.Status.ScaledWorkloads))
	}
	if *detector.Status.ScaledWorkloads[0].OriginalReplicas != 3 {
		t.Errorf("Expected originalReplicas = 3, got %d", *detector.Status.ScaledWorkloads[0].OriginalReplicas)
	}
}

func TestIdleDetector_ScaleDownStatefulSet(t *testing.T) {
	ctx := context.Background()
	scheme := newTestScheme()
	namespace := "test-namespace"

	replicas := int32(5)
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
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "test"}},
				Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "test", Image: "nginx"}}},
			},
		},
	}

	detector := &slumlordv1alpha1.SlumlordIdleDetector{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-detector",
			Namespace: namespace,
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(sts, detector).
		Build()

	reconciler := &IdleDetectorReconciler{
		Client: fakeClient,
		Scheme: scheme,
	}

	err := reconciler.scaleDownStatefulSet(ctx, detector, "test-sts")
	if err != nil {
		t.Fatalf("scaleDownStatefulSet() error = %v", err)
	}

	var updated appsv1.StatefulSet
	if err := fakeClient.Get(ctx, types.NamespacedName{Name: sts.Name, Namespace: sts.Namespace}, &updated); err != nil {
		t.Fatalf("Failed to get statefulset: %v", err)
	}
	if *updated.Spec.Replicas != 0 {
		t.Errorf("Expected replicas = 0, got %d", *updated.Spec.Replicas)
	}

	if len(detector.Status.ScaledWorkloads) != 1 {
		t.Fatalf("Expected 1 scaled workload, got %d", len(detector.Status.ScaledWorkloads))
	}
	if *detector.Status.ScaledWorkloads[0].OriginalReplicas != 5 {
		t.Errorf("Expected originalReplicas = 5, got %d", *detector.Status.ScaledWorkloads[0].OriginalReplicas)
	}
}

func TestIdleDetector_SuspendCronJob(t *testing.T) {
	ctx := context.Background()
	scheme := newTestScheme()
	namespace := "test-namespace"

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
							Containers:    []corev1.Container{{Name: "test", Image: "busybox"}},
						},
					},
				},
			},
		},
	}

	detector := &slumlordv1alpha1.SlumlordIdleDetector{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-detector",
			Namespace: namespace,
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(cj, detector).
		Build()

	reconciler := &IdleDetectorReconciler{
		Client: fakeClient,
		Scheme: scheme,
	}

	err := reconciler.suspendCronJob(ctx, detector, "test-cronjob")
	if err != nil {
		t.Fatalf("suspendCronJob() error = %v", err)
	}

	var updated batchv1.CronJob
	if err := fakeClient.Get(ctx, types.NamespacedName{Name: cj.Name, Namespace: cj.Namespace}, &updated); err != nil {
		t.Fatalf("Failed to get cronjob: %v", err)
	}
	if !*updated.Spec.Suspend {
		t.Error("Expected cronjob to be suspended")
	}

	if len(detector.Status.ScaledWorkloads) != 1 {
		t.Fatalf("Expected 1 scaled workload, got %d", len(detector.Status.ScaledWorkloads))
	}
	if detector.Status.ScaledWorkloads[0].Kind != "CronJob" {
		t.Errorf("Expected kind = CronJob, got %s", detector.Status.ScaledWorkloads[0].Kind)
	}
}

func TestIdleDetector_RestoreScaledWorkloads(t *testing.T) {
	ctx := context.Background()
	scheme := newTestScheme()
	namespace := "test-namespace"

	// Create scaled-down resources
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
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "test"}},
				Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "test", Image: "nginx"}}},
			},
		},
	}

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

	originalReplicas := int32(3)
	originalSuspend := false
	detector := &slumlordv1alpha1.SlumlordIdleDetector{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-detector",
			Namespace: namespace,
		},
		Status: slumlordv1alpha1.SlumlordIdleDetectorStatus{
			ScaledWorkloads: []slumlordv1alpha1.ScaledWorkload{
				{
					Kind:             "Deployment",
					Name:             "test-deploy",
					OriginalReplicas: &originalReplicas,
					ScaledAt:         metav1.Now(),
				},
				{
					Kind:            "CronJob",
					Name:            "test-cronjob",
					OriginalSuspend: &originalSuspend,
					ScaledAt:        metav1.Now(),
				},
			},
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(deploy, cj, detector).
		Build()

	reconciler := &IdleDetectorReconciler{
		Client: fakeClient,
		Scheme: scheme,
	}

	err := reconciler.restoreScaledWorkloads(ctx, detector)
	if err != nil {
		t.Fatalf("restoreScaledWorkloads() error = %v", err)
	}

	// Verify deployment restored
	var updatedDeploy appsv1.Deployment
	if err := fakeClient.Get(ctx, types.NamespacedName{Name: deploy.Name, Namespace: deploy.Namespace}, &updatedDeploy); err != nil {
		t.Fatalf("Failed to get deployment: %v", err)
	}
	if *updatedDeploy.Spec.Replicas != 3 {
		t.Errorf("Expected replicas = 3, got %d", *updatedDeploy.Spec.Replicas)
	}

	// Verify cronjob restored
	var updatedCJ batchv1.CronJob
	if err := fakeClient.Get(ctx, types.NamespacedName{Name: cj.Name, Namespace: cj.Namespace}, &updatedCJ); err != nil {
		t.Fatalf("Failed to get cronjob: %v", err)
	}
	if *updatedCJ.Spec.Suspend != false {
		t.Error("Expected cronjob to be unsuspended")
	}

	// Verify scaledWorkloads cleared
	if detector.Status.ScaledWorkloads != nil {
		t.Errorf("Expected scaledWorkloads to be nil, got %d entries", len(detector.Status.ScaledWorkloads))
	}
}

func TestIdleDetector_ScaleDownDeployment_AlreadyZero(t *testing.T) {
	ctx := context.Background()
	scheme := newTestScheme()
	namespace := "test-namespace"

	zero := int32(0)
	deploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-deploy",
			Namespace: namespace,
		},
		Spec: appsv1.DeploymentSpec{
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

	detector := &slumlordv1alpha1.SlumlordIdleDetector{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-detector",
			Namespace: namespace,
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(deploy, detector).
		Build()

	reconciler := &IdleDetectorReconciler{
		Client: fakeClient,
		Scheme: scheme,
	}

	err := reconciler.scaleDownDeployment(ctx, detector, "test-deploy")
	if err != nil {
		t.Fatalf("scaleDownDeployment() error = %v", err)
	}

	// Should not add to scaled workloads since it's already at 0
	if len(detector.Status.ScaledWorkloads) != 0 {
		t.Errorf("Expected 0 scaled workloads for already-zero deployment, got %d", len(detector.Status.ScaledWorkloads))
	}
}

func TestIdleDetector_Reconcile_NilMetricsClientReturnsNoIdle(t *testing.T) {
	ctx := context.Background()
	scheme := newTestScheme()
	namespace := "test-namespace"

	// Create a deployment with replicas
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
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "test"}},
				Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "test", Image: "nginx"}}},
			},
		},
	}

	detector := &slumlordv1alpha1.SlumlordIdleDetector{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "test-detector",
			Namespace:  namespace,
			Finalizers: []string{idleDetectorFinalizer},
		},
		Spec: slumlordv1alpha1.SlumlordIdleDetectorSpec{
			Selector: slumlordv1alpha1.WorkloadSelector{
				MatchLabels: map[string]string{"app": "test"},
				Types:       []string{"Deployment"},
			},
			Thresholds:   slumlordv1alpha1.IdleThresholds{},
			IdleDuration: "30m",
			Action:       "scale",
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(deploy, detector).
		WithStatusSubresource(detector).
		Build()

	reconciler := &IdleDetectorReconciler{
		Client:        fakeClient,
		Scheme:        scheme,
		MetricsClient: nil, // nil = degraded mode
	}

	result, err := reconciler.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{
			Name:      detector.Name,
			Namespace: detector.Namespace,
		},
	})
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	// Should requeue after 5 minutes
	if result.RequeueAfter.Minutes() != 5 {
		t.Errorf("Expected requeue after 5m, got %v", result.RequeueAfter)
	}

	// Deployment should NOT be scaled down (nil MetricsClient returns not idle)
	var updatedDeploy appsv1.Deployment
	if err := fakeClient.Get(ctx, types.NamespacedName{Name: deploy.Name, Namespace: deploy.Namespace}, &updatedDeploy); err != nil {
		t.Fatalf("Failed to get deployment: %v", err)
	}
	if *updatedDeploy.Spec.Replicas != 3 {
		t.Errorf("Expected replicas = 3 (unchanged, nil MetricsClient returns not idle), got %d", *updatedDeploy.Spec.Replicas)
	}

	// Status should show no idle workloads
	var updatedDetector slumlordv1alpha1.SlumlordIdleDetector
	if err := fakeClient.Get(ctx, types.NamespacedName{Name: detector.Name, Namespace: detector.Namespace}, &updatedDetector); err != nil {
		t.Fatalf("Failed to get detector: %v", err)
	}
	if len(updatedDetector.Status.IdleWorkloads) != 0 {
		t.Errorf("Expected 0 idle workloads (nil MetricsClient returns not idle), got %d", len(updatedDetector.Status.IdleWorkloads))
	}
}

func TestIdleDetector_MatchesSelector(t *testing.T) {
	tests := []struct {
		name       string
		workload   string
		matchNames []string
		expected   bool
	}{
		{
			name:       "no matchNames matches all",
			workload:   "anything",
			matchNames: nil,
			expected:   true,
		},
		{
			name:       "exact match",
			workload:   "my-deploy",
			matchNames: []string{"my-deploy"},
			expected:   true,
		},
		{
			name:       "wildcard prefix",
			workload:   "prod-api",
			matchNames: []string{"prod-*"},
			expected:   true,
		},
		{
			name:       "wildcard suffix",
			workload:   "api-service",
			matchNames: []string{"*-service"},
			expected:   true,
		},
		{
			name:       "no match",
			workload:   "staging-api",
			matchNames: []string{"prod-*"},
			expected:   false,
		},
		{
			name:       "multiple patterns one matches",
			workload:   "staging-api",
			matchNames: []string{"prod-*", "staging-*"},
			expected:   true,
		},
		{
			name:       "multiple patterns none match",
			workload:   "dev-api",
			matchNames: []string{"prod-*", "staging-*"},
			expected:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			selector := slumlordv1alpha1.WorkloadSelector{
				MatchNames: tt.matchNames,
			}
			got := matchesSelector(tt.workload, selector)
			if got != tt.expected {
				t.Errorf("matchesSelector(%q, %v) = %v, want %v", tt.workload, tt.matchNames, got, tt.expected)
			}
		})
	}
}

func TestIdleDetector_Reconcile_MatchNamesFiltering(t *testing.T) {
	ctx := context.Background()
	scheme := newTestScheme()
	namespace := "test-namespace"

	replicas := int32(3)
	matchedDeploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "prod-api",
			Namespace: namespace,
			Labels:    map[string]string{"app": "test"},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "test"}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "test"}},
				Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "test", Image: "nginx"}}},
			},
		},
	}

	unmatchedDeploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "staging-api",
			Namespace: namespace,
			Labels:    map[string]string{"app": "test"},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "test"}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "test"}},
				Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "test", Image: "nginx"}}},
			},
		},
	}

	detector := &slumlordv1alpha1.SlumlordIdleDetector{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "test-detector",
			Namespace:  namespace,
			Finalizers: []string{idleDetectorFinalizer},
		},
		Spec: slumlordv1alpha1.SlumlordIdleDetectorSpec{
			Selector: slumlordv1alpha1.WorkloadSelector{
				MatchLabels: map[string]string{"app": "test"},
				MatchNames:  []string{"prod-*"},
				Types:       []string{"Deployment"},
			},
			Thresholds:   slumlordv1alpha1.IdleThresholds{},
			IdleDuration: "30m",
			Action:       "alert",
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(matchedDeploy, unmatchedDeploy, detector).
		WithStatusSubresource(detector).
		Build()

	reconciler := &IdleDetectorReconciler{
		Client: fakeClient,
		Scheme: scheme,
	}

	_, err := reconciler.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Name: detector.Name, Namespace: detector.Namespace},
	})
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	// Both deployments should still be running (metrics stub returns not-idle),
	// but the test verifies that name filtering doesn't cause errors.
	// The MatchNames filter is applied before metrics check.
	var updatedProd appsv1.Deployment
	if err := fakeClient.Get(ctx, types.NamespacedName{Name: "prod-api", Namespace: namespace}, &updatedProd); err != nil {
		t.Fatalf("Failed to get prod deployment: %v", err)
	}
	if *updatedProd.Spec.Replicas != 3 {
		t.Errorf("Expected prod replicas = 3, got %d", *updatedProd.Spec.Replicas)
	}

	var updatedStaging appsv1.Deployment
	if err := fakeClient.Get(ctx, types.NamespacedName{Name: "staging-api", Namespace: namespace}, &updatedStaging); err != nil {
		t.Fatalf("Failed to get staging deployment: %v", err)
	}
	if *updatedStaging.Spec.Replicas != 3 {
		t.Errorf("Expected staging replicas = 3, got %d", *updatedStaging.Spec.Replicas)
	}
}

func TestIdleDetector_Reconcile_DeletionRestoresWorkloads(t *testing.T) {
	ctx := context.Background()
	scheme := newTestScheme()
	namespace := "test-namespace"

	// Create a scaled-down deployment
	zero := int32(0)
	deploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-deploy",
			Namespace: namespace,
			Labels:    map[string]string{"app": "test"},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &zero,
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "test"}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "test"}},
				Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "test", Image: "nginx"}}},
			},
		},
	}

	now := metav1.Now()
	originalReplicas := int32(3)
	detector := &slumlordv1alpha1.SlumlordIdleDetector{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "test-detector",
			Namespace:         namespace,
			Finalizers:        []string{idleDetectorFinalizer},
			DeletionTimestamp: &now,
		},
		Spec: slumlordv1alpha1.SlumlordIdleDetectorSpec{
			Selector: slumlordv1alpha1.WorkloadSelector{
				MatchLabels: map[string]string{"app": "test"},
				Types:       []string{"Deployment"},
			},
			Thresholds:   slumlordv1alpha1.IdleThresholds{},
			IdleDuration: "30m",
			Action:       "scale",
		},
		Status: slumlordv1alpha1.SlumlordIdleDetectorStatus{
			ScaledWorkloads: []slumlordv1alpha1.ScaledWorkload{
				{
					Kind:             "Deployment",
					Name:             "test-deploy",
					OriginalReplicas: &originalReplicas,
					ScaledAt:         now,
				},
			},
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(deploy, detector).
		WithStatusSubresource(detector).
		Build()

	reconciler := &IdleDetectorReconciler{
		Client: fakeClient,
		Scheme: scheme,
	}

	_, err := reconciler.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Name: detector.Name, Namespace: detector.Namespace},
	})
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	// Verify deployment was restored
	var updatedDeploy appsv1.Deployment
	if err := fakeClient.Get(ctx, types.NamespacedName{Name: deploy.Name, Namespace: deploy.Namespace}, &updatedDeploy); err != nil {
		t.Fatalf("Failed to get deployment: %v", err)
	}
	if *updatedDeploy.Spec.Replicas != 3 {
		t.Errorf("Expected replicas = 3 after restore, got %d", *updatedDeploy.Spec.Replicas)
	}

	// The detector should be garbage-collected by the fake client after finalizer removal
	// (DeletionTimestamp set + no finalizers = deleted)
	var updatedDetector slumlordv1alpha1.SlumlordIdleDetector
	err = fakeClient.Get(ctx, types.NamespacedName{Name: detector.Name, Namespace: detector.Namespace}, &updatedDetector)
	if err == nil {
		// If still present, verify finalizer was removed
		for _, f := range updatedDetector.Finalizers {
			if f == idleDetectorFinalizer {
				t.Error("Expected finalizer to be removed after deletion")
			}
		}
	}
	// err != nil (not found) is also acceptable - means the object was fully deleted
}

func TestIdleDetector_RestoreScaledWorkloads_PartialFailure(t *testing.T) {
	ctx := context.Background()
	scheme := newTestScheme()
	namespace := "test-namespace"

	// Only create one deployment - the other is "missing" to simulate failure
	zero := int32(0)
	deploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "existing-deploy",
			Namespace: namespace,
			Labels:    map[string]string{"app": "test"},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &zero,
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "test"}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "test"}},
				Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "test", Image: "nginx"}}},
			},
		},
	}

	originalReplicas := int32(3)
	missingReplicas := int32(2)
	detector := &slumlordv1alpha1.SlumlordIdleDetector{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-detector",
			Namespace: namespace,
		},
		Status: slumlordv1alpha1.SlumlordIdleDetectorStatus{
			ScaledWorkloads: []slumlordv1alpha1.ScaledWorkload{
				{
					Kind:             "Deployment",
					Name:             "existing-deploy",
					OriginalReplicas: &originalReplicas,
					ScaledAt:         metav1.Now(),
				},
				{
					Kind:             "Deployment",
					Name:             "missing-deploy",
					OriginalReplicas: &missingReplicas,
					ScaledAt:         metav1.Now(),
				},
			},
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(deploy, detector).
		Build()

	reconciler := &IdleDetectorReconciler{
		Client: fakeClient,
		Scheme: scheme,
	}

	err := reconciler.restoreScaledWorkloads(ctx, detector)
	if err == nil {
		t.Fatal("Expected error for partial restore failure, got nil")
	}

	// Verify the existing deployment was restored
	var updatedDeploy appsv1.Deployment
	if err := fakeClient.Get(ctx, types.NamespacedName{Name: "existing-deploy", Namespace: namespace}, &updatedDeploy); err != nil {
		t.Fatalf("Failed to get deployment: %v", err)
	}
	if *updatedDeploy.Spec.Replicas != 3 {
		t.Errorf("Expected existing deploy replicas = 3, got %d", *updatedDeploy.Spec.Replicas)
	}

	// Verify only the failed workload remains in status
	if len(detector.Status.ScaledWorkloads) != 1 {
		t.Fatalf("Expected 1 remaining scaled workload, got %d", len(detector.Status.ScaledWorkloads))
	}
	if detector.Status.ScaledWorkloads[0].Name != "missing-deploy" {
		t.Errorf("Expected remaining workload to be missing-deploy, got %s", detector.Status.ScaledWorkloads[0].Name)
	}
}

func TestIdleDetector_Reconcile_NoMatchLabelsWithMatchNames(t *testing.T) {
	ctx := context.Background()
	scheme := newTestScheme()
	namespace := "test-namespace"

	replicas := int32(3)
	deploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "prod-api",
			Namespace: namespace,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "prod"}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "prod"}},
				Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "test", Image: "nginx"}}},
			},
		},
	}

	// Selector with only MatchNames, no MatchLabels
	detector := &slumlordv1alpha1.SlumlordIdleDetector{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "test-detector",
			Namespace:  namespace,
			Finalizers: []string{idleDetectorFinalizer},
		},
		Spec: slumlordv1alpha1.SlumlordIdleDetectorSpec{
			Selector: slumlordv1alpha1.WorkloadSelector{
				MatchNames: []string{"prod-*"},
				Types:      []string{"Deployment"},
			},
			Thresholds:   slumlordv1alpha1.IdleThresholds{},
			IdleDuration: "30m",
			Action:       "alert",
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(deploy, detector).
		WithStatusSubresource(detector).
		Build()

	reconciler := &IdleDetectorReconciler{
		Client: fakeClient,
		Scheme: scheme,
	}

	_, err := reconciler.Reconcile(ctx, ctrl.Request{
		NamespacedName: types.NamespacedName{Name: detector.Name, Namespace: detector.Namespace},
	})
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	// Deployment should still be running (nil MetricsClient returns not-idle),
	// but this verifies that having only MatchNames (no MatchLabels) works without error
	var updatedDeploy appsv1.Deployment
	if err := fakeClient.Get(ctx, types.NamespacedName{Name: "prod-api", Namespace: namespace}, &updatedDeploy); err != nil {
		t.Fatalf("Failed to get deployment: %v", err)
	}
	if *updatedDeploy.Spec.Replicas != 3 {
		t.Errorf("Expected replicas = 3, got %d", *updatedDeploy.Spec.Replicas)
	}
}

// --- Test helpers for metrics tests ---

func makePod(name, namespace string, cpuReq, memReq string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels:    map[string]string{"app": "test"},
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name:  "app",
					Image: "nginx",
					Resources: corev1.ResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse(cpuReq),
							corev1.ResourceMemory: resource.MustParse(memReq),
						},
					},
				},
			},
		},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}
}

func makePodNoRequests(name, namespace string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels:    map[string]string{"app": "test"},
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{Name: "app", Image: "nginx"},
			},
		},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}
}

func makePodMetrics(name, namespace, cpuUsage, memUsage string) *metricsv1beta1.PodMetrics {
	return &metricsv1beta1.PodMetrics{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Containers: []metricsv1beta1.ContainerMetrics{
			{
				Name: "app",
				Usage: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse(cpuUsage),
					corev1.ResourceMemory: resource.MustParse(memUsage),
				},
			},
		},
	}
}

func int32Ptr(i int32) *int32 { return &i }

// newFakeMetricsClient creates a fake metrics client that serves the given PodMetrics
// via a reactor (needed because the fake tracker stores objects under "podmetricses"
// while the typed client queries resource "pods").
func newFakeMetricsClient(podMetrics ...metricsv1beta1.PodMetrics) *metricsfake.Clientset {
	fc := metricsfake.NewSimpleClientset() //nolint:staticcheck // NewClientset not available in k8s.io/metrics v0.35.0
	fc.PrependReactor("list", "pods", func(action ktesting.Action) (bool, runtime.Object, error) {
		la := action.(ktesting.ListAction)
		ns := la.GetNamespace()
		var filtered []metricsv1beta1.PodMetrics
		for _, pm := range podMetrics {
			if ns == "" || pm.Namespace == ns {
				filtered = append(filtered, pm)
			}
		}
		return true, &metricsv1beta1.PodMetricsList{Items: filtered}, nil
	})
	return fc
}

// --- Unit tests for computeUsagePercent ---

func TestComputeUsagePercent(t *testing.T) {
	tests := []struct {
		name     string
		pods     []corev1.Pod
		metrics  []metricsv1beta1.PodMetrics
		wantCPU  float64
		wantMem  float64
		wantData bool
	}{
		{
			name: "single pod low usage",
			pods: []corev1.Pod{*makePod("pod-1", "ns", "1000m", "1Gi")},
			metrics: []metricsv1beta1.PodMetrics{
				*makePodMetrics("pod-1", "ns", "50m", "100Mi"),
			},
			wantCPU:  5,        // 50m / 1000m = 5%
			wantMem:  9.765625, // 100Mi / 1024Mi ≈ 9.77%
			wantData: true,
		},
		{
			name: "multi-pod aggregate",
			pods: []corev1.Pod{
				*makePod("pod-1", "ns", "500m", "512Mi"),
				*makePod("pod-2", "ns", "500m", "512Mi"),
			},
			metrics: []metricsv1beta1.PodMetrics{
				*makePodMetrics("pod-1", "ns", "100m", "128Mi"),
				*makePodMetrics("pod-2", "ns", "100m", "128Mi"),
			},
			wantCPU:  20, // 200m / 1000m = 20%
			wantMem:  25, // 256Mi / 1024Mi = 25%
			wantData: true,
		},
		{
			name:     "no requests returns no data",
			pods:     []corev1.Pod{*makePodNoRequests("pod-1", "ns")},
			metrics:  []metricsv1beta1.PodMetrics{*makePodMetrics("pod-1", "ns", "50m", "100Mi")},
			wantCPU:  0,
			wantMem:  0,
			wantData: false,
		},
		{
			name:     "no matching metrics returns no data",
			pods:     []corev1.Pod{*makePod("pod-1", "ns", "1000m", "1Gi")},
			metrics:  []metricsv1beta1.PodMetrics{*makePodMetrics("other-pod", "ns", "50m", "100Mi")},
			wantCPU:  0,
			wantMem:  0,
			wantData: false,
		},
		{
			name:     "empty metrics list returns no data",
			pods:     []corev1.Pod{*makePod("pod-1", "ns", "1000m", "1Gi")},
			metrics:  nil,
			wantCPU:  0,
			wantMem:  0,
			wantData: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cpuPct, memPct, hasData := computeUsagePercent(tt.pods, tt.metrics)
			if hasData != tt.wantData {
				t.Errorf("hasData = %v, want %v", hasData, tt.wantData)
			}
			if tt.wantData {
				// Allow small floating point tolerance
				if diff := cpuPct - tt.wantCPU; diff > 0.1 || diff < -0.1 {
					t.Errorf("cpuPct = %v, want %v", cpuPct, tt.wantCPU)
				}
				if diff := memPct - tt.wantMem; diff > 0.1 || diff < -0.1 {
					t.Errorf("memPct = %v, want %v", memPct, tt.wantMem)
				}
			}
		})
	}
}

// --- Unit tests for isIdleByThresholds ---

func TestIsIdleByThresholds(t *testing.T) {
	tests := []struct {
		name       string
		cpuPct     float64
		memPct     float64
		thresholds slumlordv1alpha1.IdleThresholds
		wantIdle   bool
	}{
		{
			name:   "both below thresholds",
			cpuPct: 5, memPct: 10,
			thresholds: slumlordv1alpha1.IdleThresholds{CPUPercent: int32Ptr(20), MemoryPercent: int32Ptr(30)},
			wantIdle:   true,
		},
		{
			name:   "CPU above threshold",
			cpuPct: 25, memPct: 10,
			thresholds: slumlordv1alpha1.IdleThresholds{CPUPercent: int32Ptr(20), MemoryPercent: int32Ptr(30)},
			wantIdle:   false,
		},
		{
			name:   "memory above threshold",
			cpuPct: 5, memPct: 35,
			thresholds: slumlordv1alpha1.IdleThresholds{CPUPercent: int32Ptr(20), MemoryPercent: int32Ptr(30)},
			wantIdle:   false,
		},
		{
			name:   "only CPU threshold set and below",
			cpuPct: 5, memPct: 90,
			thresholds: slumlordv1alpha1.IdleThresholds{CPUPercent: int32Ptr(20)},
			wantIdle:   true,
		},
		{
			name:   "only memory threshold set and below",
			cpuPct: 90, memPct: 5,
			thresholds: slumlordv1alpha1.IdleThresholds{MemoryPercent: int32Ptr(20)},
			wantIdle:   true,
		},
		{
			name:   "only CPU threshold set and above",
			cpuPct: 25, memPct: 5,
			thresholds: slumlordv1alpha1.IdleThresholds{CPUPercent: int32Ptr(20)},
			wantIdle:   false,
		},
		{
			name:       "neither threshold set",
			cpuPct:     5,
			memPct:     5,
			thresholds: slumlordv1alpha1.IdleThresholds{},
			wantIdle:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isIdleByThresholds(tt.cpuPct, tt.memPct, tt.thresholds)
			if got != tt.wantIdle {
				t.Errorf("isIdleByThresholds(%v, %v) = %v, want %v", tt.cpuPct, tt.memPct, got, tt.wantIdle)
			}
		})
	}
}

// --- Integration tests for checkWorkloadMetrics ---

func TestCheckWorkloadMetrics_DetectsIdle(t *testing.T) {
	ctx := context.Background()
	scheme := newTestScheme()
	namespace := "test-namespace"

	replicas := int32(1)
	deploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "idle-deploy",
			Namespace: namespace,
			Labels:    map[string]string{"app": "test"},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "test"}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "test"}},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  "app",
							Image: "nginx",
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("1000m"),
									corev1.ResourceMemory: resource.MustParse("1Gi"),
								},
							},
						},
					},
				},
			},
		},
	}

	pod := makePod("idle-deploy-pod-1", namespace, "1000m", "1Gi")
	pod.Labels = map[string]string{"app": "test"}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(deploy, pod).
		Build()

	// Low CPU/memory usage — should be idle
	fakeMetrics := newFakeMetricsClient(
		*makePodMetrics("idle-deploy-pod-1", namespace, "10m", "50Mi"),
	)

	reconciler := &IdleDetectorReconciler{
		Client:        fakeClient,
		Scheme:        scheme,
		MetricsClient: fakeMetrics,
	}

	thresholds := slumlordv1alpha1.IdleThresholds{
		CPUPercent:    int32Ptr(20),
		MemoryPercent: int32Ptr(20),
	}

	isIdle, cpuPct, memPct := reconciler.checkWorkloadMetrics(ctx, "Deployment", "idle-deploy", namespace, thresholds)
	if !isIdle {
		t.Error("Expected workload to be detected as idle")
	}
	if cpuPct == nil || *cpuPct > 5 {
		t.Errorf("Expected low CPU percent, got %v", cpuPct)
	}
	if memPct == nil || *memPct > 10 {
		t.Errorf("Expected low memory percent, got %v", memPct)
	}
}

func TestCheckWorkloadMetrics_NotIdle(t *testing.T) {
	ctx := context.Background()
	scheme := newTestScheme()
	namespace := "test-namespace"

	replicas := int32(1)
	deploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "busy-deploy",
			Namespace: namespace,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "busy"}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "busy"}},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  "app",
							Image: "nginx",
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("1000m"),
									corev1.ResourceMemory: resource.MustParse("1Gi"),
								},
							},
						},
					},
				},
			},
		},
	}

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "busy-deploy-pod-1",
			Namespace: namespace,
			Labels:    map[string]string{"app": "busy"},
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name:  "app",
					Image: "nginx",
					Resources: corev1.ResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("1000m"),
							corev1.ResourceMemory: resource.MustParse("1Gi"),
						},
					},
				},
			},
		},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(deploy, pod).
		Build()

	// High usage — should NOT be idle
	fakeMetrics := newFakeMetricsClient(
		*makePodMetrics("busy-deploy-pod-1", namespace, "800m", "900Mi"),
	)

	reconciler := &IdleDetectorReconciler{
		Client:        fakeClient,
		Scheme:        scheme,
		MetricsClient: fakeMetrics,
	}

	thresholds := slumlordv1alpha1.IdleThresholds{
		CPUPercent:    int32Ptr(20),
		MemoryPercent: int32Ptr(20),
	}

	isIdle, _, _ := reconciler.checkWorkloadMetrics(ctx, "Deployment", "busy-deploy", namespace, thresholds)
	if isIdle {
		t.Error("Expected workload to NOT be detected as idle (high usage)")
	}
}

func TestCheckWorkloadMetrics_NilMetricsClient(t *testing.T) {
	ctx := context.Background()
	scheme := newTestScheme()
	namespace := "test-namespace"

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		Build()

	reconciler := &IdleDetectorReconciler{
		Client:        fakeClient,
		Scheme:        scheme,
		MetricsClient: nil,
	}

	thresholds := slumlordv1alpha1.IdleThresholds{
		CPUPercent:    int32Ptr(20),
		MemoryPercent: int32Ptr(20),
	}

	isIdle, cpuPct, memPct := reconciler.checkWorkloadMetrics(ctx, "Deployment", "any", namespace, thresholds)
	if isIdle {
		t.Error("Expected not-idle when MetricsClient is nil")
	}
	if cpuPct != nil || memPct != nil {
		t.Error("Expected nil percentages when MetricsClient is nil")
	}
}

// --- Integration tests for checkCronJobMetrics ---

func TestCheckCronJobMetrics_NoActiveJobs(t *testing.T) {
	ctx := context.Background()
	scheme := newTestScheme()
	namespace := "test-namespace"

	// CronJob with no active Jobs
	suspend := false
	cj := &batchv1.CronJob{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-cj",
			Namespace: namespace,
		},
		Spec: batchv1.CronJobSpec{
			Schedule: "*/5 * * * *",
			Suspend:  &suspend,
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

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(cj).
		Build()

	fakeMetrics := newFakeMetricsClient()

	reconciler := &IdleDetectorReconciler{
		Client:        fakeClient,
		Scheme:        scheme,
		MetricsClient: fakeMetrics,
	}

	thresholds := slumlordv1alpha1.IdleThresholds{
		CPUPercent:    int32Ptr(20),
		MemoryPercent: int32Ptr(20),
	}

	isIdle, cpuPct, memPct := reconciler.checkCronJobMetrics(ctx, "test-cj", namespace, thresholds)
	if isIdle {
		t.Error("Expected not-idle when there are no active Jobs")
	}
	if cpuPct != nil || memPct != nil {
		t.Error("Expected nil percentages when no active Jobs")
	}
}

func TestCheckCronJobMetrics_WithActiveJob(t *testing.T) {
	ctx := context.Background()
	scheme := newTestScheme()
	namespace := "test-namespace"

	// Active Job owned by CronJob
	trueVal := true
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-cj-12345",
			Namespace: namespace,
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: "batch/v1",
					Kind:       "CronJob",
					Name:       "test-cj",
					Controller: &trueVal,
				},
			},
		},
		Spec: batchv1.JobSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"job-name": "test-cj-12345"},
			},
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					RestartPolicy: corev1.RestartPolicyOnFailure,
					Containers: []corev1.Container{
						{
							Name:  "worker",
							Image: "busybox",
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("500m"),
									corev1.ResourceMemory: resource.MustParse("256Mi"),
								},
							},
						},
					},
				},
			},
		},
		Status: batchv1.JobStatus{
			Active: 1,
		},
	}

	// Running pod for the Job
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-cj-12345-pod",
			Namespace: namespace,
			Labels:    map[string]string{"job-name": "test-cj-12345"},
		},
		Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyOnFailure,
			Containers: []corev1.Container{
				{
					Name:  "worker",
					Image: "busybox",
					Resources: corev1.ResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("500m"),
							corev1.ResourceMemory: resource.MustParse("256Mi"),
						},
					},
				},
			},
		},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(job, pod).
		Build()

	// Low usage — should detect idle
	fakeMetrics := newFakeMetricsClient(
		*makePodMetrics("test-cj-12345-pod", namespace, "10m", "20Mi"),
	)

	reconciler := &IdleDetectorReconciler{
		Client:        fakeClient,
		Scheme:        scheme,
		MetricsClient: fakeMetrics,
	}

	thresholds := slumlordv1alpha1.IdleThresholds{
		CPUPercent:    int32Ptr(20),
		MemoryPercent: int32Ptr(20),
	}

	isIdle, cpuPct, memPct := reconciler.checkCronJobMetrics(ctx, "test-cj", namespace, thresholds)
	if !isIdle {
		t.Error("Expected CronJob to be detected as idle (low usage active Job)")
	}
	if cpuPct == nil || *cpuPct > 5 {
		t.Errorf("Expected low CPU percent, got %v", cpuPct)
	}
	if memPct == nil || *memPct > 10 {
		t.Errorf("Expected low memory percent, got %v", memPct)
	}
}

// --- Unit tests for resize action ---

func TestIdleDetector_ResizeIdleWorkloads_BasicResize(t *testing.T) {
	ctx := context.Background()
	scheme := newTestScheme()
	namespace := "test-namespace"

	replicas := int32(1)
	deploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "idle-app",
			Namespace: namespace,
			Labels:    map[string]string{"app": "test"},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "test"}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "test"}},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name: "app", Image: "nginx",
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("1000m"),
									corev1.ResourceMemory: resource.MustParse("1Gi"),
								},
							},
						},
					},
				},
			},
		},
	}

	pod := makePod("idle-app-pod-1", namespace, "1000m", "1Gi")
	pod.Labels = map[string]string{"app": "test"}

	bufferPercent := int32(25)
	minCPU := resource.MustParse("50m")
	minMem := resource.MustParse("64Mi")

	detector := &slumlordv1alpha1.SlumlordIdleDetector{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "test-detector",
			Namespace:  namespace,
			Finalizers: []string{idleDetectorFinalizer},
		},
		Spec: slumlordv1alpha1.SlumlordIdleDetectorSpec{
			Selector: slumlordv1alpha1.WorkloadSelector{
				MatchLabels: map[string]string{"app": "test"},
				Types:       []string{"Deployment"},
			},
			Thresholds:   slumlordv1alpha1.IdleThresholds{CPUPercent: int32Ptr(20), MemoryPercent: int32Ptr(20)},
			IdleDuration: "1h",
			Action:       "resize",
			Resize: &slumlordv1alpha1.ResizeConfig{
				BufferPercent: &bufferPercent,
				MinRequests: &slumlordv1alpha1.MinRequests{
					CPU:    &minCPU,
					Memory: &minMem,
				},
			},
		},
		Status: slumlordv1alpha1.SlumlordIdleDetectorStatus{
			IdleWorkloads: []slumlordv1alpha1.IdleWorkload{
				{
					Kind:      "Deployment",
					Name:      "idle-app",
					IdleSince: metav1.NewTime(time.Now().Add(-2 * time.Hour)),
				},
			},
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(deploy, pod, detector).
		WithStatusSubresource(detector).
		Build()

	fakeMetrics := newFakeMetricsClient(
		*makePodMetrics("idle-app-pod-1", namespace, "50m", "100Mi"),
	)

	reconciler := &IdleDetectorReconciler{
		Client:        fakeClient,
		Scheme:        scheme,
		MetricsClient: fakeMetrics,
	}

	err := reconciler.resizeIdleWorkloads(ctx, detector, 1*time.Hour)
	if err != nil {
		t.Fatalf("resizeIdleWorkloads() error = %v", err)
	}

	// Verify resized workload tracked
	if len(detector.Status.ResizedWorkloads) != 1 {
		t.Fatalf("Expected 1 resized workload, got %d", len(detector.Status.ResizedWorkloads))
	}
	resized := detector.Status.ResizedWorkloads[0]
	if resized.Kind != "Deployment" || resized.Name != "idle-app" {
		t.Errorf("Expected Deployment/idle-app, got %s/%s", resized.Kind, resized.Name)
	}
	if resized.PodCount != 1 {
		t.Errorf("Expected PodCount = 1, got %d", resized.PodCount)
	}
	// Original should be 1000m CPU
	origCPU := resized.OriginalRequests[corev1.ResourceCPU]
	if origCPU.MilliValue() != 1000 {
		t.Errorf("Expected original CPU = 1000m, got %dm", origCPU.MilliValue())
	}
}

func TestIdleDetector_ResizeIdleWorkloads_FloorApplied(t *testing.T) {
	ctx := context.Background()
	scheme := newTestScheme()
	namespace := "test-namespace"

	replicas := int32(1)
	deploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "floor-app",
			Namespace: namespace,
			Labels:    map[string]string{"app": "test"},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "test"}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "test"}},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name: "app", Image: "nginx",
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("1000m"),
									corev1.ResourceMemory: resource.MustParse("1Gi"),
								},
							},
						},
					},
				},
			},
		},
	}

	pod := makePod("floor-app-pod-1", namespace, "1000m", "1Gi")
	pod.Labels = map[string]string{"app": "test"}

	bufferPercent := int32(25)
	minCPU := resource.MustParse("50m")
	minMem := resource.MustParse("64Mi")

	detector := &slumlordv1alpha1.SlumlordIdleDetector{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "test-detector",
			Namespace:  namespace,
			Finalizers: []string{idleDetectorFinalizer},
		},
		Spec: slumlordv1alpha1.SlumlordIdleDetectorSpec{
			Selector: slumlordv1alpha1.WorkloadSelector{
				MatchLabels: map[string]string{"app": "test"},
				Types:       []string{"Deployment"},
			},
			Thresholds:   slumlordv1alpha1.IdleThresholds{CPUPercent: int32Ptr(20)},
			IdleDuration: "1h",
			Action:       "resize",
			Resize: &slumlordv1alpha1.ResizeConfig{
				BufferPercent: &bufferPercent,
				MinRequests: &slumlordv1alpha1.MinRequests{
					CPU:    &minCPU,
					Memory: &minMem,
				},
			},
		},
		Status: slumlordv1alpha1.SlumlordIdleDetectorStatus{
			IdleWorkloads: []slumlordv1alpha1.IdleWorkload{
				{
					Kind:      "Deployment",
					Name:      "floor-app",
					IdleSince: metav1.NewTime(time.Now().Add(-2 * time.Hour)),
				},
			},
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(deploy, pod, detector).
		WithStatusSubresource(detector).
		Build()

	// Very low usage: 10m CPU, 20Mi memory
	fakeMetrics := newFakeMetricsClient(
		*makePodMetrics("floor-app-pod-1", namespace, "10m", "20Mi"),
	)

	reconciler := &IdleDetectorReconciler{
		Client:        fakeClient,
		Scheme:        scheme,
		MetricsClient: fakeMetrics,
	}

	err := reconciler.resizeIdleWorkloads(ctx, detector, 1*time.Hour)
	if err != nil {
		t.Fatalf("resizeIdleWorkloads() error = %v", err)
	}

	if len(detector.Status.ResizedWorkloads) != 1 {
		t.Fatalf("Expected 1 resized workload, got %d", len(detector.Status.ResizedWorkloads))
	}

	// Current requests should be at least the floor
	currentCPU := detector.Status.ResizedWorkloads[0].CurrentRequests[corev1.ResourceCPU]
	currentMem := detector.Status.ResizedWorkloads[0].CurrentRequests[corev1.ResourceMemory]

	if currentCPU.MilliValue() < 50 {
		t.Errorf("Expected CPU >= 50m (floor), got %dm", currentCPU.MilliValue())
	}
	if currentMem.Value() < 64*1024*1024 {
		t.Errorf("Expected memory >= 64Mi (floor), got %d", currentMem.Value())
	}
}

func TestIdleDetector_ResizeIdleWorkloads_SkipsCronJob(t *testing.T) {
	ctx := context.Background()
	scheme := newTestScheme()
	namespace := "test-namespace"

	detector := &slumlordv1alpha1.SlumlordIdleDetector{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "test-detector",
			Namespace:  namespace,
			Finalizers: []string{idleDetectorFinalizer},
		},
		Spec: slumlordv1alpha1.SlumlordIdleDetectorSpec{
			Selector: slumlordv1alpha1.WorkloadSelector{
				Types: []string{"CronJob"},
			},
			Thresholds:   slumlordv1alpha1.IdleThresholds{CPUPercent: int32Ptr(20)},
			IdleDuration: "1h",
			Action:       "resize",
		},
		Status: slumlordv1alpha1.SlumlordIdleDetectorStatus{
			IdleWorkloads: []slumlordv1alpha1.IdleWorkload{
				{
					Kind:      "CronJob",
					Name:      "my-cronjob",
					IdleSince: metav1.NewTime(time.Now().Add(-2 * time.Hour)),
				},
			},
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(detector).
		WithStatusSubresource(detector).
		Build()

	fakeMetrics := newFakeMetricsClient()

	reconciler := &IdleDetectorReconciler{
		Client:        fakeClient,
		Scheme:        scheme,
		MetricsClient: fakeMetrics,
	}

	err := reconciler.resizeIdleWorkloads(ctx, detector, 1*time.Hour)
	if err != nil {
		t.Fatalf("resizeIdleWorkloads() error = %v", err)
	}

	// CronJob should be skipped - no resized workloads
	if len(detector.Status.ResizedWorkloads) != 0 {
		t.Errorf("Expected 0 resized workloads (CronJob skipped), got %d", len(detector.Status.ResizedWorkloads))
	}
}

func TestIdleDetector_ResizeIdleWorkloads_NotIdleLongEnough(t *testing.T) {
	ctx := context.Background()
	scheme := newTestScheme()
	namespace := "test-namespace"

	replicas := int32(1)
	deploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "recent-idle",
			Namespace: namespace,
			Labels:    map[string]string{"app": "test"},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "test"}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "test"}},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: "app", Image: "nginx"}},
				},
			},
		},
	}

	detector := &slumlordv1alpha1.SlumlordIdleDetector{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "test-detector",
			Namespace:  namespace,
			Finalizers: []string{idleDetectorFinalizer},
		},
		Spec: slumlordv1alpha1.SlumlordIdleDetectorSpec{
			Selector: slumlordv1alpha1.WorkloadSelector{
				MatchLabels: map[string]string{"app": "test"},
				Types:       []string{"Deployment"},
			},
			Thresholds:   slumlordv1alpha1.IdleThresholds{CPUPercent: int32Ptr(20)},
			IdleDuration: "1h",
			Action:       "resize",
		},
		Status: slumlordv1alpha1.SlumlordIdleDetectorStatus{
			IdleWorkloads: []slumlordv1alpha1.IdleWorkload{
				{
					Kind:      "Deployment",
					Name:      "recent-idle",
					IdleSince: metav1.NewTime(time.Now().Add(-10 * time.Minute)), // only 10 min ago
				},
			},
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(deploy, detector).
		WithStatusSubresource(detector).
		Build()

	fakeMetrics := newFakeMetricsClient()

	reconciler := &IdleDetectorReconciler{
		Client:        fakeClient,
		Scheme:        scheme,
		MetricsClient: fakeMetrics,
	}

	err := reconciler.resizeIdleWorkloads(ctx, detector, 1*time.Hour)
	if err != nil {
		t.Fatalf("resizeIdleWorkloads() error = %v", err)
	}

	if len(detector.Status.ResizedWorkloads) != 0 {
		t.Errorf("Expected 0 resized workloads (not idle long enough), got %d", len(detector.Status.ResizedWorkloads))
	}
}

func TestIdleDetector_RestoreNoLongerIdleResizedWorkloads(t *testing.T) {
	ctx := context.Background()
	scheme := newTestScheme()
	namespace := "test-namespace"

	replicas := int32(1)
	deploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "recovered-app",
			Namespace: namespace,
			Labels:    map[string]string{"app": "test"},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "test"}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "test"}},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name: "app", Image: "nginx",
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("50m"),
									corev1.ResourceMemory: resource.MustParse("64Mi"),
								},
							},
						},
					},
				},
			},
		},
	}

	// Pod with current (resized) requests
	pod := makePod("recovered-app-pod-1", namespace, "50m", "64Mi")
	pod.Labels = map[string]string{"app": "test"}

	detector := &slumlordv1alpha1.SlumlordIdleDetector{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "test-detector",
			Namespace:  namespace,
			Finalizers: []string{idleDetectorFinalizer},
		},
		Spec: slumlordv1alpha1.SlumlordIdleDetectorSpec{
			Selector: slumlordv1alpha1.WorkloadSelector{
				MatchLabels: map[string]string{"app": "test"},
				Types:       []string{"Deployment"},
			},
			Thresholds:   slumlordv1alpha1.IdleThresholds{CPUPercent: int32Ptr(20)},
			IdleDuration: "1h",
			Action:       "resize",
		},
		Status: slumlordv1alpha1.SlumlordIdleDetectorStatus{
			// NOTE: IdleWorkloads is EMPTY - the workload recovered
			IdleWorkloads: nil,
			ResizedWorkloads: []slumlordv1alpha1.ResizedWorkload{
				{
					Kind: "Deployment",
					Name: "recovered-app",
					OriginalRequests: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("500m"),
						corev1.ResourceMemory: resource.MustParse("512Mi"),
					},
					CurrentRequests: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("50m"),
						corev1.ResourceMemory: resource.MustParse("64Mi"),
					},
					ResizedAt: metav1.Now(),
					PodCount:  1,
				},
			},
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(deploy, pod, detector).
		WithStatusSubresource(detector).
		Build()

	reconciler := &IdleDetectorReconciler{
		Client: fakeClient,
		Scheme: scheme,
	}

	err := reconciler.restoreNoLongerIdleResizedWorkloads(ctx, detector)
	if err != nil {
		t.Fatalf("restoreNoLongerIdleResizedWorkloads() error = %v", err)
	}

	// Resized workloads should be cleared
	if len(detector.Status.ResizedWorkloads) != 0 {
		t.Errorf("Expected 0 resized workloads after restore, got %d", len(detector.Status.ResizedWorkloads))
	}

	// Pod should have original requests restored
	var updatedPod corev1.Pod
	if err := fakeClient.Get(ctx, types.NamespacedName{Name: "recovered-app-pod-1", Namespace: namespace}, &updatedPod); err != nil {
		t.Fatalf("Failed to get pod: %v", err)
	}
	cpuReq := updatedPod.Spec.Containers[0].Resources.Requests[corev1.ResourceCPU]
	if cpuReq.MilliValue() != 500 {
		t.Errorf("Expected restored CPU = 500m, got %dm", cpuReq.MilliValue())
	}
}

func TestIdleDetector_ResizeIdleWorkloads_AlreadyResized(t *testing.T) {
	ctx := context.Background()
	scheme := newTestScheme()
	namespace := "test-namespace"

	detector := &slumlordv1alpha1.SlumlordIdleDetector{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "test-detector",
			Namespace:  namespace,
			Finalizers: []string{idleDetectorFinalizer},
		},
		Spec: slumlordv1alpha1.SlumlordIdleDetectorSpec{
			Selector: slumlordv1alpha1.WorkloadSelector{
				Types: []string{"Deployment"},
			},
			Thresholds:   slumlordv1alpha1.IdleThresholds{CPUPercent: int32Ptr(20)},
			IdleDuration: "1h",
			Action:       "resize",
		},
		Status: slumlordv1alpha1.SlumlordIdleDetectorStatus{
			IdleWorkloads: []slumlordv1alpha1.IdleWorkload{
				{
					Kind:      "Deployment",
					Name:      "already-resized",
					IdleSince: metav1.NewTime(time.Now().Add(-2 * time.Hour)),
				},
			},
			ResizedWorkloads: []slumlordv1alpha1.ResizedWorkload{
				{
					Kind: "Deployment",
					Name: "already-resized",
					OriginalRequests: corev1.ResourceList{
						corev1.ResourceCPU: resource.MustParse("500m"),
					},
					CurrentRequests: corev1.ResourceList{
						corev1.ResourceCPU: resource.MustParse("50m"),
					},
					ResizedAt: metav1.Now(),
					PodCount:  1,
				},
			},
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(detector).
		WithStatusSubresource(detector).
		Build()

	fakeMetrics := newFakeMetricsClient()

	reconciler := &IdleDetectorReconciler{
		Client:        fakeClient,
		Scheme:        scheme,
		MetricsClient: fakeMetrics,
	}

	err := reconciler.resizeIdleWorkloads(ctx, detector, 1*time.Hour)
	if err != nil {
		t.Fatalf("resizeIdleWorkloads() error = %v", err)
	}

	// Should still have exactly 1 resized workload (not duplicated)
	if len(detector.Status.ResizedWorkloads) != 1 {
		t.Errorf("Expected 1 resized workload (unchanged), got %d", len(detector.Status.ResizedWorkloads))
	}
}

func TestIdleDetector_ReconcileInterval(t *testing.T) {
	tenMin := metav1.Duration{Duration: 10 * time.Minute}
	threeMin := metav1.Duration{Duration: 3 * time.Minute}

	tests := []struct {
		name            string
		specInterval    *metav1.Duration
		defaultInterval time.Duration
		expected        time.Duration
	}{
		{
			name:            "default when nothing configured",
			specInterval:    nil,
			defaultInterval: 0,
			expected:        5 * time.Minute,
		},
		{
			name:            "spec overrides everything",
			specInterval:    &tenMin,
			defaultInterval: 3 * time.Minute,
			expected:        10 * time.Minute,
		},
		{
			name:            "global default used when no spec",
			specInterval:    nil,
			defaultInterval: 3 * time.Minute,
			expected:        3 * time.Minute,
		},
		{
			name:            "spec takes priority over global",
			specInterval:    &threeMin,
			defaultInterval: 10 * time.Minute,
			expected:        3 * time.Minute,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &IdleDetectorReconciler{
				DefaultReconcileInterval: tt.defaultInterval,
			}
			detector := &slumlordv1alpha1.SlumlordIdleDetector{
				Spec: slumlordv1alpha1.SlumlordIdleDetectorSpec{
					ReconcileInterval: tt.specInterval,
				},
			}
			got := r.reconcileInterval(detector)
			if got != tt.expected {
				t.Errorf("reconcileInterval() = %v, want %v", got, tt.expected)
			}
		})
	}
}
