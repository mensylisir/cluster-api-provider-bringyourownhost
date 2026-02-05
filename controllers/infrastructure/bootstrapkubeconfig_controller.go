// Copyright 2022 VMware, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"context"
	b64 "encoding/base64"
	"fmt"
	"time"

	infrastructurev1beta1 "github.com/mensylisir/cluster-api-provider-bringyourownhost/apis/infrastructure/v1beta1"
	"github.com/mensylisir/cluster-api-provider-bringyourownhost/common/bootstraptoken"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientcmdlatest "k8s.io/client-go/tools/clientcmd/api/latest"
	bootstraputil "k8s.io/cluster-bootstrap/token/util"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/cluster-api/util/patch"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// BootstrapKubeconfigReconciler reconciles a BootstrapKubeconfig object
type BootstrapKubeconfigReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

const (
	// ttl is the time to live for the generated bootstrap token
	ttl = time.Minute * 30
)

//+kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=bootstrapkubeconfigs,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=bootstrapkubeconfigs/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=bootstrapkubeconfigs/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
func (r *BootstrapKubeconfigReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	logger.Info("Reconcile request received", "name", req.Name)

	// Fetch the BootstrapKubeconfig instance
	bootstrapKubeconfig := &infrastructurev1beta1.BootstrapKubeconfig{}
	err := r.Client.Get(ctx, req.NamespacedName, bootstrapKubeconfig)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// Always populate APIServer and CertificateAuthorityData if empty
	// This handles the case where MachineSet clones the BootstrapKubeconfig
	if bootstrapKubeconfig.Spec.APIServer == "" || bootstrapKubeconfig.Spec.CertificateAuthorityData == "" {
		if err := r.populateFromOriginal(ctx, bootstrapKubeconfig); err != nil {
			// If no Machine owner found and no BootstrapKubeconfigData exists yet,
			// we cannot proceed - return and wait for owner to be set
			if bootstrapKubeconfig.Status.BootstrapKubeconfigData == nil {
				logger.Info("BootstrapKubeconfig has no Machine owner and no data yet, skipping", "name", req.Name)
				return ctrl.Result{}, nil
			}
		}
	}

	// There already is bootstrap-kubeconfig data associated with this object
	// Do not create secrets again, but ensure DataSecretName and DataSecretCreated are set for CAPI compatibility
	if bootstrapKubeconfig.Status.BootstrapKubeconfigData != nil {
		// Even if BootstrapKubeconfigData exists, still populate APIServer from original
		if err := r.populateFromOriginal(ctx, bootstrapKubeconfig); err != nil {
			logger.V(4).Info("Failed to populate from original, using existing data", "name", req.Name)
		}
		// Do NOT clear BootstrapKubeconfigData - it will be regenerated when APIServer is updated
	}

	tokenStr, err := bootstraputil.GenerateBootstrapToken()
	if err != nil {
		return ctrl.Result{}, err
	}

	bootstrapKubeconfigSecret, err := bootstraptoken.GenerateSecretFromBootstrapToken(tokenStr, ttl)
	if err != nil {
		return ctrl.Result{}, err
	}

	// create secret
	err = r.Client.Create(ctx, bootstrapKubeconfigSecret)
	if err != nil {
		return ctrl.Result{}, err
	}

	bootstrapKubeconfigData, err := bootstraptoken.GenerateBootstrapKubeconfigFromBootstrapToken(tokenStr, bootstrapKubeconfig)
	if err != nil {
		return ctrl.Result{}, err
	}
	bootstrapKubeconfigData.Clusters[infrastructurev1beta1.DefaultClusterName].Server = bootstrapKubeconfig.Spec.APIServer

	caData := bootstrapKubeconfigData.Clusters[infrastructurev1beta1.DefaultClusterName].CertificateAuthorityData
	decodedCAData, err := b64.StdEncoding.DecodeString(string(caData))
	if err != nil {
		return ctrl.Result{}, err
	}

	bootstrapKubeconfigData.Clusters[infrastructurev1beta1.DefaultClusterName].CertificateAuthorityData = decodedCAData
	runtimeEncodedBootstrapKubeConfig, err := runtime.Encode(clientcmdlatest.Codec, bootstrapKubeconfigData)
	if err != nil {
		return ctrl.Result{}, err
	}

	helper, err := patch.NewHelper(bootstrapKubeconfig, r.Client)
	if err != nil {
		return ctrl.Result{}, err
	}

	bootstrapKubeconfigDataStr := string(runtimeEncodedBootstrapKubeConfig)
	bootstrapKubeconfig.Status.BootstrapKubeconfigData = &bootstrapKubeconfigDataStr

	// Set DataSecretName for CAPI Machine controller compatibility
	bootstrapKubeconfig.Status.DataSecretName = bootstrapKubeconfigSecret.Name

	// Set DataSecretCreated to true for CAPI Machine controller compatibility
	trueVal := true
	bootstrapKubeconfig.Status.Initialization.DataSecretCreated = &trueVal

	return ctrl.Result{}, helper.Patch(ctx, bootstrapKubeconfig)
}

