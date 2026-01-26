// Copyright 2021 VMware, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/pkg/errors"
	"github.com/mensylisir/cluster-api-provider-bringyourownhost/agent/cloudinit"
	"github.com/mensylisir/cluster-api-provider-bringyourownhost/agent/registration"
	"github.com/mensylisir/cluster-api-provider-bringyourownhost/common"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/cluster-api/util/conditions"
	"sigs.k8s.io/cluster-api/util/patch"
	"sigs.k8s.io/cluster-api/util/predicates"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	"github.com/kube-vip/kube-vip/pkg/vip"
	infrastructurev1beta1 "github.com/mensylisir/cluster-api-provider-bringyourownhost/apis/infrastructure/v1beta1"
)

// HostReconciler encapsulates the data/logic needed to reconcile a ByoHost
type HostReconciler struct {
	Client              client.Client
	CmdRunner           cloudinit.ICmdRunner
	FileWriter          cloudinit.IFileWriter
	TemplateParser      cloudinit.ITemplateParser
	Recorder            record.EventRecorder
	SkipK8sInstallation bool
	DownloadPath        string
}

const (
	bootstrapSentinelFile = "/run/cluster-api/bootstrap-success.complete"
	// machineIDFile stores the UID of the Machine currently bound to this host
	machineIDFile = "/run/cluster-api/machine-id"
	// KubeadmResetCommand is the command to run to force reset/remove nodes' local file system of the files created by kubeadm
	KubeadmResetCommand = "kubeadm reset --force"
	// NOTE: Agent does NOT use finalizer because it's an external process that can crash.
	// If Agent crashes during cleanup, ByoHostController will detect the stale cleanup annotation
	// and clear MachineRef without waiting for Agent. This prevents ByoHost from being stuck
	// in deletion state when the Agent process is permanently unavailable.
)

// Reconcile handles events for the ByoHost that is registered by this agent process
func (r *HostReconciler) Reconcile(ctx context.Context, req ctrl.Request) (_ ctrl.Result, reterr error) {
	logger := ctrl.LoggerFrom(ctx)
	logger.Info("Reconcile request received")

	// Fetch the ByoHost instance
	byoHost := &infrastructurev1beta1.ByoHost{}
	err := r.Client.Get(ctx, req.NamespacedName, byoHost)
	if err != nil {
		logger.Error(err, "error getting ByoHost")
		return ctrl.Result{}, err
	}

	helper, _ := patch.NewHelper(byoHost, r.Client)
	defer func() {
		err = helper.Patch(ctx, byoHost)
		if err != nil && reterr == nil {
			logger.Error(err, "failed to patch byohost")
			reterr = err
		}
	}()

	// Check for host cleanup annotation
	hostAnnotations := byoHost.GetAnnotations()
	_, ok := hostAnnotations[infrastructurev1beta1.HostCleanupAnnotation]
	if ok {
		err = r.hostCleanUp(ctx, byoHost)
		if err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	// Handle deleted machines
	if !byoHost.ObjectMeta.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, byoHost)
	}
	return r.reconcileNormal(ctx, byoHost)
}

