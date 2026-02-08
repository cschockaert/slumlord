package dashboard

import (
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	slumlordv1alpha1 "github.com/cschockaert/slumlord/api/v1alpha1"
)

func newScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(s))
	utilruntime.Must(slumlordv1alpha1.AddToScheme(s))
	return s
}

func TestOverviewEmpty(t *testing.T) {
	cl := fake.NewClientBuilder().WithScheme(newScheme()).Build()
	srv := NewServer(":0", cl)

	req := httptest.NewRequest(http.MethodGet, "/api/overview", nil)
	rec := httptest.NewRecorder()
	srv.handleOverview(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var resp OverviewResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if resp.TotalSchedules != 0 {
		t.Errorf("expected 0 schedules, got %d", resp.TotalSchedules)
	}
	if resp.SleepingCount != 0 {
		t.Errorf("expected 0 sleeping, got %d", resp.SleepingCount)
	}
}

func TestOverviewWithSchedules(t *testing.T) {
	replicas := int32(3)
	cl := fake.NewClientBuilder().WithScheme(newScheme()).WithObjects(
		&slumlordv1alpha1.SlumlordSleepSchedule{
			ObjectMeta: metav1.ObjectMeta{Name: "sched1", Namespace: "ns1"},
			Spec: slumlordv1alpha1.SlumlordSleepScheduleSpec{
				Selector: slumlordv1alpha1.WorkloadSelector{Types: []string{"Deployment"}},
				Schedule: slumlordv1alpha1.SleepWindow{Start: "22:00", End: "06:00"},
			},
			Status: slumlordv1alpha1.SlumlordSleepScheduleStatus{
				Sleeping: true,
				ManagedWorkloads: []slumlordv1alpha1.ManagedWorkload{
					{Kind: "Deployment", Name: "app1", OriginalReplicas: &replicas},
					{Kind: "Deployment", Name: "app2", OriginalReplicas: &replicas},
				},
			},
		},
		&slumlordv1alpha1.SlumlordSleepSchedule{
			ObjectMeta: metav1.ObjectMeta{Name: "sched2", Namespace: "ns2"},
			Spec: slumlordv1alpha1.SlumlordSleepScheduleSpec{
				Selector: slumlordv1alpha1.WorkloadSelector{Types: []string{"StatefulSet"}},
				Schedule: slumlordv1alpha1.SleepWindow{Start: "20:00", End: "08:00"},
			},
			Status: slumlordv1alpha1.SlumlordSleepScheduleStatus{
				Sleeping: false,
			},
		},
	).Build()

	srv := NewServer(":0", cl)

	req := httptest.NewRequest(http.MethodGet, "/api/overview", nil)
	rec := httptest.NewRecorder()
	srv.handleOverview(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var resp OverviewResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if resp.TotalSchedules != 2 {
		t.Errorf("expected 2 schedules, got %d", resp.TotalSchedules)
	}
	if resp.SleepingCount != 1 {
		t.Errorf("expected 1 sleeping, got %d", resp.SleepingCount)
	}
	if resp.AwakeCount != 1 {
		t.Errorf("expected 1 awake, got %d", resp.AwakeCount)
	}
	if resp.ManagedWorkloads != 2 {
		t.Errorf("expected 2 managed workloads, got %d", resp.ManagedWorkloads)
	}
}

func TestSchedulesEndpoint(t *testing.T) {
	now := metav1.NewTime(time.Now())
	replicas := int32(2)
	cl := fake.NewClientBuilder().WithScheme(newScheme()).WithObjects(
		&slumlordv1alpha1.SlumlordSleepSchedule{
			ObjectMeta: metav1.ObjectMeta{Name: "nightly", Namespace: "default"},
			Spec: slumlordv1alpha1.SlumlordSleepScheduleSpec{
				Selector: slumlordv1alpha1.WorkloadSelector{
					MatchLabels: map[string]string{"env": "dev"},
					Types:       []string{"Deployment"},
				},
				Schedule: slumlordv1alpha1.SleepWindow{
					Start:    "22:00",
					End:      "06:00",
					Timezone: "Europe/Paris",
					Days:     []int{1, 2, 3, 4, 5},
				},
			},
			Status: slumlordv1alpha1.SlumlordSleepScheduleStatus{
				Sleeping:           true,
				DaysDisplay:        "Mon-Fri",
				LastTransitionTime: &now,
				ManagedWorkloads: []slumlordv1alpha1.ManagedWorkload{
					{Kind: "Deployment", Name: "web", OriginalReplicas: &replicas},
				},
			},
		},
	).Build()

	srv := NewServer(":0", cl)

	req := httptest.NewRequest(http.MethodGet, "/api/schedules", nil)
	rec := httptest.NewRecorder()
	srv.handleSchedules(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var result []ScheduleResponse
	if err := json.NewDecoder(rec.Body).Decode(&result); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if len(result) != 1 {
		t.Fatalf("expected 1 schedule, got %d", len(result))
	}

	sched := result[0]
	if sched.Namespace != "default" {
		t.Errorf("expected namespace default, got %s", sched.Namespace)
	}
	if sched.Name != "nightly" {
		t.Errorf("expected name nightly, got %s", sched.Name)
	}
	if !sched.Sleeping {
		t.Error("expected sleeping=true")
	}
	if sched.Start != "22:00" {
		t.Errorf("expected start 22:00, got %s", sched.Start)
	}
	if sched.Timezone != "Europe/Paris" {
		t.Errorf("expected timezone Europe/Paris, got %s", sched.Timezone)
	}
	if len(sched.ManagedWorkloads) != 1 {
		t.Errorf("expected 1 managed workload, got %d", len(sched.ManagedWorkloads))
	}
	if sched.LastTransitionTime == nil {
		t.Error("expected lastTransitionTime to be set")
	}
}

func TestSchedulesEmptyState(t *testing.T) {
	cl := fake.NewClientBuilder().WithScheme(newScheme()).Build()
	srv := NewServer(":0", cl)

	req := httptest.NewRequest(http.MethodGet, "/api/schedules", nil)
	rec := httptest.NewRecorder()
	srv.handleSchedules(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var result []ScheduleResponse
	if err := json.NewDecoder(rec.Body).Decode(&result); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if len(result) != 0 {
		t.Errorf("expected 0 schedules, got %d", len(result))
	}
}

func TestIdleDetectorsEndpoint(t *testing.T) {
	cpu := int32(10)
	mem := int32(20)
	cl := fake.NewClientBuilder().WithScheme(newScheme()).WithObjects(
		&slumlordv1alpha1.SlumlordIdleDetector{
			ObjectMeta: metav1.ObjectMeta{Name: "detector1", Namespace: "default"},
			Spec: slumlordv1alpha1.SlumlordIdleDetectorSpec{
				Selector:     slumlordv1alpha1.WorkloadSelector{Types: []string{"Deployment"}},
				Thresholds:   slumlordv1alpha1.IdleThresholds{CPUPercent: &cpu, MemoryPercent: &mem},
				IdleDuration: "30m",
				Action:       "alert",
			},
			Status: slumlordv1alpha1.SlumlordIdleDetectorStatus{
				IdleWorkloads: []slumlordv1alpha1.IdleWorkload{
					{Kind: "Deployment", Name: "idle-app", IdleSince: metav1.Now()},
				},
			},
		},
	).Build()

	srv := NewServer(":0", cl)

	req := httptest.NewRequest(http.MethodGet, "/api/idle-detectors", nil)
	rec := httptest.NewRecorder()
	srv.handleIdleDetectors(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var result []IdleDetectorResponse
	if err := json.NewDecoder(rec.Body).Decode(&result); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if len(result) != 1 {
		t.Fatalf("expected 1 detector, got %d", len(result))
	}
	if result[0].Action != "alert" {
		t.Errorf("expected action alert, got %s", result[0].Action)
	}
	if len(result[0].IdleWorkloads) != 1 {
		t.Errorf("expected 1 idle workload, got %d", len(result[0].IdleWorkloads))
	}
}

func TestIdleDetectorsEmpty(t *testing.T) {
	cl := fake.NewClientBuilder().WithScheme(newScheme()).Build()
	srv := NewServer(":0", cl)

	req := httptest.NewRequest(http.MethodGet, "/api/idle-detectors", nil)
	rec := httptest.NewRecorder()
	srv.handleIdleDetectors(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var result []IdleDetectorResponse
	if err := json.NewDecoder(rec.Body).Decode(&result); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if len(result) != 0 {
		t.Errorf("expected 0 detectors, got %d", len(result))
	}
}

func TestStaticFileServing(t *testing.T) {
	cl := fake.NewClientBuilder().WithScheme(newScheme()).Build()
	_ = NewServer(":0", cl)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()

	sub, err := fs.Sub(staticFiles, "static")
	if err != nil {
		t.Fatalf("failed to create sub fs: %v", err)
	}
	handler := http.FileServer(http.FS(sub))
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for static file, got %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct == "" {
		t.Error("expected Content-Type header to be set")
	}
}
