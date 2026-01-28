// Copyright 2021 VMware, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package v1beta1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ByoMachineTemplateSpec defines the desired state of ByoMachineTemplate
type ByoMachineTemplateSpec struct {
	// INSERT ADDITIONAL SPEC FIELDS - desired state of cluster
	// Important: Run "make" to regenerate code after modifying this file

	Template ByoMachineTemplateResource `json:"template"`

	// Capacity defines the capacity information for scale-from-zero support.
	// These annotations will be applied to MachineDeployments to instruct
	// the Cluster Autoscaler about node sizing when scaling from zero.
	// See: https://cluster-api.sigs.k8s.io/tasks/automated-machine-management/autoscaling
	// +optional
	Capacity *MachineCapacity `json:"capacity,omitempty"`
}

// MachineCapacity defines the capacity information for scale-from-zero
type MachineCapacity struct {
	// CPU defines the CPU capacity (e.g., "4", "16000m")
	// +optional
	CPU string `json:"cpu,omitempty"`

	// Memory defines the memory capacity (e.g., "16Gi", "128G")
	// +optional
	Memory string `json:"memory,omitempty"`

	// EphemeralDisk defines the ephemeral disk capacity (e.g., "100Gi")
	// +optional
	EphemeralDisk string `json:"ephemeralDisk,omitempty"`

	// MaxPods defines the maximum number of pods (e.g., "110")
	// +optional
	MaxPods string `json:"maxPods,omitempty"`

	// GPUType defines the GPU type for GPU nodes (e.g., "nvidia.com/gpu")
	// +optional
	GPUType string `json:"gpuType,omitempty"`

	// GPUCount defines the number of GPUs (e.g., "2")
	// +optional
	GPUCount string `json:"gpuCount,omitempty"`

	// Labels defines the node labels to be applied to scaled nodes (comma-separated)
	// Format: key1=value1,key2=value2
	// +optional
	Labels string `json:"labels,omitempty"`

	// Taints defines the node taints to be applied to scaled nodes (comma-separated)
	// Format: key1=value1:NoSchedule,key2=value2:NoExecute
	// +optional
	Taints string `json:"taints,omitempty"`

	// CSIDrivers defines the CSI driver information (comma-separated)
	// Format: driver-name=volume-limit
	// +optional
	CSIDrivers string `json:"csiDrivers,omitempty"`
}

// ByoMachineTemplateStatus defines the observed state of ByoMachineTemplate
type ByoMachineTemplateStatus struct {
	// Capacity defines the resource capacity for this machine template.
	// This value is used for autoscaling from zero operations as defined in:
	// https://github.com/kubernetes-sigs/cluster-api/blob/main/docs/proposals/20210310-opt-in-autoscaling-from-zero.md
	// +optional
	Capacity corev1.ResourceList `json:"capacity,omitempty"`

	// NodeInfo contains information about the node's architecture and OS.
	// +optional
	NodeInfo *NodeInfo `json:"nodeInfo,omitempty"`
}

// NodeInfo contains information about the node's architecture and operating system.
// +kubebuilder:validation:MinProperties=1
type NodeInfo struct {
	// Architecture is the CPU architecture of the node (e.g., amd64, arm64).
	// +optional
	Architecture string `json:"architecture,omitempty"`
	// OperatingSystem is the operating system of the node (e.g., linux, windows).
	// +optional
	OperatingSystem string `json:"operatingSystem,omitempty"`
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status

// ByoMachineTemplate is the Schema for the byomachinetemplates API
type ByoMachineTemplate struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ByoMachineTemplateSpec   `json:"spec,omitempty"`
	Status ByoMachineTemplateStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// ByoMachineTemplateList contains a list of ByoMachineTemplate
type ByoMachineTemplateList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ByoMachineTemplate `json:"items"`
}

// ByoMachineTemplateResource defines the desired state of ByoMachineTemplateResource
type ByoMachineTemplateResource struct {
	// Spec is the specification of the desired behavior of the machine.
	Spec ByoMachineSpec `json:"spec"`
}

func init() {
	SchemeBuilder.Register(&ByoMachineTemplate{}, &ByoMachineTemplateList{})
}