func (r *HostReconciler) reconcileNormal(ctx context.Context, byoHost *infrastructurev1beta1.ByoHost) (ctrl.Result, error) {
	logger := ctrl.LoggerFrom(ctx)
	logger = logger.WithValues("ByoHost", byoHost.Name)
	logger.Info("reconcile normal")
	if byoHost.Status.MachineRef == nil {
		// Check for Zombie state: MachineRef is nil (cleared by Controller force cleanup),
		// but we are still bootstrapped locally. We must self-clean to ensure consistency.
		if conditions.IsTrue(byoHost, infrastructurev1beta1.K8sNodeBootstrapSucceeded) ||
			conditions.IsTrue(byoHost, infrastructurev1beta1.K8sComponentsInstallationSucceeded) {
			logger.Info("MachineRef is nil but host appears to be bootstrapped. Detected Zombie state (Force Cleanup occurred). Triggering self-cleanup.")
			if err := r.hostCleanUp(ctx, byoHost); err != nil {
				return ctrl.Result{}, err
			}
			// Cleanup successful
			return ctrl.Result{}, nil
		}

		logger.Info("Machine ref not yet set")
		conditions.MarkFalse(byoHost, infrastructurev1beta1.K8sNodeBootstrapSucceeded, infrastructurev1beta1.WaitingForMachineRefReason, clusterv1.ConditionSeverityInfo, "")
		return ctrl.Result{}, nil
	}

	if byoHost.Spec.BootstrapSecret == nil {
		logger.Info("BootstrapDataSecret not ready")
		conditions.MarkFalse(byoHost, infrastructurev1beta1.K8sNodeBootstrapSucceeded, infrastructurev1beta1.BootstrapDataSecretUnavailableReason, clusterv1.ConditionSeverityInfo, "")
		return ctrl.Result{}, nil
	}

	// Check for Machine ID mismatch (Agent consistency check)
	// If the Agent is running on a host that was previously bound to another Machine,
	// and the Agent missed the cleanup event (e.g. while offline), we must detect this
	// and force a cleanup before proceeding.
	if byoHost.Status.MachineRef != nil {
		currentMachineIDBytes, err := os.ReadFile(machineIDFile)
		if err == nil {
			currentMachineID := strings.TrimSpace(string(currentMachineIDBytes))
			if currentMachineID != string(byoHost.Status.MachineRef.UID) {
				logger.Info("Detected Machine UID mismatch. Host is bound to a new Machine but carries old state.",
					"oldID", currentMachineID, "newID", byoHost.Status.MachineRef.UID)
				if err := r.hostCleanUp(ctx, byoHost); err != nil {
					return ctrl.Result{}, err
				}
				// Cleanup triggered, return to allow fresh reconciliation
				return ctrl.Result{}, nil
			}
		}
	}

	if !conditions.IsTrue(byoHost, infrastructurev1beta1.K8sNodeBootstrapSucceeded) {
		bootstrapScript, err := r.getBootstrapScript(ctx, byoHost.Spec.BootstrapSecret.Name, byoHost.Spec.BootstrapSecret.Namespace)
		if err != nil {
			logger.Error(err, "error getting bootstrap script")
			r.Recorder.Eventf(byoHost, corev1.EventTypeWarning, "ReadBootstrapSecretFailed", "bootstrap secret %s not found", byoHost.Spec.BootstrapSecret.Name)
			return ctrl.Result{}, err
		}

		if r.SkipK8sInstallation {
			logger.Info("Skipping installation of k8s components")
		} else if !conditions.IsTrue(byoHost, infrastructurev1beta1.K8sComponentsInstallationSucceeded) {
			if byoHost.Spec.InstallationSecret == nil {
				logger.Info("InstallationSecret not ready")
				conditions.MarkFalse(byoHost, infrastructurev1beta1.K8sComponentsInstallationSucceeded, infrastructurev1beta1.K8sInstallationSecretUnavailableReason, clusterv1.ConditionSeverityInfo, "")
				return ctrl.Result{}, nil
			}
			err = r.executeInstallerController(ctx, byoHost)
			if err != nil {
				return ctrl.Result{}, err
			}
			r.Recorder.Event(byoHost, corev1.EventTypeNormal, "InstallScriptExecutionSucceeded", "install script executed")
			conditions.MarkTrue(byoHost, infrastructurev1beta1.K8sComponentsInstallationSucceeded)
		} else {
			logger.Info("install script already executed")
		}

		err = r.cleank8sdirectories(ctx)
		if err != nil {
			logger.Error(err, "error cleaning up k8s directories, please delete it manually for reconcile to proceed.")
			r.Recorder.Event(byoHost, corev1.EventTypeWarning, "CleanK8sDirectoriesFailed", "clean k8s directories failed")
			conditions.MarkFalse(byoHost, infrastructurev1beta1.K8sNodeBootstrapSucceeded, infrastructurev1beta1.CleanK8sDirectoriesFailedReason, clusterv1.ConditionSeverityError, "")
			return ctrl.Result{}, err
		}

		err = r.bootstrapK8sNode(ctx, bootstrapScript, byoHost)
		if err != nil {
			logger.Error(err, "error in bootstrapping k8s node")
			r.Recorder.Event(byoHost, corev1.EventTypeWarning, "BootstrapK8sNodeFailed", "k8s Node Bootstrap failed")
			_ = r.resetNode(ctx, byoHost)
			conditions.MarkFalse(byoHost, infrastructurev1beta1.K8sNodeBootstrapSucceeded, infrastructurev1beta1.CloudInitExecutionFailedReason, clusterv1.ConditionSeverityError, "")
			return ctrl.Result{}, err
		}
		logger.Info("k8s node successfully bootstrapped")
		r.Recorder.Event(byoHost, corev1.EventTypeNormal, "BootstrapK8sNodeSucceeded", "k8s Node Bootstraped")
		conditions.MarkTrue(byoHost, infrastructurev1beta1.K8sNodeBootstrapSucceeded)

		// Persist Machine ID to ensure consistency across restarts/rebinds
		if byoHost.Status.MachineRef != nil {
			if err := os.WriteFile(machineIDFile, []byte(byoHost.Status.MachineRef.UID), 0644); err != nil {
				logger.Error(err, "failed to persist machine ID")
			}
		}
	}

	return ctrl.Result{}, nil
}

