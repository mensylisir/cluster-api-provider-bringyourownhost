// Copyright 2021 VMware, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package v1beta1

import (
	"k8s.io/apimachinery/pkg/api/resource"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
)

const (
	// MachineFinalizer allows ReconcileByoMachine to clean up Byo
	// resources associated with ByoMachine before removing it from the
	// API Server.
	MachineFinalizer = "byomachine.infrastructure.cluster.x-k8s.io"

	// Scale-from-zero and autoscaling annotations
	// See: https://cluster-api.sigs.k8s.io/tasks/automated-machine-management/autoscaling

	// CapacityCPUAnnotation defines CPU capacity for scale-from-zero
	CapacityCPUAnnotation = "capacity.cluster-autoscaler.kubernetes.io/cpu"
	// CapacityMemoryAnnotation defines memory capacity for scale-from-zero
	CapacityMemoryAnnotation = "capacity.cluster-autoscaler.kubernetes.io/memory"
	// CapacityEphemeralDiskAnnotation defines ephemeral disk capacity for scale-from-zero
	CapacityEphemeralDiskAnnotation = "capacity.cluster-autoscaler.kubernetes.io/ephemeral-disk"
	// CapacityMaxPodsAnnotation defines max pods for scale-from-zero
	CapacityMaxPodsAnnotation = "capacity.cluster-autoscaler.kubernetes.io/maxPods"
	// CapacityGPUTypeAnnotation defines GPU type for scale-from-zero
	CapacityGPUTypeAnnotation = "capacity.cluster-autoscaler.kubernetes.io/gpu-type"
	// CapacityGPUCountAnnotation defines GPU count for scale-from-zero
	CapacityGPUCountAnnotation = "capacity.cluster-autoscaler.kubernetes.io/gpu-count"
	// CapacityLabelsAnnotation defines labels for scale-from-zero
	CapacityLabelsAnnotation = "capacity.cluster-autoscaler.kubernetes.io/labels"
	// CapacityTaintsAnnotation defines taints for scale-from-zero
	CapacityTaintsAnnotation = "capacity.cluster-autoscaler.kubernetes.io/taints"
	// CapacityCSIDriversAnnotation defines CSI drivers for scale-from-zero
	CapacityCSIDriversAnnotation = "capacity.cluster-autoscaler.kubernetes.io/csi-driver"

	// Node group autoscaling annotations
	// NodeGroupMinSizeAnnotation defines minimum node group size for autoscaler
	NodeGroupMinSizeAnnotation = "cluster.x-k8s.io/cluster-api-autoscaler-node-group-min-size"
	// NodeGroupMaxSizeAnnotation defines maximum node group size for autoscaler
	NodeGroupMaxSizeAnnotation = "cluster.x-k8s.io/cluster-api-autoscaler-node-group-max-size"

	// Per-nodegroup autoscaling options
	AutoscalingOptionsScaleDownUtilizationThreshold = "cluster.x-k8s.io/autoscaling-options-scaledownutilizationthreshold"
	AutoscalingOptionsScaleDownUnneededTime         = "cluster.x-k8s.io/autoscaling-options-scaledownunneededtime"
	AutoscalingOptionsScaleDownUnreadyTime          = "cluster.x-k8s.io/autoscaling-options-scaledownunreadytime"
	AutoscalingOptionsMaxNodeProvisionTime          = "cluster.x-k8s.io/autoscaling-options-maxnodeprovisiontime"
	AutoscalingOptionsMaxNodeStartupTime            = "cluster.x-k8s.io/autoscaling-options-maxnodestartuptime"
)

