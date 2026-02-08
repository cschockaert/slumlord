package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// SlumlordIdleDetectorSpec defines the desired state of SlumlordIdleDetector
type SlumlordIdleDetectorSpec struct {
	// Selector specifies which workloads to monitor
	Selector WorkloadSelector `json:"selector"`

	// Thresholds defines the resource usage thresholds for idle detection
	Thresholds IdleThresholds `json:"thresholds"`

	// IdleDuration is how long a workload must be idle before action (e.g., "30m", "1h")
	// +kubebuilder:validation:Pattern=`^([0-9]+h)?([0-9]+m)?([0-9]+s)?$`
	IdleDuration string `json:"idleDuration"`

	// Action defines what to do when a workload is detected as idle
	// Valid values: "alert", "scale"
	// +kubebuilder:validation:Enum=alert;scale
	Action string `json:"action"`
}

// IdleThresholds defines CPU and memory thresholds for idle detection
type IdleThresholds struct {
	// CPUPercent is the CPU usage percentage threshold (0-100)
	// Workloads using less than this are considered idle
	// +optional
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=100
	CPUPercent *int32 `json:"cpuPercent,omitempty"`

	// MemoryPercent is the memory usage percentage threshold (0-100)
	// Workloads using less than this are considered idle
	// +optional
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=100
	MemoryPercent *int32 `json:"memoryPercent,omitempty"`
}

// SlumlordIdleDetectorStatus defines the observed state of SlumlordIdleDetector
type SlumlordIdleDetectorStatus struct {
	// IdleWorkloads lists workloads detected as idle
	// +optional
	IdleWorkloads []IdleWorkload `json:"idleWorkloads,omitempty"`

	// ScaledWorkloads tracks workloads that have been scaled down due to idleness
	// +optional
	ScaledWorkloads []ScaledWorkload `json:"scaledWorkloads,omitempty"`

	// LastCheckTime is the last time idle detection was performed
	// +optional
	LastCheckTime *metav1.Time `json:"lastCheckTime,omitempty"`
}

// IdleWorkload represents a workload detected as idle
type IdleWorkload struct {
	// Kind is the workload kind (Deployment, StatefulSet, CronJob)
	Kind string `json:"kind"`

	// Name is the workload name
	Name string `json:"name"`

	// IdleSince is when the workload was first detected as idle
	IdleSince metav1.Time `json:"idleSince"`

	// CurrentCPUPercent is the current CPU usage percentage
	// +optional
	CurrentCPUPercent *int32 `json:"currentCPUPercent,omitempty"`

	// CurrentMemoryPercent is the current memory usage percentage
	// +optional
	CurrentMemoryPercent *int32 `json:"currentMemoryPercent,omitempty"`
}

// ScaledWorkload tracks a workload that was scaled down due to idleness
type ScaledWorkload struct {
	// Kind is the workload kind (Deployment, StatefulSet, CronJob)
	Kind string `json:"kind"`

	// Name is the workload name
	Name string `json:"name"`

	// OriginalReplicas stores the replica count before scaling (for Deployment/StatefulSet)
	// +optional
	OriginalReplicas *int32 `json:"originalReplicas,omitempty"`

	// OriginalSuspend stores the suspend state before scaling (for CronJob)
	// +optional
	OriginalSuspend *bool `json:"originalSuspend,omitempty"`

	// ScaledAt is when the workload was scaled down
	ScaledAt metav1.Time `json:"scaledAt"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Action",type="string",JSONPath=".spec.action"
// +kubebuilder:printcolumn:name="Idle Duration",type="string",JSONPath=".spec.idleDuration"
// +kubebuilder:printcolumn:name="Last Check",type="date",JSONPath=".status.lastCheckTime"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// SlumlordIdleDetector is the Schema for the SlumlordIdleDetectors API
type SlumlordIdleDetector struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   SlumlordIdleDetectorSpec   `json:"spec,omitempty"`
	Status SlumlordIdleDetectorStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// SlumlordIdleDetectorList contains a list of SlumlordIdleDetector
type SlumlordIdleDetectorList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []SlumlordIdleDetector `json:"items"`
}

func init() {
	SchemeBuilder.Register(&SlumlordIdleDetector{}, &SlumlordIdleDetectorList{})
}
