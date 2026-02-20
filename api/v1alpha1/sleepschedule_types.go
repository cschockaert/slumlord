package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// SlumlordSleepScheduleSpec defines the desired state of SlumlordSleepSchedule
type SlumlordSleepScheduleSpec struct {
	// Suspend prevents the schedule from running when true.
	// If workloads are currently sleeping, they will be woken up.
	// +optional
	Suspend bool `json:"suspend,omitempty"`

	// Selector specifies which workloads to target
	Selector WorkloadSelector `json:"selector"`

	// Schedule defines when workloads should sleep
	Schedule SleepWindow `json:"schedule"`

	// ReconcileInterval overrides the default reconciliation interval.
	// Controls how often the controller re-checks the schedule state.
	// Defaults to 5m if not specified.
	// +optional
	ReconcileInterval *metav1.Duration `json:"reconcileInterval,omitempty"`
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
	// Valid values: Deployment, StatefulSet, CronJob, Cluster, HelmRelease, Kustomization, ThanosRuler, Alertmanager, Prometheus, MariaDB, MaxScale, Elasticsearch, Kibana
	// +optional
	Types []string `json:"types,omitempty"`
}

// SleepWindow defines the time window for sleeping
type SleepWindow struct {
	// Start is the time to start sleeping (cron format for time, e.g., "22:00")
	// +kubebuilder:validation:Pattern="^([01]?[0-9]|2[0-3]):[0-5][0-9]$"
	Start string `json:"start"`

	// End is the time to wake up (cron format for time, e.g., "06:00")
	// +kubebuilder:validation:Pattern="^([01]?[0-9]|2[0-3]):[0-5][0-9]$"
	End string `json:"end"`

	// Timezone is the timezone for the schedule (e.g., "Europe/Paris", "UTC")
	// Defaults to UTC if not specified.
	// +optional
	Timezone string `json:"timezone,omitempty"`

	// Days specifies which days the schedule applies (0=Sunday, 6=Saturday)
	// If empty, applies every day
	// +optional
	// +kubebuilder:validation:MaxItems=7
	Days []int `json:"days,omitempty"`
}

// SlumlordSleepScheduleStatus defines the observed state of SlumlordSleepSchedule
type SlumlordSleepScheduleStatus struct {
	// Sleeping indicates whether workloads are currently sleeping
	Sleeping bool `json:"sleeping"`

	// DaysDisplay is a human-readable representation of the scheduled days
	// +optional
	DaysDisplay string `json:"daysDisplay,omitempty"`

	// ManagedWorkloads lists the workloads being managed
	// +optional
	ManagedWorkloads []ManagedWorkload `json:"managedWorkloads,omitempty"`

	// LastTransitionTime is the last time the sleep state changed
	// +optional
	LastTransitionTime *metav1.Time `json:"lastTransitionTime,omitempty"`

	// Conditions represent the latest available observations
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// ManagedWorkload tracks a workload managed by this schedule
type ManagedWorkload struct {
	// Kind is the workload kind (Deployment, StatefulSet, CronJob, Cluster, HelmRelease, Kustomization, ThanosRuler, Alertmanager, Prometheus, MariaDB, MaxScale, Elasticsearch, Kibana)
	Kind string `json:"kind"`

	// Name is the workload name
	Name string `json:"name"`

	// OriginalReplicas stores the replica count before sleeping (for Deployment/StatefulSet)
	// +optional
	OriginalReplicas *int32 `json:"originalReplicas,omitempty"`

	// OriginalSuspend stores the suspend state before sleeping (for CronJob, HelmRelease, Kustomization, MariaDB, MaxScale)
	// +optional
	OriginalSuspend *bool `json:"originalSuspend,omitempty"`

	// OriginalHibernation stores the hibernation annotation value before sleeping (for CNPG Cluster)
	// +optional
	OriginalHibernation *string `json:"originalHibernation,omitempty"`

	// OriginalNodeSetCounts stores the original nodeSet counts as JSON (for Elasticsearch)
	// Format: {"nodeSetName": count, ...}
	// +optional
	OriginalNodeSetCounts *string `json:"originalNodeSetCounts,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Suspended",type="boolean",JSONPath=".spec.suspend"
// +kubebuilder:printcolumn:name="Sleeping",type="boolean",JSONPath=".status.sleeping"
// +kubebuilder:printcolumn:name="Start",type="string",JSONPath=".spec.schedule.start"
// +kubebuilder:printcolumn:name="End",type="string",JSONPath=".spec.schedule.end"
// +kubebuilder:printcolumn:name="Days",type="string",JSONPath=".status.daysDisplay"
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