func (r *HostReconciler) executeInstallerController(ctx context.Context, byoHost *infrastructurev1beta1.ByoHost) error {
	logger := ctrl.LoggerFrom(ctx)
	secret := &corev1.Secret{}
	err := r.Client.Get(ctx, types.NamespacedName{Name: byoHost.Spec.InstallationSecret.Name, Namespace: byoHost.Spec.InstallationSecret.Namespace}, secret)
	if err != nil {
		logger.Error(err, "error getting install and uninstall script")
		r.Recorder.Eventf(byoHost, corev1.EventTypeWarning, "ReadInstallationSecretFailed", "install and uninstall script %s not found", byoHost.Spec.InstallationSecret.Name)
		return err
	}
	installScript := string(secret.Data["install"])
	uninstallScript := string(secret.Data["uninstall"])

	byoHost.Spec.UninstallationScript = &uninstallScript
	installScript, err = r.parseScript(ctx, installScript, byoHost.Name)
	if err != nil {
		return err
	}
	logger.Info("executing install script")
	err = r.CmdRunner.RunCmd(ctx, installScript)
	if err != nil {
		logger.Error(err, "error executing installation script")
		r.Recorder.Event(byoHost, corev1.EventTypeWarning, "InstallScriptExecutionFailed", "install script execution failed")
		conditions.MarkFalse(byoHost, infrastructurev1beta1.K8sComponentsInstallationSucceeded, infrastructurev1beta1.K8sComponentsInstallationFailedReason, clusterv1.ConditionSeverityInfo, "")
		return err
	}
	return nil
}

func (r *HostReconciler) reconcileDelete(ctx context.Context, byoHost *infrastructurev1beta1.ByoHost) (ctrl.Result, error) {
	logger := ctrl.LoggerFrom(ctx)
	logger.Info("reconcile delete - performing host cleanup")

	// Perform cleanup
	if err := r.hostCleanUp(ctx, byoHost); err != nil {
		logger.Error(err, "failed to cleanup host during delete")
		// Check if cleanup failed due to permanent error
		// If it's a permanent error (e.g., Agent was force-killed), proceed anyway
		// If it's a transient error, retry
		if isPermanentCleanupError(err) {
			logger.Info("cleanup failed permanently, but proceeding to allow ByoHost deletion")
		} else {
			return ctrl.Result{RequeueAfter: 30 * time.Second}, err
		}
	}

	logger.Info("Cleanup completed")
	return ctrl.Result{}, nil
}

// isPermanentCleanupError checks if the cleanup error is permanent
// Permanent errors include situations where the Agent cannot recover,
// such as when the host is permanently offline or the Agent process was killed
func isPermanentCleanupError(err error) bool {
	// Add logic to determine if the error is permanent
	// For example, if kubeadm reset fails because the node is already gone,
	// this is a permanent error (the node has already left the cluster)
	if err != nil {
		// Check for specific error patterns that indicate permanent failure
		errStr := err.Error()
		if strings.Contains(errStr, "connection refused") ||
			strings.Contains(errStr, "no such file or directory") ||
			strings.Contains(errStr, "kubeadm reset") && strings.Contains(errStr, "failed") {
			return true
		}
	}
	return false
}

func (r *HostReconciler) getBootstrapScript(ctx context.Context, dataSecretName, namespace string) (string, error) {
	secret := &corev1.Secret{}
	err := r.Client.Get(ctx, types.NamespacedName{Name: dataSecretName, Namespace: namespace}, secret)
	if err != nil {
		return "", err
	}

	bootstrapSecret := string(secret.Data["value"])
	return bootstrapSecret, nil
}

