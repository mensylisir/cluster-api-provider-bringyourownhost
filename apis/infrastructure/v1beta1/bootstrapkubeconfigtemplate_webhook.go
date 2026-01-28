// Copyright 2022 VMware, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package v1beta1

import (
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
)

// log is for logging in this package.
var bootstrapkubeconfigtemplatelog = logf.Log.WithName("bootstrapkubeconfigtemplate-resource")

func (r *BootstrapKubeconfigTemplate) SetupWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr).
		For(r).
		Complete()
}

// +kubebuilder:webhook:path=/mutate-infrastructure-cluster-x-k8s-io-v1beta1-bootstrapkubeconfigtemplate,mutating=true,failurePolicy=fail,sideEffects=None,groups=infrastructure.cluster.x-k8s.io,resources=bootstrapkubeconfigtemplates,verbs=create;update,versions=v1beta1,name=mbootstrapkubeconfigtemplate.kb.io,admissionReviewVersions=v1

var _ webhook.Defaulter = &BootstrapKubeconfigTemplate{}

// Default implements webhook.Defaulter so a webhook will be registered for the type
func (r *BootstrapKubeconfigTemplate) Default() {
	bootstrapkubeconfigtemplatelog.Info("defaulting bootstrapkubeconfigtemplate", "name", r.Name)
	// Copy labels and annotations from template to the BootstrapKubeconfig created from this template
	if r.Spec.Template.Labels == nil {
		r.Spec.Template.Labels = make(map[string]string)
	}
	if r.Spec.Template.Annotations == nil {
		r.Spec.Template.Annotations = make(map[string]string)
	}
}

func (r *BootstrapKubeconfigTemplate) SetupWebhookWithManagerForTemplate(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr).
		For(&BootstrapKubeconfigTemplate{}).
		Complete()
}

var _ webhook.Validator = &BootstrapKubeconfigTemplate{}

// ValidateCreate implements webhook.Validator so a webhook will be registered for the type
func (r *BootstrapKubeconfigTemplate) ValidateCreate() error {
	bootstrapkubeconfigtemplatelog.Info("validate create", "name", r.Name)
	return nil
}

// ValidateUpdate implements webhook.Validator so a webhook will be registered for the type
func (r *BootstrapKubeconfigTemplate) ValidateUpdate(old runtime.Object) error {
	bootstrapkubeconfigtemplatelog.Info("validate update", "name", r.Name)
	return nil
}

// ValidateDelete implements webhook.Validator so a webhook will be registered for the type
func (r *BootstrapKubeconfigTemplate) ValidateDelete() error {
	bootstrapkubeconfigtemplatelog.Info("validate delete", "name", r.Name)
	return nil
}
