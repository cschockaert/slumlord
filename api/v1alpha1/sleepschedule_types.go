package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// SlumlordSleepScheduleSpec defines the desired state of SlumlordSleepSchedule
type SlumlordSleepScheduleSpec struct {
	// Selector specifies which workloads to target
	Selector WorkloadSelector `json:"selector"`

	// Schedule defines when workloads should sleep
	Schedule SleepWindow `json:"schedule"`
}

// WorkloadSelector defines how to select workloads
type WorkloadSelector struct {
	// MatchLabels selects workloads by labels
	// +optional
	MatchLabels map[string]string `json:"matchLabels,omitempty"`

	// MatchNames selects workloads by name (supports wildcards)
	// +optional
	MatchNames []string `json:"matchNames,omitempty"`

	// Types specifies which workload types to target
	// Valid values: Deployment, StatefulSet, CronJob
	// +optional
	Types []string `json:"types,omitempty"`
}

// SleepWindow defines the time window for sleeping
type SleepWindow struct {
	// Start is the time to start sleeping (cron format for time, e.g., "22:00")
	Start string `json:"start"`

	// End is the time to wake up (cron format for time, e.g., "06:00")
	End string `json:"end"`

	// Timezone is the timezone for the schedule (e.g., "Europe/Paris", "UTC")
	// +optional
	Timezone string `json:"timezone,omitempty"`

	// Days specifies which days the schedule applies (0=Sunday, 6=Saturday)
	// If empty, applies every day
	// +optional
	Days []int `json:"days,omitempty"`
}

// SlumlordSleepScheduleStatus defines the observed state of SlumlordSleepSchedule
type SlumlordSleepScheduleStatus struct {
	// Sleeping indicates whether workloads are currently sleeping
	Sleeping bool `json:"sleeping"`

	// ManagedWorkloads lists the workloads being managed
	// +optional
	ManagedWorkloads []ManagedWorkload `json:"managedWorkloads,omitempty"`

	// LastTransitionTime is the last time the sleep state changed
	// +optional
	LastTransitionTime *metav1.Time `json:"lastTransitionTime,omitempty"`
}

// ManagedWorkload tracks a workload managed by this schedule
type ManagedWorkload struct {
	// Kind is the workload kind (Deployment, StatefulSet, CronJob)
	Kind string `json:"kind"`

	// Name is the workload name
	Name string `json:"name"`

	// OriginalReplicas stores the replica count before sleeping (for Deployment/StatefulSet)
	// +optional
	OriginalReplicas *int32 `json:"originalReplicas,omitempty"`

	// OriginalSuspend stores the suspend state before sleeping (for CronJob)
	// +optional
	OriginalSuspend *bool `json:"originalSuspend,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Sleeping",type="boolean",JSONPath=".status.sleeping"
// +kubebuilder:printcolumn:name="Start",type="string",JSONPath=".spec.schedule.start"
// +kubebuilder:printcolumn:name="End",type="string",JSONPath=".spec.schedule.end"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// SlumlordSleepSchedule is the Schema for the SlumlordSleepSchedules API
type SlumlordSleepSchedule struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   SlumlordSleepScheduleSpec   `json:"spec,omitempty"`
	Status SlumlordSleepScheduleStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// SlumlordSleepScheduleList contains a list of SlumlordSleepSchedule
type SlumlordSleepScheduleList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []SlumlordSleepSchedule `json:"items"`
}

func init() {
	SchemeBuilder.Register(&SlumlordSleepSchedule{}, &SlumlordSleepScheduleList{})
}
