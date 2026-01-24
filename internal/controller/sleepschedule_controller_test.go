package controller

import (
	"context"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	slumlordv1alpha1 "github.com/cschockaert/slumlord/api/v1alpha1"
)

func TestShouldBeSleeping(t *testing.T) {
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

// Integration tests using fake client
var _ = Describe("SleepScheduleReconciler", func() {
	var (
		ctx        context.Context
		reconciler *SleepScheduleReconciler
		fakeClient client.Client
		namespace  string
	)

	BeforeEach(func() {
		ctx = context.Background()
		namespace = "test-namespace"

		s := scheme.Scheme
		Expect(slumlordv1alpha1.AddToScheme(s)).To(Succeed())

		fakeClient = fake.NewClientBuilder().
			WithScheme(s).
			Build()

		reconciler = &SleepScheduleReconciler{
			Client: fakeClient,
			Scheme: s,
		}
	})

	Describe("Reconcile", func() {
		It("should scale down deployments when sleeping", func() {
			// Create a deployment
			replicas := int32(3)
			deploy := &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-deploy",
					Namespace: namespace,
					Labels: map[string]string{
						"app": "test",
					},
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
			Expect(fakeClient.Create(ctx, deploy)).To(Succeed())

			// Create sleep schedule
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
			Expect(fakeClient.Create(ctx, schedule)).To(Succeed())

			// Reconcile
			_, err := reconciler.Reconcile(ctx, ctrl.Request{
				NamespacedName: types.NamespacedName{
					Name:      schedule.Name,
					Namespace: schedule.Namespace,
				},
			})
			Expect(err).NotTo(HaveOccurred())

			// Verify deployment scaled to 0
			var updatedDeploy appsv1.Deployment
			Expect(fakeClient.Get(ctx, types.NamespacedName{
				Name:      deploy.Name,
				Namespace: deploy.Namespace,
			}, &updatedDeploy)).To(Succeed())
			Expect(*updatedDeploy.Spec.Replicas).To(Equal(int32(0)))

			// Verify status updated
			var updatedSchedule slumlordv1alpha1.SlumlordSleepSchedule
			Expect(fakeClient.Get(ctx, types.NamespacedName{
				Name:      schedule.Name,
				Namespace: schedule.Namespace,
			}, &updatedSchedule)).To(Succeed())
			Expect(updatedSchedule.Status.Sleeping).To(BeTrue())
			Expect(updatedSchedule.Status.ManagedWorkloads).To(HaveLen(1))
			Expect(*updatedSchedule.Status.ManagedWorkloads[0].OriginalReplicas).To(Equal(int32(3)))
		})

		It("should suspend cronjobs when sleeping", func() {
			// Create a cronjob
			suspend := false
			cj := &batchv1.CronJob{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-cronjob",
					Namespace: namespace,
					Labels: map[string]string{
						"app": "test",
					},
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
			Expect(fakeClient.Create(ctx, cj)).To(Succeed())

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
			Expect(fakeClient.Create(ctx, schedule)).To(Succeed())

			// Reconcile
			_, err := reconciler.Reconcile(ctx, ctrl.Request{
				NamespacedName: types.NamespacedName{
					Name:      schedule.Name,
					Namespace: schedule.Namespace,
				},
			})
			Expect(err).NotTo(HaveOccurred())

			// Verify cronjob suspended
			var updatedCJ batchv1.CronJob
			Expect(fakeClient.Get(ctx, types.NamespacedName{
				Name:      cj.Name,
				Namespace: cj.Namespace,
			}, &updatedCJ)).To(Succeed())
			Expect(*updatedCJ.Spec.Suspend).To(BeTrue())
		})
	})
})

func TestMain(m *testing.M) {
	RegisterFailHandler(Fail)
	m.Run()
}