// ByoMachineSpec defines the desired state of ByoMachine
type ByoMachineSpec struct {
	// Label Selector to choose the byohost
	Selector *metav1.LabelSelector `json:"selector,omitempty"`

	ProviderID string `json:"providerID,omitempty"`

	// InstallerRef is an optional reference to a installer-specific resource that holds
	// the details of InstallationSecret to be used to install BYOH Bundle.
	// +optional
	InstallerRef *corev1.ObjectReference `json:"installerRef,omitempty"`

	// BootstrapConfigRef is an optional reference to a bootstrap-specific resource
	// that holds the bootstrap configuration (e.g., BootstrapKubeconfig for TLS Bootstrap mode).
	// If not specified, the controller will generate the bootstrap configuration automatically.
	// +optional
	BootstrapConfigRef *corev1.ObjectReference `json:"bootstrapConfigRef,omitempty"`

	// JoinMode defines how the node joins the cluster.
	// - kubeadm: Use kubeadm join command (default)
	// - tlsBootstrap: Use TLS Bootstrapping mechanism
	// +kubebuilder:validation:Enum=kubeadm;tlsBootstrap
	// +optional
	JoinMode JoinMode `json:"joinMode,omitempty"`

	// DownloadMode defines how to obtain K8s binaries.
	// Only valid when JoinMode is tlsBootstrap.
	// - offline: Use locally existing binaries
	// - online: Download binaries from the network
	// +kubebuilder:validation:Enum=offline;online
	// +optional
	DownloadMode DownloadMode `json:"downloadMode,omitempty"`

	// KubernetesVersion is the K8s version for binaries (only for TLSBootstrap mode).
	// If not specified, it will be derived from the Machine or Cluster spec.
	// +optional
	KubernetesVersion string `json:"kubernetesVersion,omitempty"`

	// ManageKubeProxy determines whether Agent manages kube-proxy.
	// Only valid when JoinMode is tlsBootstrap.
	// - false: kube-proxy runs as DaemonSet (cloud native approach)
	// - true: Agent starts kube-proxy binary (binary deployment approach)
	// +optional
	ManageKubeProxy bool `json:"manageKubeProxy,omitempty"`

	// CapacityRequirements specifies the minimum capacity required for this machine.
	// The scheduler will only select hosts that have at least this capacity.
	// +optional
	CapacityRequirements map[corev1.ResourceName]resource.Quantity `json:"capacityRequirements,omitempty"`
}

// NetworkStatus provides information about one of a VM's networks.
type NetworkStatus struct {
	// Connected is a flag that indicates whether this network is currently
	// connected to the VM.
	Connected bool `json:"connected,omitempty"`

	// IPAddrs is one or more IP addresses reported by vm-tools.
	// +optional
	IPAddrs []string `json:"ipAddrs,omitempty"`

	// MACAddr is the MAC address of the network device.
	MACAddr string `json:"macAddr"`

	// NetworkInterfaceName is the name of the network interface.
	// +optional
	NetworkInterfaceName string `json:"networkInterfaceName,omitempty"`

	// IsDefault is a flag that indicates whether this interface name is where
	// the default gateway sit on.
	IsDefault bool `json:"isDefault,omitempty"`
}

// ByoMachineStatus defines the observed state of ByoMachine
type ByoMachineStatus struct {
	// INSERT ADDITIONAL STATUS FIELD - define observed state of cluster
	// Important: Run "make" to regenerate code after modifying this file

	// HostInfo has the attached host platform details.
	// +optional
	HostInfo HostInfo `json:"hostinfo,omitempty"`

	// +optional
	Ready bool `json:"ready"`

	// Conditions defines current service state of the BYOMachine.
	// +optional
	Conditions clusterv1.Conditions `json:"conditions,omitempty"`

	// CleanupStarted indicates that host cleanup has been initiated.
	// +optional
	CleanupStarted bool `json:"cleanupStarted,omitempty"`

	// CleanupCompleted indicates that host cleanup has finished.
	// +optional
	CleanupCompleted bool `json:"cleanupCompleted,omitempty"`

	// NodeRef is a reference to the created Node object.
	// +optional
	NodeRef *corev1.ObjectReference `json:"nodeRef,omitempty"`

	// NodeStartupTimeout indicates that the node startup has timed out.
	// This is set by MachineHealthCheck when nodeStartupTimeoutSeconds is exceeded.
	// +optional
	NodeStartupTimeout bool `json:"nodeStartupTimeout,omitempty"`

	// LastBootstrapTimestamp records the timestamp of the last bootstrap attempt.
	// +optional
	LastBootstrapTimestamp *metav1.Time `json:"lastBootstrapTimestamp,omitempty"`

	// Addresses contains the associated addresses for the machine.
	// These are propagated to Machine.status.addresses for user convenience.
	// +optional
	Addresses []clusterv1.MachineAddress `json:"addresses,omitempty"`
}

//+kubebuilder:object:root=true
//+kubebuilder:resource:path=byomachines,scope=Namespaced,shortName=byom
//+kubebuilder:subresource:status

// ByoMachine is the Schema for the byomachines API
type ByoMachine struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ByoMachineSpec   `json:"spec,omitempty"`
	Status ByoMachineStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// ByoMachineList contains a list of ByoMachine
type ByoMachineList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ByoMachine `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ByoMachine{}, &ByoMachineList{})
}

// GetConditions returns the conditions of ByoMachine status
func (byoMachine *ByoMachine) GetConditions() clusterv1.Conditions {
	return byoMachine.Status.Conditions
}

// SetConditions sets the conditions of ByoMachine status
func (byoMachine *ByoMachine) SetConditions(conditions clusterv1.Conditions) {
	byoMachine.Status.Conditions = conditions
}
