package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// SlumlordNodeDrainPolicySpec defines the desired state of SlumlordNodeDrainPolicy
type SlumlordNodeDrainPolicySpec struct {
	// NodeSelector filters which nodes to consider for draining
	// +optional
	NodeSelector map[string]string `json:"nodeSelector,omitempty"`

	// Thresholds defines resource usage thresholds for candidate node detection.
	// A node is a candidate if CPU OR memory is below the threshold (OR logic).
	Thresholds DrainThresholds `json:"thresholds"`

	// Schedule defines when the drain policy runs (cron expression + timezone)
	Schedule DrainSchedule `json:"schedule"`

	// Drain configures the drain behavior
	// +optional
	Drain DrainConfig `json:"drain,omitempty"`

	// Safety configures safety constraints
	// +optional
	Safety DrainSafety `json:"safety,omitempty"`

	// Suspend prevents the policy from running when true
	// +optional
	Suspend bool `json:"suspend,omitempty"`

	// ReconcileInterval overrides the default reconciliation interval.
	// Controls how often the controller re-evaluates the policy when not on a cron tick.
	// Defaults to 6m30s if not specified.
	// +optional
	ReconcileInterval *metav1.Duration `json:"reconcileInterval,omitempty"`
}

// DrainThresholds defines resource usage thresholds for drain candidate detection.
// A node is a candidate if CPU OR memory requests percentage is below the threshold.
type DrainThresholds struct {
	// CPURequestPercent is the CPU requests/allocatable percentage threshold (0-100).
	// Nodes below this are candidates for draining.
	// +optional
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=100
	CPURequestPercent *int32 `json:"cpuRequestPercent,omitempty"`

	// MemoryRequestPercent is the memory requests/allocatable percentage threshold (0-100).
	// Nodes below this are candidates for draining.
	// +optional
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=100
	MemoryRequestPercent *int32 `json:"memoryRequestPercent,omitempty"`
}

// DrainSchedule defines the cron schedule for drain policy execution
type DrainSchedule struct {
	// Cron is the cron expression defining when the drain runs (e.g. "0 2 * * 1-5")
	// +kubebuilder:validation:MinLength=9
	Cron string `json:"cron"`

	// Timezone is the IANA timezone for the cron schedule (e.g. "Europe/Paris")
	// Defaults to UTC if not specified.
	// +optional
	Timezone string `json:"timezone,omitempty"`
}

// DrainConfig configures drain behavior
type DrainConfig struct {
	// GracePeriodSeconds is the grace period for pod termination.
	// If not set, uses each pod's terminationGracePeriodSeconds.
	// +optional
	// +kubebuilder:validation:Minimum=0
	GracePeriodSeconds *int64 `json:"gracePeriodSeconds,omitempty"`

	// TimeoutSeconds is the maximum time to wait for a node drain to complete.
	// Defaults to 300 (5 minutes).
	// +optional
	// +kubebuilder:validation:Minimum=30
	TimeoutSeconds *int32 `json:"timeoutSeconds,omitempty"`

	// DeleteEmptyDirData allows draining nodes with pods using emptyDir volumes.
	// +optional
	DeleteEmptyDirData bool `json:"deleteEmptyDirData,omitempty"`

	// IgnoreDaemonSets ignores DaemonSet pods when draining (they will not be evicted).
	// Defaults to true.
	// +optional
	IgnoreDaemonSets *bool `json:"ignoreDaemonSets,omitempty"`

	// DeleteNodeAfterDrain deletes the node object after a successful drain.
	// Useful for spot/preemptible nodes managed by a cloud autoscaler.
	// +optional
	DeleteNodeAfterDrain bool `json:"deleteNodeAfterDrain,omitempty"`

	// PodSelector limits eviction to pods matching these labels.
	// If empty, all non-exempt pods are evicted.
	// +optional
	PodSelector map[string]string `json:"podSelector,omitempty"`
}

