package controller

import (
	"context"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
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

func TestIdleDetector_Reconcile_MetricsStubReturnsNoIdle(t *testing.T) {
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
		Client: fakeClient,
		Scheme: scheme,
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

	// Deployment should NOT be scaled down (metrics stub returns not idle)
	var updatedDeploy appsv1.Deployment
	if err := fakeClient.Get(ctx, types.NamespacedName{Name: deploy.Name, Namespace: deploy.Namespace}, &updatedDeploy); err != nil {
		t.Fatalf("Failed to get deployment: %v", err)
	}
	if *updatedDeploy.Spec.Replicas != 3 {
		t.Errorf("Expected replicas = 3 (unchanged, stub returns not idle), got %d", *updatedDeploy.Spec.Replicas)
	}

	// Status should show no idle workloads
	var updatedDetector slumlordv1alpha1.SlumlordIdleDetector
	if err := fakeClient.Get(ctx, types.NamespacedName{Name: detector.Name, Namespace: detector.Namespace}, &updatedDetector); err != nil {
		t.Fatalf("Failed to get detector: %v", err)
	}
	if len(updatedDetector.Status.IdleWorkloads) != 0 {
		t.Errorf("Expected 0 idle workloads (stub returns not idle), got %d", len(updatedDetector.Status.IdleWorkloads))
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

	// Deployment should still be running (metrics stub returns not-idle),
	// but this verifies that having only MatchNames (no MatchLabels) works without error
	var updatedDeploy appsv1.Deployment
	if err := fakeClient.Get(ctx, types.NamespacedName{Name: "prod-api", Namespace: namespace}, &updatedDeploy); err != nil {
		t.Fatalf("Failed to get deployment: %v", err)
	}
	if *updatedDeploy.Spec.Replicas != 3 {
		t.Errorf("Expected replicas = 3, got %d", *updatedDeploy.Spec.Replicas)
	}
}