func (r *HostReconciler) parseScript(ctx context.Context, script string, hostname string) (string, error) {
	data, err := cloudinit.TemplateParser{
		Template: map[string]string{
			"BundleDownloadPath": r.DownloadPath,
			"Hostname":           hostname,
		},
	}.ParseTemplate(script)
	if err != nil {
		return "", fmt.Errorf("unable to apply install parsed template to the data object")
	}
	return data, nil
}

// applyScaleFromZeroAnnotations applies scale-from-zero annotations to the node
// This is called during bootstrap to set labels and taints from autoscaler capacity annotations
func (r *HostReconciler) applyScaleFromZeroAnnotations(ctx context.Context, byoHost *infrastructurev1beta1.ByoHost) error {
	logger := ctrl.LoggerFrom(ctx)

	if byoHost.Status.MachineRef == nil {
		return nil
	}

	// Get the Machine to access scale-from-zero annotations
	machine := &clusterv1.Machine{}
	if err := r.Client.Get(ctx, types.NamespacedName{
		Namespace: byoHost.Status.MachineRef.Namespace,
		Name:      byoHost.Status.MachineRef.Name,
	}, machine); err != nil {
		logger.V(4).Info("Failed to get Machine for scale-from-zero annotations", "error", err)
		return nil
	}

	// Extract scale-from-zero annotations
	annotations := machine.Annotations
	if annotations == nil {
		return nil
	}

	// Build node labels from capacity annotations
	var nodeLabels []string
	if labels, ok := annotations[infrastructurev1beta1.CapacityLabelsAnnotation]; ok && labels != "" {
		nodeLabels = append(nodeLabels, strings.Split(labels, ",")...)
	}

	// Build node taints from capacity annotations
	var nodeTaints []string
	if taints, ok := annotations[infrastructurev1beta1.CapacityTaintsAnnotation]; ok && taints != "" {
		nodeTaints = append(nodeTaints, strings.Split(taints, ",")...)
	}

	// If there are labels or taints to apply, use kubelet flags
	// Note: This is typically handled by the bootstrap data (cloud-init)
	// This function is a no-op if the annotations are not present
	if len(nodeLabels) > 0 || len(nodeTaints) > 0 {
		logger.Info("Scale-from-zero annotations detected, will be applied via bootstrap data",
			"labels", nodeLabels, "taints", nodeTaints)
	}

	return nil
}

// SetupWithManager sets up the controller with the manager
func (r *HostReconciler) SetupWithManager(ctx context.Context, mgr manager.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&infrastructurev1beta1.ByoHost{}).
		WithEventFilter(predicates.ResourceNotPaused(ctrl.LoggerFrom(ctx))).
		Complete(r)
}

// cleanup /run/kubeadm, /etc/cni/net.d dirs to remove any stale config on the host
func (r *HostReconciler) cleank8sdirectories(ctx context.Context) error {
	logger := ctrl.LoggerFrom(ctx)

	dirs := []string{
		"/run/kubeadm/*",
		"/etc/cni/net.d/*",
	}

	errList := make([]error, 0)
	for _, dir := range dirs {
		logger.Info(fmt.Sprintf("cleaning up directory %s", dir))
		if err := common.RemoveGlob(dir); err != nil {
			logger.Error(err, fmt.Sprintf("failed to clean up directory %s", dir))
			errList = append(errList, err)
		}
	}

	if len(errList) > 0 {
		err := errList[0]               //nolint: gosec
		for _, e := range errList[1:] { //nolint: gosec
			err = fmt.Errorf("%w; %v error", err, e)
		}
		return errors.WithMessage(err, "not all k8s directories are cleaned up")
	}
	return nil
}