// DrainSafety configures safety constraints for drain operations
type DrainSafety struct {
	// MaxNodesPerRun caps the number of nodes drained per run.
	// Defaults to 1.
	// +optional
	// +kubebuilder:validation:Minimum=1
	MaxNodesPerRun *int32 `json:"maxNodesPerRun,omitempty"`

	// MinReadyNodes is the minimum number of ready nodes that must remain after draining.
	// Defaults to 1.
	// +optional
	// +kubebuilder:validation:Minimum=0
	MinReadyNodes *int32 `json:"minReadyNodes,omitempty"`

	// DryRun simulates the drain without actually evicting pods or cordoning nodes.
	// +optional
	DryRun bool `json:"dryRun,omitempty"`
}

// SlumlordNodeDrainPolicyStatus defines the observed state of SlumlordNodeDrainPolicy
type SlumlordNodeDrainPolicyStatus struct {
	// Conditions represent the latest available observations of the policy's state
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// LastRun contains details about the most recent drain run
	// +optional
	LastRun *DrainRunStatus `json:"lastRun,omitempty"`

	// NextRun is the next scheduled run time
	// +optional
	NextRun *metav1.Time `json:"nextRun,omitempty"`

	// TotalNodesDrained is the cumulative number of nodes drained
	TotalNodesDrained int64 `json:"totalNodesDrained"`
}

// DrainRunStatus contains details about a drain run
type DrainRunStatus struct {
	// StartTime is when the run started
	StartTime metav1.Time `json:"startTime"`

	// CompletionTime is when the run completed
	// +optional
	CompletionTime *metav1.Time `json:"completionTime,omitempty"`

	// NodesEvaluated is the number of nodes that matched the selector
	NodesEvaluated int32 `json:"nodesEvaluated"`

	// NodesCandidates is the number of nodes below thresholds
	NodesCandidates int32 `json:"nodesCandidates"`

	// NodesDrained is the number of nodes successfully drained
	NodesDrained int32 `json:"nodesDrained"`

	// NodesDeleted is the number of nodes deleted after drain
	NodesDeleted int32 `json:"nodesDeleted"`

	// NodeResults contains per-node details
	// +optional
	NodeResults []DrainNodeResult `json:"nodeResults,omitempty"`
}

// DrainNodeResult contains the result of draining a single node
type DrainNodeResult struct {
	// Name is the node name
	Name string `json:"name"`

	// CPURequestPercent is the node's CPU utilization at evaluation time
	CPURequestPercent int32 `json:"cpuRequestPercent"`

	// MemoryRequestPercent is the node's memory utilization at evaluation time
	MemoryRequestPercent int32 `json:"memoryRequestPercent"`

	// Action is what was done: "drained", "deleted", "skipped", "failed", "dry-run"
	Action string `json:"action"`

	// Reason explains why this action was taken
	// +optional
	Reason string `json:"reason,omitempty"`

	// PodsEvicted is the number of pods evicted from this node
	PodsEvicted int32 `json:"podsEvicted"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:printcolumn:name="Schedule",type="string",JSONPath=".spec.schedule.cron"
// +kubebuilder:printcolumn:name="Suspended",type="boolean",JSONPath=".spec.suspend"
// +kubebuilder:printcolumn:name="Last Run",type="date",JSONPath=".status.lastRun.startTime"
// +kubebuilder:printcolumn:name="Drained",type="integer",JSONPath=".status.totalNodesDrained"
// +kubebuilder:printcolumn:name="Next Run",type="date",JSONPath=".status.nextRun"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// SlumlordNodeDrainPolicy is the Schema for the SlumlordNodeDrainPolicies API
type SlumlordNodeDrainPolicy struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   SlumlordNodeDrainPolicySpec   `json:"spec,omitempty"`
	Status SlumlordNodeDrainPolicyStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// SlumlordNodeDrainPolicyList contains a list of SlumlordNodeDrainPolicy
type SlumlordNodeDrainPolicyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []SlumlordNodeDrainPolicy `json:"items"`
}

func init() {
	SchemeBuilder.Register(&SlumlordNodeDrainPolicy{}, &SlumlordNodeDrainPolicyList{})
}