// populateFromOriginal populates APIServer and CertificateAuthorityData from the original BootstrapKubeconfig
func (r *BootstrapKubeconfigReconciler) populateFromOriginal(ctx context.Context, bk *infrastructurev1beta1.BootstrapKubeconfig) error {
	// Find the Machine owner
	var machineName string
	var machineNamespace string
	for _, ref := range bk.GetOwnerReferences() {
		if ref.Kind == "Machine" {
			machineName = ref.Name
			machineNamespace = bk.GetNamespace()
			break
		}
	}

	if machineName == "" {
		return fmt.Errorf("no Machine owner found")
	}

	// Look up the Machine
	machine := &clusterv1.Machine{}
	if err := r.Client.Get(ctx, types.NamespacedName{
		Name:      machineName,
		Namespace: machineNamespace,
	}, machine); err != nil {
		return fmt.Errorf("failed to get Machine %s: %w", machineName, err)
	}

	// Get the Cluster to find the control plane endpoint
	if machine.Spec.ClusterName == "" {
		return fmt.Errorf("Machine %s has no cluster name", machineName)
	}

	cluster := &clusterv1.Cluster{}
	if err := r.Client.Get(ctx, types.NamespacedName{
		Name:      machine.Spec.ClusterName,
		Namespace: machineNamespace,
	}, cluster); err != nil {
		return fmt.Errorf("failed to get Cluster %s: %w", machine.Spec.ClusterName, err)
	}

	// Patch the BootstrapKubeconfig with values from the Cluster
	helper, err := patch.NewHelper(bk, r.Client)
	if err != nil {
		return fmt.Errorf("failed to create patch helper: %w", err)
	}

	// Use the control plane endpoint from the Cluster
	if bk.Spec.APIServer == "" && cluster.Spec.ControlPlaneEndpoint.IsValid() {
		bk.Spec.APIServer = cluster.Spec.ControlPlaneEndpoint.String()
		log.FromContext(ctx).Info("populated APIServer from Cluster controlPlaneEndpoint", "endpoint", bk.Spec.APIServer)
	}

	// Try to get CA data and APIServer from the original BootstrapKubeconfig
	if machine.Spec.Bootstrap.ConfigRef != nil && machine.Spec.Bootstrap.ConfigRef.Name != bk.Name {
		originalBK := &infrastructurev1beta1.BootstrapKubeconfig{}
		if err := r.Client.Get(ctx, types.NamespacedName{
			Name:      machine.Spec.Bootstrap.ConfigRef.Name,
			Namespace: machineNamespace,
		}, originalBK); err == nil {
			if bk.Spec.CertificateAuthorityData == "" && originalBK.Spec.CertificateAuthorityData != "" {
				bk.Spec.CertificateAuthorityData = originalBK.Spec.CertificateAuthorityData
				log.FromContext(ctx).Info("populated CertificateAuthorityData from original BootstrapKubeconfig", "original", originalBK.Name)
			}
			if bk.Spec.APIServer == "" && originalBK.Spec.APIServer != "" {
				bk.Spec.APIServer = originalBK.Spec.APIServer
				log.FromContext(ctx).Info("populated APIServer from original BootstrapKubeconfig", "original", originalBK.Name)
			}
		}
	}

	return helper.Patch(ctx, bk)
}

// SetupWithManager sets up the controller with the Manager.
func (r *BootstrapKubeconfigReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&infrastructurev1beta1.BootstrapKubeconfig{}).
		Complete(r)
}