func (r *HostReconciler) hostCleanUp(ctx context.Context, byoHost *infrastructurev1beta1.ByoHost) error {
	logger := ctrl.LoggerFrom(ctx)
	logger.Info("cleaning up host")

	k8sComponentsInstallationSucceeded := conditions.Get(byoHost, infrastructurev1beta1.K8sComponentsInstallationSucceeded)
	if k8sComponentsInstallationSucceeded != nil && k8sComponentsInstallationSucceeded.Status == corev1.ConditionTrue {
		// Reset the node (kubeadm reset) with retry
		logger.Info("resetting node with retry")
		if err := r.resetNodeWithRetry(ctx, byoHost); err != nil {
			logger.Error(err, "failed to reset node after multiple attempts, continuing cleanup")
		}
		if r.SkipK8sInstallation {
			logger.Info("Skipping uninstallation of k8s components")
		} else {
			if byoHost.Spec.UninstallationScript == nil {
				// UninstallScript may be nil for first-time installs or skip-installation mode
				logger.Info("UninstallationScript not found, skipping uninstall step")
			} else {
				logger.Info("Executing Uninstall script")
				uninstallScript := *byoHost.Spec.UninstallationScript
				var err error
				uninstallScript, err = r.parseScript(ctx, uninstallScript, byoHost.Name)
				if err != nil {
					logger.Error(err, "error parsing Uninstallation script")
					return err
				}
				err = r.CmdRunner.RunCmd(ctx, uninstallScript)
				if err != nil {
					logger.Error(err, "error executing Uninstallation script")
					r.Recorder.Event(byoHost, corev1.EventTypeWarning, "UninstallScriptExecutionFailed", "uninstall script execution failed")
					return err
				}
			}
		}
		conditions.MarkFalse(byoHost, infrastructurev1beta1.K8sComponentsInstallationSucceeded, infrastructurev1beta1.K8sNodeAbsentReason, clusterv1.ConditionSeverityInfo, "")
		logger.Info("host removed from the cluster and the uninstall is executed successfully")
	} else {
		logger.Info("Skipping k8s node reset and k8s component uninstallation")
	}
	conditions.MarkFalse(byoHost, infrastructurev1beta1.K8sNodeBootstrapSucceeded, infrastructurev1beta1.K8sNodeAbsentReason, clusterv1.ConditionSeverityInfo, "")

	err := r.removeSentinelFile(ctx, byoHost)
	if err != nil {
		return err
	}

	err = r.deleteEndpointIP(ctx, byoHost)
	if err != nil {
		return err
	}

	byoHost.Spec.InstallationSecret = nil
	byoHost.Spec.UninstallationScript = nil
	r.removeAnnotations(ctx, byoHost)
	conditions.MarkFalse(byoHost, infrastructurev1beta1.K8sNodeBootstrapSucceeded, infrastructurev1beta1.K8sNodeAbsentReason, clusterv1.ConditionSeverityInfo, "")

	// Remove Machine ID file
	if err := os.Remove(machineIDFile); err != nil && !os.IsNotExist(err) {
		logger.Error(err, "failed to remove machine ID file")
	}

	return nil
}

func (r *HostReconciler) resetNode(ctx context.Context, byoHost *infrastructurev1beta1.ByoHost) error {
	logger := ctrl.LoggerFrom(ctx)
	logger.Info("Running kubeadm reset")

	err := r.CmdRunner.RunCmd(ctx, KubeadmResetCommand)
	if err != nil {
		r.Recorder.Event(byoHost, corev1.EventTypeWarning, "ResetK8sNodeFailed", "k8s Node Reset failed")
		return errors.Wrapf(err, "failed to exec kubeadm reset")
	}
	logger.Info("Kubernetes Node reset completed")
	r.Recorder.Event(byoHost, corev1.EventTypeNormal, "ResetK8sNodeSucceeded", "k8s Node Reset completed")
	return nil
}

// resetNodeWithRetry attempts to reset the node with retry logic
func (r *HostReconciler) resetNodeWithRetry(ctx context.Context, byoHost *infrastructurev1beta1.ByoHost) error {
	logger := ctrl.LoggerFrom(ctx)
	var lastErr error

	for attempt := 1; attempt <= 3; attempt++ {
		logger.Info(fmt.Sprintf("attempting to reset node (attempt %d/3)", attempt))

		err := r.resetNode(ctx, byoHost)
		if err == nil {
			logger.Info("node reset successfully")
			return nil
		}

		lastErr = err
		logger.Info(fmt.Sprintf("reset attempt %d failed: %v", attempt, err))

		if attempt < 3 {
			logger.Info("waiting 30s before retry")
			timer := time.NewTimer(30 * time.Second)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
				continue
			}
		}
	}

	logger.Error(lastErr, "all reset attempts failed")
	r.Recorder.Event(byoHost, corev1.EventTypeWarning, "ResetK8sNodeFailed", "Failed to reset node after multiple attempts")
	return lastErr
}

