// Copyright 2022 VMware, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package v1beta1

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	b64 "encoding/base64"
	"encoding/pem"
	"net/url"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation/field"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
)

// log is for logging in this package.
var bootstrapkubeconfiglog = ctrl.Log.WithName("bootstrapkubeconfig-resource")

// APIServerURLScheme is the url scheme for the APIServer
const APIServerURLScheme = "https"

func (r *BootstrapKubeconfig) SetupWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr).
		For(r).
		Complete()
}

var _ webhook.Defaulter = &BootstrapKubeconfig{}

func (r *BootstrapKubeconfig) Default() {
	bootstrapkubeconfiglog.Info("defaulting bootstrapkubeconfig", "name", r.Name)
}

// +k8s:deepcopy-gen=false
// BootstrapKubeconfigMutatingWebhook handles admission requests
type BootstrapKubeconfigMutatingWebhook struct {
	Client  client.Client
	decoder *admission.Decoder
}

// InjectDecoder injects the decoder.
func (wh *BootstrapKubeconfigMutatingWebhook) InjectDecoder(d *admission.Decoder) error {
	wh.decoder = d
	return nil
}

func (wh *BootstrapKubeconfigMutatingWebhook) Handle(ctx context.Context, req admission.Request) admission.Response {
	bootstrapkubeconfiglog.Info("mutating webhook called", "name", req.Name)

	obj := &BootstrapKubeconfig{}
	if err := wh.decoder.Decode(req, obj); err != nil {
		return admission.Errored(http.StatusBadRequest, err)
	}

	// Only process if APIServer or CertificateAuthorityData is empty (cloned by MachineSet)
	if obj.Spec.APIServer == "" || obj.Spec.CertificateAuthorityData == "" {
		if err := wh.populateFromCluster(ctx, obj); err != nil {
			bootstrapkubeconfiglog.Error(err, "failed to populate from cluster", "name", obj.Name)
			// Don't fail - let the validating webhook handle the error
		}
	}

	marshaled, err := json.Marshal(obj)
	if err != nil {
		return admission.Errored(http.StatusInternalServerError, err)
	}

	return admission.PatchResponseFromRaw(req.Object.Raw, marshaled)
}

func (wh *BootstrapKubeconfigMutatingWebhook) populateFromCluster(ctx context.Context, obj *BootstrapKubeconfig) error {
	// Find the Cluster owner in the ownerReferences
	var clusterName types.NamespacedName
	var machineName string
	for _, ref := range obj.GetOwnerReferences() {
		if ref.Kind == "Machine" {
			machineName = ref.Name
			// Look up the Machine to find its Cluster owner
			machine := &clusterv1.Machine{}
			if err := wh.Client.Get(ctx, types.NamespacedName{
				Name:      ref.Name,
				Namespace: obj.GetNamespace(),
			}, machine); err != nil {
				continue
			}

			// Check if the Machine has a Cluster owner
			for _, machineRef := range machine.GetOwnerReferences() {
				if machineRef.Kind == "Cluster" {
					clusterName = types.NamespacedName{
						Name:      machineRef.Name,
						Namespace: obj.GetNamespace(),
					}
					break
				}
			}
			break
		} else if ref.Kind == "Cluster" {
			clusterName = types.NamespacedName{
				Name:      ref.Name,
				Namespace: obj.GetNamespace(),
			}
			break
		}
	}

	if clusterName.Name == "" {
		return fmt.Errorf("failed to find Cluster owner reference")
	}

	// Look up the Cluster
	cluster := &clusterv1.Cluster{}
	if err := wh.Client.Get(ctx, clusterName, cluster); err != nil {
		return fmt.Errorf("failed to get Cluster %s: %w", clusterName, err)
	}

	// Get the ByoCluster from the Cluster's infrastructure ref
	if cluster.Spec.InfrastructureRef == nil {
		return fmt.Errorf("Cluster %s does not have an infrastructure ref", clusterName)
	}

	// Look up the ByoCluster
	byoCluster := &ByoCluster{}
	if err := wh.Client.Get(ctx, types.NamespacedName{
		Name:      cluster.Spec.InfrastructureRef.Name,
		Namespace: cluster.Spec.InfrastructureRef.Namespace,
	}, byoCluster); err != nil {
		return fmt.Errorf("failed to get ByoCluster %s: %w", cluster.Spec.InfrastructureRef.Name, err)
	}

	// Populate the APIServer from the controlPlaneEndpoint
	if obj.Spec.APIServer == "" && byoCluster.Spec.ControlPlaneEndpoint.Host != "" && byoCluster.Spec.ControlPlaneEndpoint.Port != 0 {
		obj.Spec.APIServer = fmt.Sprintf("https://%s:%d", byoCluster.Spec.ControlPlaneEndpoint.Host, byoCluster.Spec.ControlPlaneEndpoint.Port)
		bootstrapkubeconfiglog.Info("populated APIServer from cluster", "apiserver", obj.Spec.APIServer)
	}

	// Populate CertificateAuthorityData from the original BootstrapKubeconfig
	if obj.Spec.CertificateAuthorityData == "" && machineName != "" {
		machine := &clusterv1.Machine{}
		if err := wh.Client.Get(ctx, types.NamespacedName{
			Name:      machineName,
			Namespace: obj.GetNamespace(),
		}, machine); err == nil {
			// Get the original BootstrapKubeconfig from Machine's bootstrap config ref
			if machine.Spec.Bootstrap.ConfigRef != nil {
				originalBK := &BootstrapKubeconfig{}
				if err := wh.Client.Get(ctx, types.NamespacedName{
					Name:      machine.Spec.Bootstrap.ConfigRef.Name,
					Namespace: obj.GetNamespace(),
				}, originalBK); err == nil {
					if originalBK.Spec.CertificateAuthorityData != "" {
						obj.Spec.CertificateAuthorityData = originalBK.Spec.CertificateAuthorityData
						bootstrapkubeconfiglog.Info("populated CertificateAuthorityData from original BootstrapKubeconfig", "name", originalBK.Name)
					}
				}
			}
		}
	}

	return nil
}

