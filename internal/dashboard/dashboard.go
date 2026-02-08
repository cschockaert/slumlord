package dashboard

import (
	"context"
	"embed"
	"encoding/json"
	"io/fs"
	"net"
	"net/http"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	slumlordv1alpha1 "github.com/cschockaert/slumlord/api/v1alpha1"
)

//go:embed static
var staticFiles embed.FS

var log = logf.Log.WithName("dashboard")

// Server serves the dashboard UI and API endpoints.
// It implements manager.Runnable so it can be registered with the controller manager.
type Server struct {
	addr   string
	reader client.Reader
}

// NewServer creates a new dashboard server.
func NewServer(addr string, reader client.Reader) *Server {
	return &Server{addr: addr, reader: reader}
}

// Start implements manager.Runnable. It starts the HTTP server and blocks until
// the context is cancelled.
func (s *Server) Start(ctx context.Context) error {
	mux := http.NewServeMux()

	mux.HandleFunc("/api/overview", s.handleOverview)
	mux.HandleFunc("/api/schedules", s.handleSchedules)
	mux.HandleFunc("/api/idle-detectors", s.handleIdleDetectors)

	staticFS, err := fs.Sub(staticFiles, "static")
	if err != nil {
		return err
	}
	mux.Handle("/", http.FileServer(http.FS(staticFS)))

	srv := &http.Server{
		Addr:              s.addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		BaseContext:       func(_ net.Listener) context.Context { return ctx },
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	log.Info("starting dashboard server", "addr", s.addr)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

// OverviewResponse is the JSON response for GET /api/overview.
type OverviewResponse struct {
	TotalSchedules     int `json:"totalSchedules"`
	SleepingCount      int `json:"sleepingCount"`
	AwakeCount         int `json:"awakeCount"`
	ManagedWorkloads   int `json:"managedWorkloads"`
	TotalIdleDetectors int `json:"totalIdleDetectors"`
	IdleWorkloads      int `json:"idleWorkloads"`
	ScaledWorkloads    int `json:"scaledWorkloads"`
}

func (s *Server) handleOverview(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	var schedules slumlordv1alpha1.SlumlordSleepScheduleList
	if err := s.reader.List(ctx, &schedules); err != nil {
		http.Error(w, "failed to list schedules", http.StatusInternalServerError)
		return
	}

	resp := OverviewResponse{
		TotalSchedules: len(schedules.Items),
	}
	for _, sched := range schedules.Items {
		if sched.Status.Sleeping {
			resp.SleepingCount++
		} else {
			resp.AwakeCount++
		}
		resp.ManagedWorkloads += len(sched.Status.ManagedWorkloads)
	}

	var detectors slumlordv1alpha1.SlumlordIdleDetectorList
	if err := s.reader.List(ctx, &detectors); err == nil {
		resp.TotalIdleDetectors = len(detectors.Items)
		for _, d := range detectors.Items {
			resp.IdleWorkloads += len(d.Status.IdleWorkloads)
			resp.ScaledWorkloads += len(d.Status.ScaledWorkloads)
		}
	}

	writeJSON(w, resp)
}

// ScheduleResponse is the JSON response for a single schedule in GET /api/schedules.
type ScheduleResponse struct {
	Namespace          string                             `json:"namespace"`
	Name               string                             `json:"name"`
	Sleeping           bool                               `json:"sleeping"`
	Start              string                             `json:"start"`
	End                string                             `json:"end"`
	Timezone           string                             `json:"timezone"`
	Days               []int                              `json:"days"`
	DaysDisplay        string                             `json:"daysDisplay"`
	ManagedWorkloads   []slumlordv1alpha1.ManagedWorkload `json:"managedWorkloads"`
	LastTransitionTime *string                            `json:"lastTransitionTime"`
	MatchLabels        map[string]string                  `json:"matchLabels,omitempty"`
	MatchNames         []string                           `json:"matchNames,omitempty"`
	Types              []string                           `json:"types,omitempty"`
}

func (s *Server) handleSchedules(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	var schedules slumlordv1alpha1.SlumlordSleepScheduleList
	if err := s.reader.List(ctx, &schedules); err != nil {
		http.Error(w, "failed to list schedules", http.StatusInternalServerError)
		return
	}

	result := make([]ScheduleResponse, 0, len(schedules.Items))
	for _, sched := range schedules.Items {
		sr := ScheduleResponse{
			Namespace:        sched.Namespace,
			Name:             sched.Name,
			Sleeping:         sched.Status.Sleeping,
			Start:            sched.Spec.Schedule.Start,
			End:              sched.Spec.Schedule.End,
			Timezone:         sched.Spec.Schedule.Timezone,
			Days:             sched.Spec.Schedule.Days,
			DaysDisplay:      sched.Status.DaysDisplay,
			ManagedWorkloads: sched.Status.ManagedWorkloads,
			MatchLabels:      sched.Spec.Selector.MatchLabels,
			MatchNames:       sched.Spec.Selector.MatchNames,
			Types:            sched.Spec.Selector.Types,
		}
		if sr.Timezone == "" {
			sr.Timezone = "UTC"
		}
		if sr.ManagedWorkloads == nil {
			sr.ManagedWorkloads = []slumlordv1alpha1.ManagedWorkload{}
		}
		if sched.Status.LastTransitionTime != nil {
			t := sched.Status.LastTransitionTime.Format(time.RFC3339)
			sr.LastTransitionTime = &t
		}
		result = append(result, sr)
	}

	writeJSON(w, result)
}

// IdleDetectorResponse is the JSON response for a single idle detector.
type IdleDetectorResponse struct {
	Namespace       string                            `json:"namespace"`
	Name            string                            `json:"name"`
	Action          string                            `json:"action"`
	IdleDuration    string                            `json:"idleDuration"`
	CPUPercent      *int32                            `json:"cpuPercent"`
	MemoryPercent   *int32                            `json:"memoryPercent"`
	IdleWorkloads   []slumlordv1alpha1.IdleWorkload   `json:"idleWorkloads"`
	ScaledWorkloads []slumlordv1alpha1.ScaledWorkload `json:"scaledWorkloads"`
	LastCheckTime   *string                           `json:"lastCheckTime"`
	MatchLabels     map[string]string                 `json:"matchLabels,omitempty"`
	MatchNames      []string                          `json:"matchNames,omitempty"`
	Types           []string                          `json:"types,omitempty"`
}

func (s *Server) handleIdleDetectors(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	var detectors slumlordv1alpha1.SlumlordIdleDetectorList
	if err := s.reader.List(ctx, &detectors); err != nil {
		// CRD might not be installed — return empty array
		writeJSON(w, []IdleDetectorResponse{})
		return
	}

	result := make([]IdleDetectorResponse, 0, len(detectors.Items))
	for _, d := range detectors.Items {
		dr := IdleDetectorResponse{
			Namespace:       d.Namespace,
			Name:            d.Name,
			Action:          d.Spec.Action,
			IdleDuration:    d.Spec.IdleDuration,
			CPUPercent:      d.Spec.Thresholds.CPUPercent,
			MemoryPercent:   d.Spec.Thresholds.MemoryPercent,
			IdleWorkloads:   d.Status.IdleWorkloads,
			ScaledWorkloads: d.Status.ScaledWorkloads,
			MatchLabels:     d.Spec.Selector.MatchLabels,
			MatchNames:      d.Spec.Selector.MatchNames,
			Types:           d.Spec.Selector.Types,
		}
		if dr.IdleWorkloads == nil {
			dr.IdleWorkloads = []slumlordv1alpha1.IdleWorkload{}
		}
		if dr.ScaledWorkloads == nil {
			dr.ScaledWorkloads = []slumlordv1alpha1.ScaledWorkload{}
		}
		if d.Status.LastCheckTime != nil {
			t := d.Status.LastCheckTime.Format(time.RFC3339)
			dr.LastCheckTime = &t
		}
		result = append(result, dr)
	}

	writeJSON(w, result)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Error(err, "failed to encode JSON response")
	}
}