func (r *HostReconciler) bootstrapK8sNode(ctx context.Context, bootstrapScript string, byoHost *infrastructurev1beta1.ByoHost) error {
	logger := ctrl.LoggerFrom(ctx)
	logger.Info("Bootstraping k8s Node")

	// Check if TLS Bootstrap mode is enabled
	if byoHost.Spec.JoinMode == infrastructurev1beta1.JoinModeTLSBootstrap {
		return r.bootstrapK8sNodeTLS(ctx, byoHost)
	}

	return cloudinit.ScriptExecutor{
		WriteFilesExecutor:    r.FileWriter,
		RunCmdExecutor:        r.CmdRunner,
		ParseTemplateExecutor: r.TemplateParser,
		Hostname:              byoHost.Name,
	}.Execute(bootstrapScript)
}

// bootstrapK8sNodeTLS performs TLS Bootstrap mode node bootstrapping.
// This function:
// 1. Reads the TLS bootstrap secret containing CA cert and bootstrap kubeconfig
// 2. Writes the necessary configuration files to the host
// 3. Starts kubelet with TLS bootstrap configuration
// 4. Optionally starts kube-proxy if ManageKubeProxy is true
func (r *HostReconciler) bootstrapK8sNodeTLS(ctx context.Context, byoHost *infrastructurev1beta1.ByoHost) error {
	logger := ctrl.LoggerFrom(ctx)
	logger.Info("Bootstrapping k8s Node using TLS Bootstrap mode")

	// Read the TLS bootstrap secret
	if byoHost.Spec.BootstrapSecret == nil {
		return fmt.Errorf("bootstrap secret is required for TLS Bootstrap mode")
	}

	secret := &corev1.Secret{}
	if err := r.Client.Get(ctx, types.NamespacedName{
		Name:      byoHost.Spec.BootstrapSecret.Name,
		Namespace: byoHost.Spec.BootstrapSecret.Namespace,
	}, secret); err != nil {
		return fmt.Errorf("failed to get TLS bootstrap secret: %w", err)
	}

	// Write CA certificate
	if caCrt, ok := secret.Data["ca.crt"]; ok {
		caPath := "/etc/kubernetes/pki/ca.crt"
		if err := r.FileWriter.WriteToFile(&cloudinit.Files{
			Path:        caPath,
			Content:     string(caCrt),
			Permissions: "0644",
		}); err != nil {
			return fmt.Errorf("failed to write CA certificate: %w", err)
		}
		logger.Info("Wrote CA certificate", "path", caPath)
	}

	// Write bootstrap kubeconfig
	if bootstrapKubeconfig, ok := secret.Data["bootstrap-kubeconfig"]; ok {
		bootstrapKubeconfigPath := "/etc/kubernetes/bootstrap-kubeconfig"
		if err := r.FileWriter.WriteToFile(&cloudinit.Files{
			Path:        bootstrapKubeconfigPath,
			Content:     string(bootstrapKubeconfig),
			Permissions: "0600",
		}); err != nil {
			return fmt.Errorf("failed to write bootstrap kubeconfig: %w", err)
		}
		logger.Info("Wrote bootstrap kubeconfig", "path", bootstrapKubeconfigPath)
	}

	// Write kubelet configuration if provided
	if kubeletConfig, ok := secret.Data["kubelet-config.yaml"]; ok {
		kubeletConfigPath := "/var/lib/kubelet/config.yaml"
		if err := r.FileWriter.WriteToFile(&cloudinit.Files{
			Path:        kubeletConfigPath,
			Content:     string(kubeletConfig),
			Permissions: "0644",
		}); err != nil {
			return fmt.Errorf("failed to write kubelet config: %w", err)
		}
		logger.Info("Wrote kubelet config", "path", kubeletConfigPath)
	}

	// Write kube-proxy configuration if provided and ManageKubeProxy is true
	if byoHost.Spec.ManageKubeProxy {
		if kubeProxyConfig, ok := secret.Data["kube-proxy.kubeconfig"]; ok {
			kubeProxyConfigPath := "/etc/kubernetes/kube-proxy.kubeconfig"
			if err := r.FileWriter.WriteToFile(&cloudinit.Files{
				Path:        kubeProxyConfigPath,
				Content:     string(kubeProxyConfig),
				Permissions: "0600",
			}); err != nil {
				return fmt.Errorf("failed to write kube-proxy kubeconfig: %w", err)
			}
			logger.Info("Wrote kube-proxy kubeconfig", "path", kubeProxyConfigPath)
		}
	}

	// Start kubelet with TLS bootstrap configuration
	kubeletArgs := []string{
		"--bootstrap-kubeconfig=/etc/kubernetes/bootstrap-kubeconfig",
		"--kubeconfig=/etc/kubernetes/kubelet.conf",
		"--cert-dir=/var/lib/kubelet/pki",
		"--rotate-certificates=true",
		"--rotate-server-certificates=true",
		"--pod-manifest-path=/etc/kubernetes/manifests",
		// Inject provider-id for Cluster Autoscaler compatibility
		// This matches the behavior in Kubeadm mode (cloudinit interceptor)
		fmt.Sprintf("--provider-id=%s", common.GenerateProviderID(byoHost.Name)),
	}

	// Add cluster DNS configuration from annotations if available
	if endpointIP, ok := byoHost.Annotations[infrastructurev1beta1.EndPointIPAnnotation]; ok {
		kubeletArgs = append(kubeletArgs, fmt.Sprintf("--cluster-dns=%s", endpointIP))
	}

	// Start kubelet
	kubeletCmd := fmt.Sprintf("kubelet %s", strings.Join(kubeletArgs, " "))
	logger.Info("Starting kubelet", "command", kubeletCmd)

	if err := r.CmdRunner.RunCmd(ctx, kubeletCmd); err != nil {
		return fmt.Errorf("failed to start kubelet: %w", err)
	}

	// Start kube-proxy if ManageKubeProxy is true
	if byoHost.Spec.ManageKubeProxy {
		kubeProxyCmd := "kube-proxy --config=/etc/kubernetes/kube-proxy-config.yaml"
		logger.Info("Starting kube-proxy", "command", kubeProxyCmd)

		if err := r.CmdRunner.RunCmd(ctx, kubeProxyCmd); err != nil {
			return fmt.Errorf("failed to start kube-proxy: %w", err)
		}
	}

	logger.Info("Successfully bootstrapped k8s node using TLS Bootstrap mode")
	return nil
}

