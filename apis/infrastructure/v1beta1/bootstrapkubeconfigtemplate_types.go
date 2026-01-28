// Copyright 2022 VMware, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package v1beta1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// BootstrapKubeconfigTemplateSpec defines the desired state of BootstrapKubeconfigTemplate
type BootstrapKubeconfigTemplateSpec struct {
	// Template is the specification of the desired behavior of the BootstrapKubeconfig.
	Template BootstrapKubeconfigTemplateResource `json:"template"`
}

// BootstrapKubeconfigTemplateResource defines the desired state of BootstrapKubeconfigTemplateResource
type BootstrapKubeconfigTemplateResource struct {
	// Spec is the specification of the desired behavior of the BootstrapKubeconfig.
	Spec BootstrapKubeconfigSpec `json:"spec"`

	// +optional
	Labels map[string]string `json:"labels,omitempty"`

	// +optional
	Annotations map[string]string `json:"annotations,omitempty"`
}

// BootstrapKubeconfigTemplateStatus defines the observed state of BootstrapKubeconfigTemplate
type BootstrapKubeconfigTemplateStatus struct {
	// Capacity defines the resource capacity for this machine template.
	// +optional
	Capacity corev1.ResourceList `json:"capacity,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:path=bootstrapkubeconfigtemplates,scope=Namespaced,shortName=bkt
// +kubebuilder:storageversion
// BootstrapKubeconfigTemplate is the Schema for the bootstrapkubeconfigtemplates API
type BootstrapKubeconfigTemplate struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   BootstrapKubeconfigTemplateSpec   `json:"spec,omitempty"`
	Status BootstrapKubeconfigTemplateStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// BootstrapKubeconfigTemplateList contains a list of BootstrapKubeconfigTemplate
type BootstrapKubeconfigTemplateList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []BootstrapKubeconfigTemplate `json:"items"`
}

func init() {
	SchemeBuilder.Register(&BootstrapKubeconfigTemplate{}, &BootstrapKubeconfigTemplateList{})
}
