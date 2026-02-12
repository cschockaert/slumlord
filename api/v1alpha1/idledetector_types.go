package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
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
	// Valid values: "alert", "scale", "resize"
	// +kubebuilder:validation:Enum=alert;scale;resize
	Action string `json:"action"`

	// Resize configures the resize action parameters. Required when action=resize.
	// +optional
	Resize *ResizeConfig `json:"resize,omitempty"`
}

// ResizeConfig defines the configuration for the resize action
type ResizeConfig struct {
	// BufferPercent is the percentage of headroom to add above actual usage
	// when computing target requests. E.g., 25 means target = usage * 1.25
	// +optional
	// +kubebuilder:default=25
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=200
	BufferPercent *int32 `json:"bufferPercent,omitempty"`

	// MinRequests defines the minimum resource requests floor.
	// The controller will never resize below these values.
	// +optional
	MinRequests *MinRequests `json:"minRequests,omitempty"`
}

// MinRequests defines minimum resource requests that the resize action
// will never go below, to prevent starving the workload.
type MinRequests struct {
	// CPU is the minimum CPU request (e.g., "50m", "100m")
	// +optional
	// +kubebuilder:default="50m"
	CPU *resource.Quantity `json:"cpu,omitempty"`

	// Memory is the minimum memory request (e.g., "64Mi", "128Mi")
	// +optional
	// +kubebuilder:default="64Mi"
	Memory *resource.Quantity `json:"memory,omitempty"`
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

	// ResizedWorkloads tracks workloads whose pod requests were resized in-place
	// +optional
	ResizedWorkloads []ResizedWorkload `json:"resizedWorkloads,omitempty"`

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

// ResizedWorkload tracks a workload whose pod requests were resized in-place
type ResizedWorkload struct {
	// Kind is the workload kind (Deployment, StatefulSet)
	Kind string `json:"kind"`

	// Name is the workload name
	Name string `json:"name"`

	// OriginalRequests stores the original resource requests before resize
	OriginalRequests corev1.ResourceList `json:"originalRequests"`

	// CurrentRequests stores the current (resized) resource requests
	CurrentRequests corev1.ResourceList `json:"currentRequests"`

	// ResizedAt is when the workload pods were last resized
	ResizedAt metav1.Time `json:"resizedAt"`

	// PodCount is the number of pods that were resized
	PodCount int32 `json:"podCount"`
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
