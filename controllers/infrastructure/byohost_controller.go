// Copyright 2021 VMware, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"context"
	"fmt"
	"os"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/cluster-api/util/conditions"
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
	// cleanupStartedAtAnnotation is the timestamp when cleanup annotation was first detected
	cleanupStartedAtAnnotation = "byoh.infrastructure.cluster.x-k8s.io/cleanup-started-at"
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
		cleanupStarted := time.Now()

		if !byoHost.DeletionTimestamp.IsZero() {
			// ByoHost is being deleted
			deletionDuration := time.Since(byoHost.DeletionTimestamp.Time)
			if deletionDuration > cleanupTimeout {
				logger.Info("ByoHost deletion timeout exceeded, forcing cleanup",
					"timeout", cleanupTimeout, "elapsed", deletionDuration)
				shouldForceCleanup = true
			}
		} else if startedAtStr, ok := byoHost.Annotations[cleanupStartedAtAnnotation]; ok {
			// Cleanup annotation was set previously, check if timeout exceeded
			if startedAt, err := time.Parse(time.RFC3339, startedAtStr); err == nil {
				elapsed := time.Since(startedAt)
				if elapsed > cleanupTimeout {
					logger.Info("Cleanup annotation timeout exceeded, forcing cleanup",
						"timeout", cleanupTimeout, "elapsed", elapsed)
					shouldForceCleanup = true
				} else {
					cleanupStarted = startedAt
				}
			}
		} else {
			// First time seeing cleanup annotation, record the start time
			if byoHost.Annotations == nil {
				byoHost.Annotations = make(map[string]string)
			}
			byoHost.Annotations[cleanupStartedAtAnnotation] = time.Now().Format(time.RFC3339)
			logger.Info("Recording cleanup start time", "timeout", cleanupTimeout)
		}

		if shouldForceCleanup {
			logger.Info("Force cleanup: Agent unavailable or timeout exceeded",
				"forceCleanup", shouldForceCleanup)

			node := &corev1.Node{}
			if err := r.Client.Get(ctx, client.ObjectKey{Name: byoHost.Name}, node); err == nil {
				logger.Info("Deleting Node object directly",
					"node", byoHost.Name)
				if err := r.Client.Delete(ctx, node); err != nil && !apierrors.IsNotFound(err) {
					logger.Error(err, "failed to delete Node object during force cleanup",
						"node", byoHost.Name)
					return ctrl.Result{}, err
				}
				logger.Info("Successfully deleted Node object during force cleanup",
					"node", byoHost.Name)
			}

			// Clear MachineRef
			byoHost.Status.MachineRef = nil

			// Record force cleanup in audit log
			auditEntry := fmt.Sprintf("timestamp=%s,reason=agent_unavailable,timeout=%v,elapsed=%v,controller=byohost-controller",
				time.Now().Format(time.RFC3339), cleanupTimeout, time.Since(cleanupStarted))
			byoHost.Annotations[forceCleanupAuditAnnotation] = auditEntry
			logger.Info("Force cleanup recorded in audit log", "audit", auditEntry)

			conditions.MarkTrue(byoHost, infrastructurev1beta1.K8sNodeBootstrapSucceeded)

			// Remove cleanup-related annotations
			delete(byoHost.Annotations, infrastructurev1beta1.HostCleanupAnnotation)
			delete(byoHost.Annotations, cleanupStartedAtAnnotation)
			delete(byoHost.Annotations, forceCleanupAuditAnnotation)

			logger.Info("Host released successfully")
			return ctrl.Result{}, nil
		}

		// Cleanup annotation exists but within timeout - wait for Agent to process
		logger.Info("Waiting for Agent to complete cleanup",
			"timeout", cleanupTimeout,
			"elapsed", time.Since(cleanupStarted))
		return ctrl.Result{RequeueAfter: cleanupTimeout}, nil
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
