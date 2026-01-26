// Copyright 2021 VMware, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"context"
	"fmt"
	"os"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/cluster-api/util/patch"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	infrastructurev1beta1 "github.com/mensylisir/cluster-api-provider-bringyourownhost/apis/infrastructure/v1beta1"
)

const (
	// hostCleanupTimeoutEnvVar is the environment variable to override the default cleanup timeout
	hostCleanupTimeoutEnvVar = "BYOH_HOST_CLEANUP_TIMEOUT"
	// defaultHostCleanupTimeout is the default maximum time to wait for Agent to complete cleanup
	defaultHostCleanupTimeout = 5 * time.Minute
	// minHostCleanupTimeout is the minimum timeout value
	minHostCleanupTimeout = 2 * time.Minute
	// maxHostCleanupTimeout is the maximum timeout value
	maxHostCleanupTimeout = 15 * time.Minute

	// forceCleanupAuditAnnotation is the annotation to track force cleanup operations
	forceCleanupAuditAnnotation = "byoh.infrastructure.cluster.x-k8s.io/force-cleanup-audit"
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

		// Get dynamic timeout based on host capacity
		cleanupTimeout := r.getCleanupTimeout(byoHost)

		// Check if we should force cleanup (Agent is unavailable)
		shouldForceCleanup := false
		if !byoHost.DeletionTimestamp.IsZero() {
			// If ByoHost is being deleted and has been for longer than cleanup timeout,
			// force cleanup (Agent is likely unavailable)
			deletionDuration := time.Since(byoHost.DeletionTimestamp.Time)
			if deletionDuration > cleanupTimeout {
				logger.Info("ByoHost deletion timeout exceeded, forcing cleanup",
					"timeout", cleanupTimeout, "elapsed", deletionDuration)
				shouldForceCleanup = true
			}
		}

		// If MachineRef is already cleared or we're forcing cleanup, just remove the annotation
		if byoHost.Status.MachineRef == nil || shouldForceCleanup {
			logger.Info("Releasing host (Agent unavailable or cleanup already complete)",
				"forceCleanup", shouldForceCleanup)

			// Clear MachineRef if not already cleared
			byoHost.Status.MachineRef = nil

			// Record force cleanup in audit log
			if shouldForceCleanup {
				// Add audit annotation to track force cleanup
				if byoHost.Annotations == nil {
					byoHost.Annotations = make(map[string]string)
				}
				auditEntry := fmt.Sprintf("timestamp=%s,reason=agent_unavailable,timeout=%v,controller=byohost-controller",
					time.Now().Format(time.RFC3339), cleanupTimeout)
				byoHost.Annotations[forceCleanupAuditAnnotation] = auditEntry
				logger.Info("Force cleanup recorded in audit log", "audit", auditEntry)
			}

			// Remove Annotation
			delete(byoHost.Annotations, infrastructurev1beta1.HostCleanupAnnotation)

			logger.Info("Host released successfully")
			return ctrl.Result{}, nil
		}

		// MachineRef exists and we're within timeout - check if Agent is still processing
		// The Agent will handle cleanup and clear MachineRef
		// If the annotation persists beyond cleanup timeout, the next reconcile will force cleanup
		logger.Info("Waiting for Agent to complete cleanup",
			"machineRef", byoHost.Status.MachineRef.Name,
			"timeout", cleanupTimeout)
	}

	return ctrl.Result{}, nil
}

// getCleanupTimeout calculates the timeout for host cleanup based on host capacity and configuration
// This allows for dynamic adjustment of timeout based on host size and environment conditions
func (r *ByoHostReconciler) getCleanupTimeout(byoHost *infrastructurev1beta1.ByoHost) time.Duration {
	// First, try to get the timeout from environment variable
	if timeoutStr := os.Getenv(hostCleanupTimeoutEnvVar); timeoutStr != "" {
		if timeout, err := time.ParseDuration(timeoutStr); err == nil {
			// Validate the timeout is within acceptable bounds
			if timeout >= minHostCleanupTimeout && timeout <= maxHostCleanupTimeout {
				return timeout
			}
			fmt.Fprintf(os.Stderr, "Warning: BYOH_HOST_CLEANUP_TIMEOUT value %s is out of bounds [%v, %v], using default\n",
				timeoutStr, minHostCleanupTimeout, maxHostCleanupTimeout)
		} else {
			fmt.Fprintf(os.Stderr, "Warning: Invalid BYOH_HOST_CLEANUP_TIMEOUT value %s: %v\n", timeoutStr, err)
		}
	}

	// Calculate dynamic timeout based on host capacity
	// Larger hosts (more CPU/memory) typically need more time to clean up
	timeout := defaultHostCleanupTimeout

	if byoHost.Spec.Capacity != nil {
		// Get CPU and memory capacity
		if cpu, exists := byoHost.Spec.Capacity[corev1.ResourceCPU]; exists {
			// For hosts with more than 8 CPUs, add 30 seconds per additional CPU
			eightCpus := resource.MustParse("8")
			if cpu.Cmp(eightCpus) > 0 {
				extraCPUs := cpu.Value() - 8
				timeout += time.Duration(extraCPUs) * 30 * time.Second
			}
		}

		if memory, exists := byoHost.Spec.Capacity[corev1.ResourceMemory]; exists {
			// For hosts with more than 16GB RAM, add 1 minute per additional 8GB
			sixteenGB := resource.MustParse("16Gi")
			if memory.Cmp(sixteenGB) > 0 {
				extraMemoryGB := (memory.Value() - 16*1024*1024*1024) / (8 * 1024 * 1024 * 1024)
				timeout += time.Duration(extraMemoryGB) * time.Minute
			}
		}
	}

	// Apply bounds checking
	if timeout < minHostCleanupTimeout {
		timeout = minHostCleanupTimeout
	}
	if timeout > maxHostCleanupTimeout {
		timeout = maxHostCleanupTimeout
	}

	return timeout
}

// SetupWithManager sets up the controller with the Manager.
func (r *ByoHostReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&infrastructurev1beta1.ByoHost{}).
		Complete(r)
}
