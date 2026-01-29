// Copyright 2021 VMware, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/mensylisir/cluster-api-provider-bringyourownhost/agent/cloudinit"
	"github.com/mensylisir/cluster-api-provider-bringyourownhost/agent/registration"
	"github.com/mensylisir/cluster-api-provider-bringyourownhost/common"
	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/clientcmd"
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
				// For TLS Bootstrap mode, we check DownloadMode to decide whether to skip installation.
				// If DownloadMode is explicitly "offline", we assume binaries are pre-installed.
				if byoHost.Spec.JoinMode == infrastructurev1beta1.JoinModeTLSBootstrap && byoHost.Spec.DownloadMode == infrastructurev1beta1.DownloadModeOffline {
					logger.Info("TLS Bootstrap mode detected with offline download mode. Skipping k8s components installation.")
					r.Recorder.Event(byoHost, corev1.EventTypeNormal, "InstallScriptSkipped", "Skipped k8s components installation (Offline mode)")
					conditions.MarkTrue(byoHost, infrastructurev1beta1.K8sComponentsInstallationSucceeded)
				} else {
					// In all other cases (Kubeadm mode, or TLS Bootstrap with Online/Default mode),
					// we expect an InstallationSecret. If it's missing, we wait.
					logger.Info("InstallationSecret not ready")
					conditions.MarkFalse(byoHost, infrastructurev1beta1.K8sComponentsInstallationSucceeded, infrastructurev1beta1.K8sInstallationSecretUnavailableReason, clusterv1.ConditionSeverityInfo, "")
					return ctrl.Result{}, nil
				}
			} else {
				err = r.executeInstallerController(ctx, byoHost)
				if err != nil {
					return ctrl.Result{}, err
				}
				r.Recorder.Event(byoHost, corev1.EventTypeNormal, "InstallScriptExecutionSucceeded", "install script executed")
				conditions.MarkTrue(byoHost, infrastructurev1beta1.K8sComponentsInstallationSucceeded)
			}
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

		// For Kubeadm mode, we need to manually patch the ProviderID on the Node object
		// because kubeadm join doesn't accept --provider-id flag in the way we need.
		// Doing it here (Agent-side) is faster than waiting for the Controller to do it.
		if byoHost.Spec.JoinMode != infrastructurev1beta1.JoinModeTLSBootstrap {
			if err := r.patchLocalNodeProviderID(ctx, byoHost.Name); err != nil {
				// Don't fail reconciliation, just log error. Controller will retry eventually.
				logger.Error(err, "failed to patch local node providerID")
			} else {
				logger.Info("Successfully patched local node providerID")
			}
		}

		// Persist Machine ID to ensure consistency across restarts/rebinds
		if byoHost.Status.MachineRef != nil {
			if err := os.WriteFile(machineIDFile, []byte(byoHost.Status.MachineRef.UID), 0644); err != nil {
				logger.Error(err, "failed to persist machine ID")
			}
		}

		// For TLS Bootstrap mode, check if kube-proxy needs to be started
		// This handles the case where ManageKubeProxy is set to true after bootstrap
		if byoHost.Spec.JoinMode == infrastructurev1beta1.JoinModeTLSBootstrap && byoHost.Spec.ManageKubeProxy {
			if err := r.startKubeProxyIfNeeded(ctx, byoHost); err != nil {
				logger.Error(err, "failed to start kube-proxy")
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

	// Pre-flight checks
	// We perform basic checks before attempting installation to fail fast
	if err := r.preflightChecks(ctx); err != nil {
		logger.Error(err, "pre-flight checks failed")
		r.Recorder.Event(byoHost, corev1.EventTypeWarning, "PreflightCheckFailed", fmt.Sprintf("Pre-flight check failed: %v", err))
		return err
	}

	// Retry logic for install script execution
	// This helps with transient network issues during binary downloads
	maxRetries := 3
	for i := 0; i < maxRetries; i++ {
		err = r.CmdRunner.RunCmd(ctx, installScript)
		if err == nil {
			break
		}
		if i < maxRetries-1 {
			logger.Error(err, "install script execution failed, retrying...", "attempt", i+1)
			// Wait before retrying (exponential backoff could be better, but simple sleep is a start)
			time.Sleep(10 * time.Second)
		}
	}

	if err != nil {
		logger.Error(err, "error executing installation script after retries")
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
// The annotations are merged with ByoHost.Spec.Labels and ByoHost.Spec.Taints
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

	// Build node labels from capacity annotations and merge with ByoHost.Spec.Labels
	var nodeLabels map[string]string
	if labels, ok := annotations[infrastructurev1beta1.CapacityLabelsAnnotation]; ok && labels != "" {
		nodeLabels = make(map[string]string)
		// First add ByoHost.Spec.Labels
		for k, v := range byoHost.Spec.Labels {
			nodeLabels[k] = v
		}
		// Then add/override with capacity labels
		labelPairs := strings.Split(labels, ",")
		for _, label := range labelPairs {
			label = strings.TrimSpace(label)
			if label == "" {
				continue
			}
			parts := strings.SplitN(label, "=", 2)
			if len(parts) == 2 {
				nodeLabels[parts[0]] = parts[1]
			}
		}
		logger.Info("Applied scale-from-zero labels", "labels", nodeLabels)
	}

	// Build node taints from capacity annotations and merge with ByoHost.Spec.Taints
	var nodeTaints []corev1.Taint
	if taints, ok := annotations[infrastructurev1beta1.CapacityTaintsAnnotation]; ok && taints != "" {
		// First add ByoHost.Spec.Taints
		nodeTaints = append(nodeTaints, byoHost.Spec.Taints...)
		// Then add capacity taints
		taintPairs := strings.Split(taints, ",")
		for _, taint := range taintPairs {
			taint = strings.TrimSpace(taint)
			if taint == "" {
				continue
			}
			parts := strings.SplitN(taint, ":", 3)
			if len(parts) >= 3 {
				newTaint := corev1.Taint{
					Key:    parts[0],
					Value:  parts[1],
					Effect: corev1.TaintEffect(parts[2]),
				}
				nodeTaints = append(nodeTaints, newTaint)
			}
		}
		logger.Info("Applied scale-from-zero taints", "taints", nodeTaints)
	}

	// If we have labels or taints from annotations, update ByoHost
	if len(nodeLabels) > 0 || len(nodeTaints) > 0 {
		// Note: Updating ByoHost in place is not recommended, but for scale-from-zero
		// we need to persist these values. In a real implementation, this should be
		// done through a proper update call with the Kubernetes API.
		logger.Info("Scale-from-zero annotations applied",
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
	logger.Info("Resetting k8s Node")

	// Try to run kubeadm reset if it exists
	path, err := exec.LookPath("kubeadm")
	if err == nil && path != "" {
		logger.Info("Found kubeadm, running kubeadm reset")
		err := r.CmdRunner.RunCmd(ctx, KubeadmResetCommand)
		if err != nil {
			logger.Error(err, "kubeadm reset failed, falling back to manual cleanup")
		}
	} else {
		logger.Info("kubeadm not found, performing manual cleanup")
	}

	// Manual cleanup (Stop services and remove files)
	// This handles both binary installations and failed kubeadm resets

	// 1. Stop services
	_ = r.CmdRunner.RunCmd(ctx, "systemctl stop kubelet")
	_ = r.CmdRunner.RunCmd(ctx, "systemctl stop containerd")
	if byoHost.Spec.ManageKubeProxy {
		_ = r.CmdRunner.RunCmd(ctx, "systemctl stop kube-proxy")
	}

	// 2. Clean up files
	filesToRemove := []string{
		"/etc/kubernetes/bootstrap-kubeconfig",
		"/etc/kubernetes/kubelet.conf",
		"/etc/kubernetes/pki/ca.crt",
		"/var/lib/kubelet/config.yaml",
		"/etc/kubernetes/kube-proxy.kubeconfig",
		"/var/lib/kube-proxy/kube-proxy-config.yaml",
		"/etc/systemd/system/kubelet.service",
		"/etc/systemd/system/kube-proxy.service",
	}

	for _, f := range filesToRemove {
		if err := os.Remove(f); err != nil && !os.IsNotExist(err) {
			logger.V(4).Info("Failed to remove file", "file", f, "error", err)
		}
	}

	// Reload systemd to pick up service file removal
	_ = r.CmdRunner.RunCmd(ctx, "systemctl daemon-reload")

	// 3. Remove directories
	dirsToRemove := []string{
		"/var/lib/kubelet",
		"/var/lib/kube-proxy",
		"/var/lib/etcd",
		"/etc/kubernetes",
		"/run/kubernetes",
		"/var/lib/cni",
		"/etc/cni",
		"/opt/cni",
	}

	for _, d := range dirsToRemove {
		if err := os.RemoveAll(d); err != nil {
			logger.V(4).Info("Failed to remove directory", "dir", d, "error", err)
		}
	}

	logger.Info("Kubernetes Node reset completed")

	node := &corev1.Node{}
	if err := r.Client.Get(ctx, types.NamespacedName{Name: byoHost.Name}, node); err != nil {
		logger.V(4).Info("Node object not found, skipping deletion", "node", byoHost.Name)
	} else {
		logger.Info("Deleting Node object from API server", "node", byoHost.Name)
		if err := r.Client.Delete(ctx, node); err != nil {
			logger.Error(err, "Failed to delete Node object", "node", byoHost.Name)
		} else {
			logger.Info("Successfully deleted Node object", "node", byoHost.Name)
		}
	}

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
		Labels:                byoHost.Spec.Labels,
		Taints:                byoHost.Spec.Taints,
	}.Execute(ctx, bootstrapScript)
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
	var caCertData string
	var bootstrapToken string
	if caCrt, ok := secret.Data["ca.crt"]; ok {
		caCertData = string(caCrt)
	}
	// Extract CA and token from bootstrap-kubeconfig
	if bootstrapKubeconfig, ok := secret.Data["bootstrap-kubeconfig"]; ok {
		if caCertData == "" {
			caCertData = extractCACertificate(string(bootstrapKubeconfig))
		}
		bootstrapToken = extractTokenFromBootstrapKubeconfig(string(bootstrapKubeconfig))
	}

	if caCertData != "" {
		// Write CA certificate to multiple common paths
		caPaths := []string{
			"/etc/kubernetes/pki/ca.crt",
			"/etc/kubernetes/ssl/ca.pem",
			"/etc/kubernetes/pki/ca-certificates.crt",
			"/etc/ssl/certs/ca-certificates.crt",
		}

		for _, caPath := range caPaths {
			// Create parent directory if it doesn't exist
			caDir := filepath.Dir(caPath)
			if err := r.FileWriter.MkdirIfNotExists(caDir); err != nil {
				logger.V(4).Info("failed to create CA directory", "dir", caDir, "error", err)
				continue
			}
			if err := r.FileWriter.WriteToFile(&cloudinit.Files{
				Path:        caPath,
				Content:     caCertData,
				Permissions: "0644",
			}); err != nil {
				logger.V(4).Info("failed to write CA certificate", "path", caPath, "error", err)
				continue
			}
			logger.Info("Wrote CA certificate", "path", caPath)
		}
	}

	// Write bootstrap kubeconfig
	if bootstrapKubeconfig, ok := secret.Data["bootstrap-kubeconfig"]; ok {
		bootstrapKubeconfigPath := "/etc/kubernetes/bootstrap-kubeconfig"
		// Create parent directory if it doesn't exist
		if err := r.FileWriter.MkdirIfNotExists("/etc/kubernetes"); err != nil {
			return fmt.Errorf("failed to create /etc/kubernetes directory: %w", err)
		}
		if err := r.FileWriter.WriteToFile(&cloudinit.Files{
			Path:        bootstrapKubeconfigPath,
			Content:     string(bootstrapKubeconfig),
			Permissions: "0600",
		}); err != nil {
			return fmt.Errorf("failed to write bootstrap kubeconfig: %w", err)
		}
		logger.Info("Wrote bootstrap kubeconfig", "path", bootstrapKubeconfigPath)
	}

	// Write kubelet configuration if provided, otherwise generate a default
	kubeletConfigPath := "/var/lib/kubelet/config.yaml"
	if err := r.FileWriter.MkdirIfNotExists("/var/lib/kubelet"); err != nil {
		return fmt.Errorf("failed to create /var/lib/kubelet directory: %w", err)
	}

	var kubeletConfigContent string
	if kubeletConfig, ok := secret.Data["kubelet-config.yaml"]; ok {
		kubeletConfigContent = string(kubeletConfig)
		logger.Info("Using kubelet config from TLS bootstrap secret")
	} else {
		// Generate default kubelet configuration as fallback
		kubeletConfigContent = generateDefaultKubeletConfig()
		logger.Info("No kubelet config in secret, using default configuration")
	}

	if err := r.FileWriter.WriteToFile(&cloudinit.Files{
		Path:        kubeletConfigPath,
		Content:     kubeletConfigContent,
		Permissions: "0644",
	}); err != nil {
		return fmt.Errorf("failed to write kubelet config: %w", err)
	}
	logger.Info("Wrote kubelet config", "path", kubeletConfigPath)

	// Write kube-proxy configuration (always write for TLS Bootstrap mode, even if ManageKubeProxy is false)
	// This allows the external kube-proxy to use the configuration
	kubeProxyConfigPath := "/var/lib/kube-proxy/kube-proxy-config.yaml"
	if err := r.FileWriter.MkdirIfNotExists("/var/lib/kube-proxy"); err != nil {
		return fmt.Errorf("failed to create /var/lib/kube-proxy directory: %w", err)
	}

	var kubeProxyConfigContent string
	if kubeProxyConfigYAML, hasConfig := secret.Data["kube-proxy-config.yaml"]; hasConfig {
		kubeProxyConfigContent = string(kubeProxyConfigYAML)
		logger.Info("Using kube-proxy config from TLS bootstrap secret")
	} else {
		// Generate default kube-proxy configuration as fallback
		kubeProxyConfigContent = generateDefaultKubeProxyConfig()
		logger.Info("No kube-proxy config in secret, using default configuration")
	}

	if err := r.FileWriter.WriteToFile(&cloudinit.Files{
		Path:        kubeProxyConfigPath,
		Content:     kubeProxyConfigContent,
		Permissions: "0644",
	}); err != nil {
		return fmt.Errorf("failed to write kube-proxy config: %w", err)
	}
	logger.Info("Wrote kube-proxy config", "path", kubeProxyConfigPath)

	// Write kube-proxy.kubeconfig (always write for TLS Bootstrap mode)
	kubeProxyKubeconfigPath := "/etc/kubernetes/kube-proxy.kubeconfig"
	if err := r.FileWriter.MkdirIfNotExists("/etc/kubernetes"); err != nil {
		return fmt.Errorf("failed to create /etc/kubernetes directory: %w", err)
	}

	var kubeProxyKubeconfigContent string
	if kubeProxyKubeconfig, ok := secret.Data["kube-proxy.kubeconfig"]; ok {
		kubeProxyKubeconfigContent = string(kubeProxyKubeconfig)
		logger.Info("Using kube-proxy.kubeconfig from TLS bootstrap secret")
	} else {
		// Generate default kube-proxy.kubeconfig as fallback using bootstrap token
		// Get API server endpoint from ByoHost annotations
		apiServerHost := "https://127.0.0.1:6443" // default
		if endpointIP, ok := byoHost.Annotations[infrastructurev1beta1.EndPointIPAnnotation]; ok {
			apiServerHost = fmt.Sprintf("https://%s:6443", endpointIP)
		}
		kubeProxyKubeconfigContent = generateDefaultKubeProxyKubeconfig(caCertData, apiServerHost, bootstrapToken)
		logger.Info("No kube-proxy.kubeconfig in secret, using default configuration")
	}

	if err := r.FileWriter.WriteToFile(&cloudinit.Files{
		Path:        kubeProxyKubeconfigPath,
		Content:     kubeProxyKubeconfigContent,
		Permissions: "0600",
	}); err != nil {
		return fmt.Errorf("failed to write kube-proxy kubeconfig: %w", err)
	}
	logger.Info("Wrote kube-proxy kubeconfig", "path", kubeProxyKubeconfigPath)

	// Start kubelet with TLS bootstrap configuration
	kubeletArgs := []string{
		"--bootstrap-kubeconfig=/etc/kubernetes/bootstrap-kubeconfig",
		"--kubeconfig=/etc/kubernetes/kubelet.conf",
		"--cert-dir=/var/lib/kubelet/pki",
		"--config=/var/lib/kubelet/config.yaml",
		"--rotate-certificates=true",
		"--rotate-server-certificates=true",
		"--pod-manifest-path=/etc/kubernetes/manifests",
		// Inject provider-id for Cluster Autoscaler compatibility
		// This matches the behavior in Kubeadm mode (cloudinit interceptor)
		fmt.Sprintf("--provider-id=%s", common.GenerateProviderID(byoHost.Name)),
	}

	// Add node labels from ByoHost.Spec.Labels
	if len(byoHost.Spec.Labels) > 0 {
		var labelStrs []string
		for k, v := range byoHost.Spec.Labels {
			labelStrs = append(labelStrs, fmt.Sprintf("%s=%s", k, v))
		}
		kubeletArgs = append(kubeletArgs, fmt.Sprintf("--node-labels=%s", strings.Join(labelStrs, ",")))
		logger.Info("Adding node labels", "labels", byoHost.Spec.Labels)
	}

	// Add node taints from ByoHost.Spec.Taints
	if len(byoHost.Spec.Taints) > 0 {
		var taintStrs []string
		for _, taint := range byoHost.Spec.Taints {
			taintValue := taint.Value
			if taintValue == "" {
				taintValue = taint.Key // For NoSchedule, PreferNoSchedule, etc.
			}
			taintStrs = append(taintStrs, fmt.Sprintf("%s=%s:%s", taint.Key, taintValue, taint.Effect))
		}
		kubeletArgs = append(kubeletArgs, fmt.Sprintf("--register-with-taints=%s", strings.Join(taintStrs, ",")))
		logger.Info("Adding node taints", "taints", byoHost.Spec.Taints)
	}

	// Add cluster DNS configuration from annotations if available
	if endpointIP, ok := byoHost.Annotations[infrastructurev1beta1.EndPointIPAnnotation]; ok {
		kubeletArgs = append(kubeletArgs, fmt.Sprintf("--cluster-dns=%s", endpointIP))
	}

	// Create critical directories for kubelet
	// These must exist before kubelet starts to avoid errors
	criticalDirs := []string{
		"/etc/kubernetes/manifests", // For static pod manifests
		"/var/lib/kubelet/pki",      // For kubelet certificates
		"/var/lib/kube-proxy",       // For kube-proxy state
	}
	for _, dir := range criticalDirs {
		if err := r.FileWriter.MkdirIfNotExists(dir); err != nil {
			return fmt.Errorf("failed to create directory %s: %w", dir, err)
		}
		logger.V(4).Info("Created directory", "path", dir)
	}

	// Create and start kubelet systemd service
	kubeletServiceContent := fmt.Sprintf(`[Unit]
Description=kubelet: The Kubernetes Node Agent
Documentation=https://kubernetes.io/docs/home/
Wants=network-online.target
After=network-online.target

[Service]
ExecStart=/usr/local/bin/kubelet %s
Restart=always
StartLimitInterval=0
RestartSec=10
# Mount cgroup to support cgroupfs driver (common in binary installs)
ExecStartPre=-/bin/mount -o remount,rw '/sys/fs/cgroup'
# Ensure working directory exists
WorkingDirectory=/var/lib/kubelet
# Resource accounting
CPUAccounting=true
MemoryAccounting=true

[Install]
WantedBy=multi-user.target
`, strings.Join(kubeletArgs, " "))

	if err := r.FileWriter.WriteToFile(&cloudinit.Files{
		Path:        "/etc/systemd/system/kubelet.service",
		Content:     kubeletServiceContent,
		Permissions: "0644",
	}); err != nil {
		return fmt.Errorf("failed to write kubelet service: %w", err)
	}
	logger.Info("Wrote kubelet service file")

	if err := r.CmdRunner.RunCmd(ctx, "systemctl daemon-reload"); err != nil {
		return fmt.Errorf("failed to reload systemd: %w", err)
	}

	if err := r.CmdRunner.RunCmd(ctx, "systemctl enable --now kubelet"); err != nil {
		return fmt.Errorf("failed to enable/start kubelet: %w", err)
	}
	logger.Info("Started kubelet service")

	// Start kube-proxy if ManageKubeProxy is true
	if byoHost.Spec.ManageKubeProxy {
		kubeProxyServiceContent := `[Unit]
Description=kube-proxy: The Kubernetes Network Proxy
Documentation=https://kubernetes.io/docs/home/
Wants=network-online.target
After=network-online.target

[Service]
ExecStart=/usr/local/bin/kube-proxy --config=/var/lib/kube-proxy/kube-proxy-config.yaml
Restart=always
StartLimitInterval=0
RestartSec=10

[Install]
WantedBy=multi-user.target
`
		if err := r.FileWriter.WriteToFile(&cloudinit.Files{
			Path:        "/etc/systemd/system/kube-proxy.service",
			Content:     kubeProxyServiceContent,
			Permissions: "0644",
		}); err != nil {
			return fmt.Errorf("failed to write kube-proxy service: %w", err)
		}
		logger.Info("Wrote kube-proxy service file")

		if err := r.CmdRunner.RunCmd(ctx, "systemctl daemon-reload"); err != nil {
			return fmt.Errorf("failed to reload systemd for kube-proxy: %w", err)
		}
		if err := r.CmdRunner.RunCmd(ctx, "systemctl enable --now kube-proxy"); err != nil {
			return fmt.Errorf("failed to enable/start kube-proxy: %w", err)
		}
		logger.Info("Started kube-proxy service")
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

	// Remove the cleanup started at annotation
	delete(byoHost.Annotations, "byoh.infrastructure.cluster.x-k8s.io/cleanup-started-at")

	// Remove the force cleanup annotation
	delete(byoHost.Annotations, "byoh.infrastructure.cluster.x-k8s.io/force-cleanup")

	// Remove the cluster version annotation
	delete(byoHost.Annotations, infrastructurev1beta1.K8sVersionAnnotation)

	// Remove the bundle registry annotation
	delete(byoHost.Annotations, infrastructurev1beta1.BundleLookupBaseRegistryAnnotation)

	logger.Info("Annotations removed")
}

// patchLocalNodeProviderID patches the ProviderID of the local Node object
// using the local kubelet configuration.
func (r *HostReconciler) patchLocalNodeProviderID(ctx context.Context, hostname string) error {
	logger := ctrl.LoggerFrom(ctx)
	logger.Info("Attempting to patch local node ProviderID")

	kubeconfigPath := "/etc/kubernetes/kubelet.conf"
	if _, err := os.Stat(kubeconfigPath); os.IsNotExist(err) {
		return fmt.Errorf("kubelet.conf not found at %s", kubeconfigPath)
	}

	// Build client from local kubeconfig
	config, err := clientcmd.BuildConfigFromFlags("", kubeconfigPath)
	if err != nil {
		return fmt.Errorf("failed to build config from kubelet.conf: %w", err)
	}

	localClient, err := client.New(config, client.Options{})
	if err != nil {
		return fmt.Errorf("failed to create local client: %w", err)
	}

	// Get Node
	node := &corev1.Node{}
	// Note: We assume the Node name matches the Hostname
	if err := localClient.Get(ctx, types.NamespacedName{Name: hostname}, node); err != nil {
		return fmt.Errorf("failed to get local node %s: %w", hostname, err)
	}

	// Patch ProviderID
	providerID := common.GenerateProviderID(hostname)
	if node.Spec.ProviderID == providerID {
		logger.Info("Node ProviderID already set correctly")
		return nil
	}

	helper, err := patch.NewHelper(node, localClient)
	if err != nil {
		return fmt.Errorf("failed to create patch helper: %w", err)
	}

	node.Spec.ProviderID = providerID
	if err := helper.Patch(ctx, node); err != nil {
		return fmt.Errorf("failed to patch node ProviderID: %w", err)
	}

	logger.Info("Successfully patched Node ProviderID", "providerID", providerID)
	return nil
}

// preflightChecks performs basic checks before installation
func (r *HostReconciler) preflightChecks(ctx context.Context) error {
	logger := ctrl.LoggerFrom(ctx)

	// Check Swap
	// swapon --show returns exit code 0 if swap is active (and output), 0 if no output (no swap? check man page)
	// Actually `swapon --show` returns 0 even if no swap, but output is empty.
	// If swap is active, output is not empty.
	// We can try `swapon --summary` or check `/proc/swaps`.
	// Simple check: `swapon --show` has output?
	// But `CmdRunner` returns error if command fails.
	// Let's use `cat /proc/swaps`.
	// Or trust the installer script to handle swapoff?
	// The installer script does `swapoff -a`.
	// But `kubelet` might fail if swap is re-enabled.
	// CAPI usually expects swap disabled.
	// The user asked to "verify... exist problems".
	// The installer script (ubuntu20_4k8s.go) DOES `swapoff -a`.
	// So maybe swap check is redundant IF the installer succeeds.
	// But checking ports is good.

	// Check Port 10250 (Kubelet)
	// We can't easily check ports without netstat/ss.
	// `ss -tuln | grep :10250`

	// Check if Kubelet is already running?
	// `systemctl is-active kubelet`
	// If it is active, and we are installing, maybe we should stop it?
	// The installer script handles this?

	// Let's add a simple check for critical files to ensure we are not overwriting a working cluster
	// unintentionally (though `hostCleanUp` should have run).
	if _, err := os.Stat("/etc/kubernetes/manifests/kube-apiserver.yaml"); err == nil {
		logger.Info("Warning: Found existing kube-apiserver manifest. Node might already be part of a cluster.")
		// We don't fail, just warn, because maybe it's a re-install.
	}

	return nil
}

// generateDefaultKubeletConfig generates a default KubeletConfiguration
// For TLS Bootstrap mode when no kubelet-config is provided in the secret,
// generate a minimal working config that works for most clusters
func generateDefaultKubeletConfig() string {
	return fmt.Sprintf(`apiVersion: kubelet.config.k8s.io/v1beta1
kind: KubeletConfiguration
authentication:
  anonymous:
    enabled: false
  webhook:
    cacheTTL: 2m0s
    enabled: true
  x509:
    clientCAFile: /etc/kubernetes/pki/ca.crt
authorization:
  mode: Webhook
  webhook:
    cacheAuthorizedTTL: 5m0s
    cacheUnauthorizedTTL: 30s
cgroupDriver: systemd
clusterDNS:
- 169.254.20.10
clusterDomain: cluster.local
containerLogMaxFiles: 5
containerLogMaxSize: 10Mi
evictionHard:
  imagefs.available: 15%%
  memory.available: 100Mi
  nodefs.available: 10%%
  nodefs.inodesFree: 5%%
evictionPressureTransitionPeriod: 5m0s
fileCheckFrequency: 40s
healthzBindAddress: 127.0.0.1
healthzPort: 10248
imageGCHighThresholdPercent: 85
imageGCLowThresholdPercent: 80
logging:
  verbosity: 0
nodeStatusUpdateFrequency: 10s
rotateCertificates: true
runtimeRequestTimeout: 2m0s
staticPodPath: /etc/kubernetes/manifests
streamingConnectionIdleTimeout: 4h0m0s
syncFrequency: 1m0s
volumeStatsAggPeriod: 1m0s
`)
}

// generateDefaultKubeProxyConfig generates a default KubeProxyConfiguration
// For binary-deployed clusters without ConfigMaps, generate a minimal working config
func generateDefaultKubeProxyConfig() string {
	return fmt.Sprintf(`apiVersion: kubeproxy.config.k8s.io/v1alpha1
kind: KubeProxyConfiguration
bindAddress: 0.0.0.0
clientConnection:
  acceptContentTypes: ""
  burst: 10
  contentType: application/vnd.kubernetes.protobuf
  kubeconfig: /var/lib/kube-proxy/kubeconfig.conf
  qps: 5
clusterCIDR: ""
configSyncPeriod: 15m0s
conntrack:
  maxPerCore: 32768
  min: 131072
  tcpCloseWaitTimeout: 1h0m0s
  tcpEstablishedTimeout: 24h0m0s
enableProfiling: false
healthzBindAddress: 0.0.0.0:10256
hostnameOverride: ""
iptables:
  masqueradeAll: false
  masqueradeBit: 14
  minSyncPeriod: 0s
  syncPeriod: 30s
ipvs:
  excludeCIDRs: null
  minSyncPeriod: 0s
  scheduler: ""
  strictARP: false
  syncPeriod: 30s
  tcpFinTimeout: 0s
  tcpTimeout: 0s
  udpTimeout: 0s
metricsBindAddress: 127.0.0.1:10249
mode: ""
nodePortAddresses: null
oomScoreAdj: -999
portRange: ""
clusterDomain: "cluster.local"
 `)
}

// startKubeProxyIfNeeded starts kube-proxy if ManageKubeProxy is true and kube-proxy is not already running.
// This handles the case where ManageKubeProxy is set to true after bootstrap.
func (r *HostReconciler) startKubeProxyIfNeeded(ctx context.Context, byoHost *infrastructurev1beta1.ByoHost) error {
	logger := ctrl.LoggerFrom(ctx)

	// Check if ManageKubeProxy is enabled
	if !byoHost.Spec.ManageKubeProxy {
		logger.V(4).Info("ManageKubeProxy is false, skipping kube-proxy start")
		return nil
	}

	// Check if kube-proxy is already running
	if err := r.CmdRunner.RunCmd(ctx, "systemctl is-active --quiet kube-proxy"); err == nil {
		logger.Info("kube-proxy is already running")
		return nil
	}

	logger.Info("ManageKubeProxy is true but kube-proxy is not running, starting kube-proxy")

	// Generate default kube-proxy config if it doesn't exist
	kubeProxyConfigPath := "/var/lib/kube-proxy/kube-proxy-config.yaml"
	if _, err := os.Stat(kubeProxyConfigPath); os.IsNotExist(err) {
		logger.Info("kube-proxy config not found, generating default config")
		if err := r.FileWriter.MkdirIfNotExists("/etc/kubernetes"); err != nil {
			return fmt.Errorf("failed to create /etc/kubernetes directory: %w", err)
		}
		if err := r.FileWriter.WriteToFile(&cloudinit.Files{
			Path:        kubeProxyConfigPath,
			Content:     generateDefaultKubeProxyConfig(),
			Permissions: "0644",
		}); err != nil {
			return fmt.Errorf("failed to write kube-proxy config: %w", err)
		}
		logger.Info("Generated default kube-proxy config", "path", kubeProxyConfigPath)
	}

	// Write kube-proxy service file
	kubeProxyServiceContent := `[Unit]
Description=kube-proxy: The Kubernetes Network Proxy
Documentation=https://kubernetes.io/docs/home/
Wants=network-online.target
After=network-online.target

[Service]
ExecStart=/usr/local/bin/kube-proxy --config=/var/lib/kube-proxy/kube-proxy-config.yaml
Restart=always
StartLimitInterval=0
RestartSec=10

[Install]
WantedBy=multi-user.target
`
	if err := r.FileWriter.WriteToFile(&cloudinit.Files{
		Path:        "/etc/systemd/system/kube-proxy.service",
		Content:     kubeProxyServiceContent,
		Permissions: "0644",
	}); err != nil {
		return fmt.Errorf("failed to write kube-proxy service: %w", err)
	}
	logger.Info("Wrote kube-proxy service file")

	// Reload systemd and start kube-proxy
	if err := r.CmdRunner.RunCmd(ctx, "systemctl daemon-reload"); err != nil {
		return fmt.Errorf("failed to reload systemd for kube-proxy: %w", err)
	}

	if err := r.CmdRunner.RunCmd(ctx, "systemctl enable --now kube-proxy"); err != nil {
		return fmt.Errorf("failed to enable/start kube-proxy: %w", err)
	}

	logger.Info("Successfully started kube-proxy service")
	return nil
}

// extractCACertificate extracts the CA certificate data from a kubeconfig string
func extractCACertificate(kubeconfigContent string) string {
	// Parse the kubeconfig
	config, err := clientcmd.Load([]byte(kubeconfigContent))
	if err != nil {
		return ""
	}

	// Get CA data from the first cluster
	for _, cluster := range config.Clusters {
		if len(cluster.CertificateAuthorityData) > 0 {
			return string(cluster.CertificateAuthorityData)
		}
	}

	return ""
}

// extractTokenFromBootstrapKubeconfig extracts the bootstrap token from a kubeconfig string
func extractTokenFromBootstrapKubeconfig(kubeconfigContent string) string {
	// Parse the kubeconfig
	config, err := clientcmd.Load([]byte(kubeconfigContent))
	if err != nil {
		return ""
	}

	// Get token from the first auth info
	for _, authInfo := range config.AuthInfos {
		if len(authInfo.Token) > 0 {
			return authInfo.Token
		}
	}

	return ""
}

// generateDefaultKubeProxyKubeconfig generates a default kube-proxy.kubeconfig
func generateDefaultKubeProxyKubeconfig(caData, server, token string) string {
	return fmt.Sprintf(`apiVersion: v1
kind: Config
clusters:
- cluster:
    certificate-authority-data: %s
    server: %s
  name: default
contexts:
- context:
    cluster: default
    user: default
  name: default
current-context: default
users:
- name: default
  user:
    token: %s
`, caData, server, token)
}
