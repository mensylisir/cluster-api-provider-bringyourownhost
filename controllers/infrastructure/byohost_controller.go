// Copyright 2021 VMware, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"context"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/cluster-api/util/patch"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	infrastructurev1beta1 "github.com/vmware-tanzu/cluster-api-provider-bringyourownhost/apis/infrastructure/v1beta1"
)

const (
	// hostCleanupTimeout is the maximum time to wait for Agent to complete cleanup
	// If the Agent is unavailable (crashed, host down), we force cleanup after this timeout
	hostCleanupTimeout = 5 * time.Minute
)

// ByoHostReconciler reconciles a ByoHost object
type ByoHostReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

//+kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=byohosts,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=byohosts/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=byohosts/finalizers,verbs=update
//+kubebuilder:rbac:groups=certificates.k8s.io,resources=certificatesigningrequests,verbs=create;get;watch

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.8.3/pkg/reconcile
func (r *ByoHostReconciler) Reconcile(ctx context.Context, req ctrl.Request) (_ ctrl.Result, reterr error) {
	logger := log.FromContext(ctx)

	// Fetch the ByoHost instance
	byoHost := &infrastructurev1beta1.ByoHost{}
	if err := r.Client.Get(ctx, req.NamespacedName, byoHost); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// Initialize the patch helper
	helper, err := patch.NewHelper(byoHost, r.Client)
	if err != nil {
		return ctrl.Result{}, err
	}

	defer func() {
		if err := helper.Patch(ctx, byoHost); err != nil && reterr == nil {
			logger.Error(err, "failed to patch byohost")
			reterr = err
		}
	}()

	// Handle Host Cleanup
	if _, ok := byoHost.Annotations[infrastructurev1beta1.HostCleanupAnnotation]; ok {
		logger.Info("Host cleanup annotation detected", "host", byoHost.Name)

		// Check if we should force cleanup (Agent is unavailable)
		shouldForceCleanup := false
		if !byoHost.DeletionTimestamp.IsZero() {
			// If ByoHost is being deleted and has been for longer than cleanup timeout,
			// force cleanup (Agent is likely unavailable)
			deletionDuration := time.Since(byoHost.DeletionTimestamp.Time)
			if deletionDuration > hostCleanupTimeout {
				logger.Info("ByoHost deletion timeout exceeded, forcing cleanup",
					"timeout", hostCleanupTimeout, "elapsed", deletionDuration)
				shouldForceCleanup = true
			}
		}

		// If MachineRef is already cleared or we're forcing cleanup, just remove the annotation
		if byoHost.Status.MachineRef == nil || shouldForceCleanup {
			logger.Info("Releasing host (Agent unavailable or cleanup already complete)",
				"forceCleanup", shouldForceCleanup)

			// Clear MachineRef if not already cleared
			byoHost.Status.MachineRef = nil

			// Remove Annotation
			delete(byoHost.Annotations, infrastructurev1beta1.HostCleanupAnnotation)

			logger.Info("Host released successfully")
			return ctrl.Result{}, nil
		}

		// MachineRef exists and we're within timeout - check if Agent is still processing
		// The Agent will handle cleanup and clear MachineRef
		// If the annotation persists beyond cleanup timeout, the next reconcile will force cleanup
		logger.Info("Waiting for Agent to complete cleanup",
			"machineRef", byoHost.Status.MachineRef.Name)
	}

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *ByoHostReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&infrastructurev1beta1.ByoHost{}).
		Complete(r)
}
