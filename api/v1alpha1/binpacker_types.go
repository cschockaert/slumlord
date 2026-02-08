package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// SlumlordBinPackerSpec defines the desired state of SlumlordBinPacker
type SlumlordBinPackerSpec struct {
	// Action defines what to do with underutilized nodes
	// Valid values: "report", "consolidate"
	// +kubebuilder:validation:Enum=report;consolidate
	Action string `json:"action"`

	// NodeSelector filters which nodes to consider for bin packing
	// +optional
	NodeSelector map[string]string `json:"nodeSelector,omitempty"`

	// Namespaces limits pod analysis to these namespaces
	// +optional
	Namespaces []string `json:"namespaces,omitempty"`

	// ExcludeNamespaces skips pods in these namespaces
	// Defaults to kube-system, kube-public, kube-node-lease if not set
	// +optional
	ExcludeNamespaces []string `json:"excludeNamespaces,omitempty"`

	// Thresholds defines resource usage thresholds for candidate node detection
	Thresholds BinPackerThresholds `json:"thresholds"`

	// Schedule defines an optional time window for consolidation
	// +optional
	Schedule *SleepWindow `json:"schedule,omitempty"`

	// MaxEvictionsPerCycle caps the number of pod evictions per reconcile cycle
	// +optional
	// +kubebuilder:validation:Minimum=1
	MaxEvictionsPerCycle *int32 `json:"maxEvictionsPerCycle,omitempty"`

	// DryRun simulates consolidation without evicting pods
	// +optional
	DryRun bool `json:"dryRun,omitempty"`
}

// BinPackerThresholds defines resource usage thresholds for bin packing
type BinPackerThresholds struct {
	// CPURequestPercent is the CPU requests/allocatable percentage threshold (0-100).
	// Nodes below this are candidates for consolidation.
	// +optional
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=100
	CPURequestPercent *int32 `json:"cpuRequestPercent,omitempty"`

	// MemoryRequestPercent is the memory requests/allocatable percentage threshold (0-100).
	// Nodes below this are candidates for consolidation.
	// +optional
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=100
	MemoryRequestPercent *int32 `json:"memoryRequestPercent,omitempty"`
}

// CandidateNode represents a node identified as underutilized
type CandidateNode struct {
	// Name is the node name
	Name string `json:"name"`

	// CPURequestPercent is the current CPU requests/allocatable percentage
	CPURequestPercent int32 `json:"cpuRequestPercent"`

	// MemoryRequestPercent is the current memory requests/allocatable percentage
	MemoryRequestPercent int32 `json:"memoryRequestPercent"`

	// PodCount is the number of evictable pods on this node
	PodCount int32 `json:"podCount"`
}

// PlannedEviction represents a planned pod eviction
type PlannedEviction struct {
	// PodName is the pod to evict
	PodName string `json:"podName"`

	// Namespace is the pod namespace
	Namespace string `json:"namespace"`

	// SourceNode is the node the pod is currently on
	SourceNode string `json:"sourceNode"`

	// TargetNode is the suggested target node
	TargetNode string `json:"targetNode"`

	// Reason explains why this pod was selected
	Reason string `json:"reason"`
}

// SlumlordBinPackerStatus defines the observed state of SlumlordBinPacker
type SlumlordBinPackerStatus struct {
	// CandidateNodes lists nodes identified as underutilized
	// +optional
	CandidateNodes []CandidateNode `json:"candidateNodes,omitempty"`

	// CandidateNodeCount is the number of candidate nodes
	CandidateNodeCount int32 `json:"candidateNodeCount"`

	// ConsolidationPlan lists planned pod evictions
	// +optional
	ConsolidationPlan []PlannedEviction `json:"consolidationPlan,omitempty"`

	// LastAnalysisTime is the last time node analysis was performed
	// +optional
	LastAnalysisTime *metav1.Time `json:"lastAnalysisTime,omitempty"`

	// LastConsolidationTime is the last time pod evictions were performed
	// +optional
	LastConsolidationTime *metav1.Time `json:"lastConsolidationTime,omitempty"`

	// NodesAnalyzed is the total number of nodes analyzed
	NodesAnalyzed int32 `json:"nodesAnalyzed"`

	// PodsEvictedThisCycle is the number of pods evicted in the last cycle
	PodsEvictedThisCycle int32 `json:"podsEvictedThisCycle"`

	// TotalPodsEvicted is the cumulative number of pods evicted
	TotalPodsEvicted int64 `json:"totalPodsEvicted"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:printcolumn:name="Action",type="string",JSONPath=".spec.action"
// +kubebuilder:printcolumn:name="Candidates",type="integer",JSONPath=".status.candidateNodeCount"
// +kubebuilder:printcolumn:name="Evicted",type="integer",JSONPath=".status.podsEvictedThisCycle"
// +kubebuilder:printcolumn:name="Last Analysis",type="date",JSONPath=".status.lastAnalysisTime"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// SlumlordBinPacker is the Schema for the SlumlordBinPackers API
type SlumlordBinPacker struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   SlumlordBinPackerSpec   `json:"spec,omitempty"`
	Status SlumlordBinPackerStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// SlumlordBinPackerList contains a list of SlumlordBinPacker
type SlumlordBinPackerList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []SlumlordBinPacker `json:"items"`
}

func init() {
	SchemeBuilder.Register(&SlumlordBinPacker{}, &SlumlordBinPackerList{})
}