func (r *HostReconciler) removeSentinelFile(ctx context.Context, byoHost *infrastructurev1beta1.ByoHost) error {
	logger := ctrl.LoggerFrom(ctx)
	logger.Info("Removing the bootstrap sentinel file")
	if _, err := os.Stat(bootstrapSentinelFile); !os.IsNotExist(err) {
		err := os.Remove(bootstrapSentinelFile)
		if err != nil {
			return errors.Wrapf(err, "failed to delete sentinel file %s", bootstrapSentinelFile)
		}
	}
	return nil
}

func (r *HostReconciler) deleteEndpointIP(ctx context.Context, byoHost *infrastructurev1beta1.ByoHost) error {
	logger := ctrl.LoggerFrom(ctx)
	logger.Info("Removing network endpoints")
	if IP, ok := byoHost.Annotations[infrastructurev1beta1.EndPointIPAnnotation]; ok {
		network, err := vip.NewConfig(IP, registration.LocalHostRegistrar.ByoHostInfo.DefaultNetworkInterfaceName, "", false, 0)
		if err == nil {
			err := network.DeleteIP()
			if err != nil {
				return err
			}
		}
	}
	return nil
}

func (r *HostReconciler) removeAnnotations(ctx context.Context, byoHost *infrastructurev1beta1.ByoHost) {
	logger := ctrl.LoggerFrom(ctx)
	logger.Info("Removing annotations")
	// Remove host reservation
	byoHost.Status.MachineRef = nil

	// Remove BootstrapSecret
	byoHost.Spec.BootstrapSecret = nil

	// Remove cluster-name label
	delete(byoHost.Labels, clusterv1.ClusterNameLabel)

	// Remove Byomachine-name label
	delete(byoHost.Labels, infrastructurev1beta1.AttachedByoMachineLabel)

	// Remove the EndPointIP annotation
	delete(byoHost.Annotations, infrastructurev1beta1.EndPointIPAnnotation)

	// Remove the cleanup annotation
	delete(byoHost.Annotations, infrastructurev1beta1.HostCleanupAnnotation)

	// Remove the cluster version annotation
	delete(byoHost.Annotations, infrastructurev1beta1.K8sVersionAnnotation)

	// Remove the bundle registry annotation
	delete(byoHost.Annotations, infrastructurev1beta1.BundleLookupBaseRegistryAnnotation)
}
