package controller

import (
	"context"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	slumlordv1alpha1 "github.com/cschockaert/slumlord/api/v1alpha1"
)

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