// SetupMutatingWebhookWithManager sets up the mutating webhook with the manager
func SetupMutatingWebhookWithManager(mgr ctrl.Manager) error {
	mgr.GetWebhookServer().Register(
		"/mutate-infrastructure-cluster-x-k8s-io-v1beta1-bootstrapkubeconfig",
		&webhook.Admission{Handler: &BootstrapKubeconfigMutatingWebhook{Client: mgr.GetClient()}},
	)
	return nil
}

//+kubebuilder:webhook:path=/validate-infrastructure-cluster-x-k8s-io-v1beta1-bootstrapkubeconfig,mutating=false,failurePolicy=fail,sideEffects=None,groups=infrastructure.cluster.x-k8s.io,resources=bootstrapkubeconfigs,verbs=create;update,versions=v1beta1,name=vbootstrapkubeconfig.kb.io,admissionReviewVersions=v1

var _ webhook.Validator = &BootstrapKubeconfig{}

// ValidateCreate implements webhook.Validator so a webhook will be registered for the type
func (r *BootstrapKubeconfig) ValidateCreate() error {
	bootstrapkubeconfiglog.Info("validate create", "name", r.Name)

	// Skip APIServer validation if it's empty - mutating webhook will populate it
	if r.Spec.APIServer != "" {
		if err := r.validateAPIServer(); err != nil {
			return err
		}
	}

	if err := r.validateCAData(); err != nil {
		return err
	}

	return nil
}

// ValidateUpdate implements webhook.Validator so a webhook will be registered for the type
func (r *BootstrapKubeconfig) ValidateUpdate(old runtime.Object) error {
	bootstrapkubeconfiglog.Info("validate update", "name", r.Name)

	// Skip APIServer validation if it's empty - mutating webhook will populate it
	if r.Spec.APIServer != "" {
		if err := r.validateAPIServer(); err != nil {
			return err
		}
	}

	if err := r.validateCAData(); err != nil {
		return err
	}

	return nil
}

// ValidateDelete implements webhook.Validator so a webhook will be registered for the type
func (r *BootstrapKubeconfig) ValidateDelete() error {
	bootstrapkubeconfiglog.Info("validate delete", "name", r.Name)

	return nil
}

func (r *BootstrapKubeconfig) validateAPIServer() error {
	if r.Spec.APIServer == "" {
		return field.Invalid(field.NewPath("spec").Child("apiserver"), r.Spec.APIServer, "APIServer field cannot be empty")
	}

	parsedURL, err := url.Parse(r.Spec.APIServer)
	if err != nil {
		return field.Invalid(field.NewPath("spec").Child("apiserver"), r.Spec.APIServer, "APIServer URL is not valid")
	}
	if !r.isURLValid(parsedURL) {
		return field.Invalid(field.NewPath("spec").Child("apiserver"), r.Spec.APIServer, "APIServer is not of the format https://hostname:port")
	}
	return nil
}

func (r *BootstrapKubeconfig) validateCAData() error {
	if r.Spec.CertificateAuthorityData == "" {
		return field.Invalid(field.NewPath("spec").Child("caData"), r.Spec.CertificateAuthorityData, "CertificateAuthorityData field cannot be empty")
	}

	decodedCAData, err := b64.StdEncoding.DecodeString(r.Spec.CertificateAuthorityData)
	if err != nil {
		return field.Invalid(field.NewPath("spec").Child("caData"), r.Spec.CertificateAuthorityData, "cannot base64 decode CertificateAuthorityData")
	}

	block, _ := pem.Decode(decodedCAData)
	if block == nil {
		return field.Invalid(field.NewPath("spec").Child("caData"), r.Spec.CertificateAuthorityData, "CertificateAuthorityData is not PEM encoded")
	}

	return nil
}

func (r *BootstrapKubeconfig) isURLValid(parsedURL *url.URL) bool {
	if parsedURL.Host == "" || parsedURL.Scheme != APIServerURLScheme || parsedURL.Port() == "" {
		return false
	}
	return true
}
