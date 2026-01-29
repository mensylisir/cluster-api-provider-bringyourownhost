// Copyright 2021 VMware, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"regexp"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/selection"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/cluster-api/controllers/external"
	"sigs.k8s.io/cluster-api/controllers/remote"
	"sigs.k8s.io/cluster-api/util"
	"sigs.k8s.io/cluster-api/util/conditions"
	"sigs.k8s.io/cluster-api/util/patch"
	"sigs.k8s.io/cluster-api/util/predicates"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	bootstraputil "k8s.io/cluster-bootstrap/token/util"
	"github.com/go-logr/logr"
	"github.com/mensylisir/cluster-api-provider-bringyourownhost/common/bootstraptoken"
	infrav1 "github.com/mensylisir/cluster-api-provider-bringyourownhost/apis/infrastructure/v1beta1"
	"github.com/mensylisir/cluster-api-provider-bringyourownhost/common"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/cluster-api/util/annotations"
	"sigs.k8s.io/yaml"
)

const (
	// ProviderIDSuffixLength length of provider id suffix
	ProviderIDSuffixLength = 6
	// RequeueForbyohost requeue delay for byoh host
	RequeueForbyohost = 10 * time.Second
	// RequeueInstallerConfigTime requeue delay for installer config
	RequeueInstallerConfigTime = 10 * time.Second

	// HostLeaseAnnotationKey annotation key for lease-based locking
	HostLeaseAnnotationKey = "byohost.infrastructure.cluster.x-k8s.io/lease"
	// HostLeaseTimeoutSeconds lease timeout in seconds (30 seconds)
	HostLeaseTimeoutSeconds = 30
	// MaxRetries maximum number of retries for attaching a host
	MaxRetries = 5

	// hostCleanupTimeout reference timeout for ByoMachine deletion
	// This should match the default value in byohost_controller.go
	hostCleanupTimeout = 5 * time.Minute
)

// ByoMachineReconciler reconciles a ByoMachine object
type ByoMachineReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Tracker  *remote.ClusterCacheTracker
	Recorder record.EventRecorder

	// roundRobinIndex tracks the last selected host for round-robin selection
	// This is only for in-memory tracking and is not persisted
	roundRobinIndex map[string]int
}

// lockInfo holds lease lock information for a ByoHost
type lockInfo struct {
	Holder      string    `json:"holder"`
	AcquireTime time.Time `json:"acquireTime"`
	MachineName string    `json:"machineName"`
}

//+kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=byomachines,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=byomachines/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=byomachines/finalizers,verbs=update
// +kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=byohosts,verbs=get;list;watch
// +kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=byohosts/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=*,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=cluster.x-k8s.io,resources=clusters;machines,verbs=get;list;watch
// +kubebuilder:rbac:groups=cluster.x-k8s.io,resources=machines;machines/status,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=events,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the ByoMachine object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.8.3/pkg/reconcile

// Reconcile handles ByoMachine events
// nolint: gocyclo, funlen
func (r *ByoMachineReconciler) Reconcile(ctx context.Context, req ctrl.Request) (_ ctrl.Result, reterr error) {
	logger := log.FromContext(ctx)
	logger.Info("Reconcile request received")

	// Fetch the ByoMachine instance
	byoMachine := &infrav1.ByoMachine{}
	err := r.Client.Get(ctx, req.NamespacedName, byoMachine)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// Fetch the Machine
	machine, err := util.GetOwnerMachine(ctx, r.Client, byoMachine.ObjectMeta)
	if err != nil {
		logger.Error(err, "failed to get Owner Machine")
		return ctrl.Result{}, err
	}

	if machine == nil {
		logger.Info("Waiting for Machine Controller to set OwnerRef on ByoMachine")
		return ctrl.Result{}, nil
	}

	// Fetch the Cluster
	cluster, err := util.GetClusterFromMetadata(ctx, r.Client, byoMachine.ObjectMeta)
	if err != nil {
		logger.Error(err, "ByoMachine owner Machine is missing cluster label or cluster does not exist")
		return ctrl.Result{}, err
	}

	if cluster == nil {
		logger.Info(fmt.Sprintf("Please associate this machine with a cluster using the label %s: <name of cluster>", clusterv1.ClusterNameLabel))
		return ctrl.Result{}, nil
	}
	logger = logger.WithValues("cluster", cluster.Name)

	byoCluster := &infrav1.ByoCluster{}
	infraClusterName := client.ObjectKey{
		Namespace: byoMachine.Namespace,
		Name:      cluster.Spec.InfrastructureRef.Name,
	}

	if err = r.Client.Get(ctx, infraClusterName, byoCluster); err != nil {
		logger.Error(err, "failed to get infra cluster")
		return ctrl.Result{}, nil
	}

	helper, _ := patch.NewHelper(byoMachine, r.Client)
	defer func() {
		if err = helper.Patch(ctx, byoMachine); err != nil && reterr == nil {
			logger.Error(err, "failed to patch byomachine")
			reterr = err
		}
	}()

	// Fetch the BYOHost which is referencing this machine, if any
	refByoHost, err := r.FetchAttachedByoHost(ctx, byoMachine.Name, byoMachine.Namespace)
	if err != nil {
		return ctrl.Result{}, err
	}
	if refByoHost != nil {
		logger = logger.WithValues("BYOHost", refByoHost.Name)
	}

	// Create the machine scope
	machineScope, err := newByoMachineScope(byoMachineScopeParams{
		Client:     r.Client,
		Cluster:    cluster,
		Machine:    machine,
		ByoCluster: byoCluster,
		ByoMachine: byoMachine,
		ByoHost:    refByoHost,
	})
	if err != nil {
		return ctrl.Result{}, err
	}

	// Return early if the object or Cluster is paused
	if annotations.IsPaused(cluster, byoMachine) {
		logger.Info("byoMachine or linked Cluster is marked as paused. Won't reconcile")
		if machineScope.ByoHost != nil {
			if err = r.setPausedConditionForByoHost(ctx, machineScope, true); err != nil {
				logger.Error(err, "cannot set paused annotation for byohost")
			}
		}
		conditions.MarkFalse(byoMachine, infrav1.BYOHostReady, infrav1.ClusterOrResourcePausedReason, clusterv1.ConditionSeverityInfo, "")
		return ctrl.Result{}, nil
	}

	// Handle deleted machines
	if !byoMachine.ObjectMeta.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, machineScope)
	}

	// Handle non-deleted machines
	return r.reconcileNormal(ctx, machineScope)
}

// FetchAttachedByoHost fetches BYOHost attached to this machine
func (r *ByoMachineReconciler) FetchAttachedByoHost(ctx context.Context, byomachineName, byomachineNamespace string) (*infrav1.ByoHost, error) {
	logger := log.FromContext(ctx)
	logger.Info("Fetching an attached ByoHost")

	selector := labels.NewSelector()
	byohostLabels, _ := labels.NewRequirement(infrav1.AttachedByoMachineLabel, selection.Equals, []string{byomachineNamespace + "." + byomachineName})
	selector = selector.Add(*byohostLabels)
	hostsList := &infrav1.ByoHostList{}
	err := r.Client.List(
		ctx,
		hostsList,
		&client.ListOptions{LabelSelector: selector},
	)
	if err != nil {
		return nil, err
	}
	var refByoHost *infrav1.ByoHost = nil
	if len(hostsList.Items) > 0 {
		refByoHost = &hostsList.Items[0]
		logger.Info("Successfully fetched an attached Byohost", "byohost", refByoHost.Name)
		if len(hostsList.Items) > 1 {
			errMsg := "more than one Byohost object attached to this Byomachine object. Only take one of it, please take care of the rest manually"
			logger.Error(errors.New(errMsg), errMsg)
		}
	}
	return refByoHost, nil
}

func (r *ByoMachineReconciler) reconcileDelete(ctx context.Context, machineScope *byoMachineScope) (reconcile.Result, error) {
	logger := log.FromContext(ctx).WithValues("cluster", machineScope.Cluster.Name)
	logger.Info("Deleting ByoMachine")

	// If ByoHost is not found via label (e.g., stale label from previous Machine),
	// try to find it by matching machineRef.UID with byoMachine.UID
	if machineScope.ByoHost == nil {
		hostsList := &infrav1.ByoHostList{}
		if err := r.Client.List(ctx, hostsList); err != nil {
			return reconcile.Result{}, err
		}
		for i := range hostsList.Items {
			host := &hostsList.Items[i]
			if host.Status.MachineRef != nil && host.Status.MachineRef.UID == machineScope.ByoMachine.UID {
				logger.Info("Found ByoHost via machineRef UID match", "byohost", host.Name)
				machineScope.ByoHost = host
				break
			}
		}
	}

	// Check if there's an associated ByoHost
	if machineScope.ByoHost != nil {
		// Check if ByoHost is already marked for deletion
		if !machineScope.ByoHost.ObjectMeta.DeletionTimestamp.IsZero() {
			logger.Info("ByoHost already marked for deletion")
			// Check if cleanup has completed (MachineRef is cleared)
			if machineScope.ByoHost.Status.MachineRef != nil {
				// Check if we've exceeded the cleanup timeout
				cleanupDuration := time.Since(machineScope.ByoHost.DeletionTimestamp.Time)
				if cleanupDuration > hostCleanupTimeout {
					logger.Info("ByoHost cleanup timeout exceeded, proceeding with cleanup completion",
						"timeout", hostCleanupTimeout, "elapsed", cleanupDuration)
					// Timeout exceeded, proceed to complete ByoMachine deletion
				} else {
					// Still within timeout, wait for Agent to complete cleanup
					logger.Info("Waiting for Agent to complete cleanup",
						"elapsed", cleanupDuration, "timeout", hostCleanupTimeout)
					return reconcile.Result{RequeueAfter: RequeueForbyohost}, nil
				}
			}
			// Cleanup complete or timed out, proceed
		} else {
			// Add annotation to trigger host cleanup
			logger.Info("Releasing ByoHost", "byohost", machineScope.ByoHost.Name)
			if err := r.markHostForCleanup(ctx, machineScope); err != nil {
				return reconcile.Result{}, err
			}
			r.Recorder.Eventf(machineScope.ByoHost, corev1.EventTypeNormal, "ByoHostReleaseSucceeded", "ByoHost Released by %s", machineScope.ByoMachine.Name)
			r.Recorder.Eventf(machineScope.ByoMachine, corev1.EventTypeNormal, "ByoHostReleaseSucceeded", "Released ByoHost %s", machineScope.ByoHost.Name)

			// Update ByoMachine status to indicate cleanup has started
			machineScope.ByoMachine.Status.CleanupStarted = true

			logger.Info("Waiting for host cleanup to complete")
			return reconcile.Result{RequeueAfter: RequeueForbyohost}, nil
		}
	}

	// Update status to indicate cleanup completed
	machineScope.ByoMachine.Status.CleanupCompleted = true
	// Clear NodeRef since the node should be cleaned up
	machineScope.ByoMachine.Status.NodeRef = nil

	controllerutil.RemoveFinalizer(machineScope.ByoMachine, infrav1.MachineFinalizer)
	return reconcile.Result{}, nil
}

func (r *ByoMachineReconciler) reconcileNormal(ctx context.Context, machineScope *byoMachineScope) (reconcile.Result, error) {
	logger := log.FromContext(ctx).WithValues("cluster", machineScope.Cluster.Name)
	logger.Info("Reconciling ByoMachine")

	controllerutil.AddFinalizer(machineScope.ByoMachine, infrav1.MachineFinalizer)

	// Check if Machine is marked for remediation by MachineHealthCheck
	if machineScope.Machine.Annotations != nil {
		if _, isRemediation := machineScope.Machine.Annotations["cluster.x-k8s.io/remediation-for"]; isRemediation {
			logger.Info("Machine is being remediated by MachineHealthCheck, checking if immediate cleanup is needed")
			// If Machine is being remediated, prioritize cleanup
			if machineScope.ByoHost != nil {
				if machineScope.ByoHost.Status.MachineRef != nil {
					// Check if node is still registered
					nodeRef := machineScope.ByoMachine.Status.NodeRef
					if nodeRef != nil {
						node := &corev1.Node{}
						if err := r.Client.Get(ctx, types.NamespacedName{Name: nodeRef.Name}, node); err != nil {
							// Node doesn't exist, safe to cleanup immediately
							logger.Info("Node no longer exists, proceeding with immediate cleanup")
							if err := r.markHostForCleanup(ctx, machineScope); err != nil {
								return reconcile.Result{}, err
							}
						} else if !node.DeletionTimestamp.IsZero() {
							// Node is being deleted, safe to cleanup
							logger.Info("Node is being deleted, proceeding with cleanup")
							if err := r.markHostForCleanup(ctx, machineScope); err != nil {
								return reconcile.Result{}, err
							}
						}
					}
				}
			}
		}
	}

	if machineScope.ByoHost != nil {
		// if there is already byohost associated with it, make sure the paused status of byohost is false
		if err := r.setPausedConditionForByoHost(ctx, machineScope, false); err != nil {
			logger.Error(err, "Set resume flag for byohost failed")
			return ctrl.Result{}, err
		}
	}

	if machineScope.ByoMachine.Spec.InstallerRef != nil {
		if err := r.createInstallerConfig(ctx, machineScope); err != nil {
			logger.Error(err, "create installer config failed")
			return ctrl.Result{}, err
		}
	}

	if !machineScope.Cluster.Status.InfrastructureReady {
		logger.Info("Cluster infrastructure is not ready yet")
		conditions.MarkFalse(machineScope.ByoMachine, infrav1.BYOHostReady, infrav1.WaitingForClusterInfrastructureReason, clusterv1.ConditionSeverityInfo, "")
		return reconcile.Result{}, nil
	}

	// For TLS Bootstrap mode, we create our own bootstrap secret directly
	// So we don't need to wait for Machine.Spec.Bootstrap.DataSecretName
	// For Kubeadm mode, we need to wait for the bootstrap data secret to be created
	if machineScope.ByoMachine.Spec.JoinMode != infrav1.JoinModeTLSBootstrap {
		if machineScope.Machine.Spec.Bootstrap.DataSecretName == nil {
			logger.Info("Bootstrap Data Secret not available yet")
			conditions.MarkFalse(machineScope.ByoMachine, infrav1.BYOHostReady, infrav1.WaitingForBootstrapDataSecretReason, clusterv1.ConditionSeverityInfo, "")
			return reconcile.Result{}, nil
		}
	}

	// If there is not yet an byoHost for this byoMachine,
	// then pick one from the host capacity pool
	if machineScope.ByoHost == nil {
		logger.Info("Attempting host reservation")
		if res, err := r.attachByoHost(ctx, machineScope); err != nil {
			return res, err
		}
		conditions.MarkFalse(machineScope.ByoMachine, infrav1.BYOHostReady, infrav1.InstallationSecretNotAvailableReason, clusterv1.ConditionSeverityInfo, "")
		r.Recorder.Eventf(machineScope.ByoHost, corev1.EventTypeNormal, "ByoHostAttachSucceeded", "Attached to ByoMachine %s", machineScope.ByoMachine.Name)
		r.Recorder.Eventf(machineScope.ByoMachine, corev1.EventTypeNormal, "ByoHostAttachSucceeded", "Attached ByoHost %s", machineScope.ByoHost.Name)
	}

	if machineScope.ByoMachine.Status.HostInfo == (infrav1.HostInfo{}) {
		machineScope.ByoMachine.Status.HostInfo = machineScope.ByoHost.Status.HostDetails
	}

	if machineScope.ByoMachine.Spec.InstallerRef != nil && machineScope.ByoHost.Spec.InstallationSecret == nil {
		res, err := r.setInstallationSecretForByoHost(ctx, machineScope)
		if err != nil {
			logger.Error(err, "failed to set installation secret on byohost")
			return res, err
		}
		if res.RequeueAfter > 0 {
			return res, nil
		}
	}

	logger.Info("Updating Node with ProviderID")
	return r.updateNodeProviderID(ctx, machineScope)
}

func (r *ByoMachineReconciler) updateNodeProviderID(ctx context.Context, machineScope *byoMachineScope) (ctrl.Result, error) {
	logger := log.FromContext(ctx).WithValues("cluster", machineScope.Cluster.Name)
	remoteClient, err := r.getRemoteClient(ctx, machineScope.ByoMachine)
	if err != nil {
		logger.Error(err, "failed to get remote client")
		return ctrl.Result{}, err
	}

	providerID, node, err := r.setNodeProviderID(ctx, remoteClient, machineScope.ByoHost)
	if err != nil {
		// Check if the error is because the Node doesn't exist yet
		// This is expected when the kubelet is still bootstrapping
		if apierrors.IsNotFound(err) {
			logger.Info("Node not found yet, waiting for kubelet to register the node",
				"node", machineScope.ByoHost.Name)
			conditions.MarkFalse(machineScope.ByoMachine, infrav1.BYOHostReady,
				infrav1.WaitingForNodeRefReason, clusterv1.ConditionSeverityInfo,
				"Waiting for node %s to be registered", machineScope.ByoHost.Name)
			// Requeue after a short delay instead of returning an error
			return ctrl.Result{RequeueAfter: RequeueForbyohost}, nil
		}
		// For other errors, log and return
		logger.Error(err, "failed to set node providerID")
		r.Recorder.Eventf(machineScope.ByoMachine, corev1.EventTypeWarning, "SetNodeProviderFailed", "Failed to set ProviderID: %v", err)
		return ctrl.Result{}, err
	}

	machineScope.ByoMachine.Spec.ProviderID = providerID
	machineScope.ByoMachine.Status.Ready = true

	// Set the NodeRef to track the created Node object
	if node != nil {
		machineScope.ByoMachine.Status.NodeRef = &corev1.ObjectReference{
			APIVersion: "v1",
			Kind:       "Node",
			Name:       node.Name,
		}
	}

	// Set Addresses from ByoHost.Network if not already set
	// This propagates network information to Machine.status.addresses
	if len(machineScope.ByoMachine.Status.Addresses) == 0 && machineScope.ByoHost != nil {
		machineScope.ByoMachine.Status.Addresses = r.convertNetworkToAddresses(machineScope.ByoHost.Status.Network)
	}

	conditions.MarkTrue(machineScope.ByoMachine, infrav1.BYOHostReady)
	r.Recorder.Eventf(machineScope.ByoMachine, corev1.EventTypeNormal, "NodeProvisionedSucceeded", "Provisioned Node %s", machineScope.ByoHost.Name)
	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *ByoMachineReconciler) SetupWithManager(ctx context.Context, mgr ctrl.Manager) error {
	var (
		controlledType     = &infrav1.ByoMachine{}
		controlledTypeName = reflect.TypeOf(controlledType).Elem().Name()
		controlledTypeGVK  = infrav1.GroupVersion.WithKind(controlledTypeName)
	)
	logger := ctrl.LoggerFrom(ctx)
	ClusterToByoMachines := r.ClusterToByoMachines(logger)

	return ctrl.NewControllerManagedBy(mgr).
		For(controlledType).
		Watches(
			&source.Kind{Type: &infrav1.ByoHost{}},
			handler.EnqueueRequestsFromMapFunc(ByoHostToByoMachineMapFunc(controlledTypeGVK)),
		).
		// Watch the CAPI resource that owns this infrastructure resource
		Watches(
			&source.Kind{Type: &clusterv1.Machine{}},
			handler.EnqueueRequestsFromMapFunc(util.MachineToInfrastructureMapFunc(controlledTypeGVK)),
		).
		Watches(
			&source.Kind{Type: &clusterv1.Cluster{}},
			handler.EnqueueRequestsFromMapFunc(ClusterToByoMachines),
			builder.WithPredicates(predicates.ClusterUnpausedAndInfrastructureReady(ctrl.LoggerFrom(ctx))),
		).
		Complete(r)
}

// ClusterToByoMachines is a handler.ToRequestsFunc to be used to enqeue requests for reconciliation
// of ByoMachines
func (r *ByoMachineReconciler) ClusterToByoMachines(logger logr.Logger) handler.MapFunc {
	return func(o client.Object) []ctrl.Request {
		c, ok := o.(*clusterv1.Cluster)
		if !ok {
			errMsg := fmt.Sprintf("Expected a Cluster but got a %T", o)
			logger.Error(errors.New(errMsg), errMsg)
			return nil
		}

		logger = logger.WithValues("objectMapper", "ClusterToByoMachines", "namespace", c.Namespace, "Cluster", c.Name)

		// Don't handle deleted clusters
		if !c.ObjectMeta.DeletionTimestamp.IsZero() {
			logger.Info("Cluster has a deletion timestamp, skipping mapping.")
			return nil
		}

		clusterLabels := map[string]string{clusterv1.ClusterNameLabel: c.Name}
		byoMachineList := &infrav1.ByoMachineList{}
		if err := r.Client.List(context.TODO(), byoMachineList, client.InNamespace(c.Namespace), client.MatchingLabels(clusterLabels)); err != nil {
			logger.Error(err, "Failed to get ByoMachine, skipping mapping.")
			return nil
		}

		result := make([]ctrl.Request, 0, len(byoMachineList.Items))
		for i := range byoMachineList.Items {
			logger.WithValues("byoMachine", byoMachineList.Items[i].Name)
			logger.Info("Adding ByoMachine to reconciliation request.")
			result = append(result, ctrl.Request{NamespacedName: client.ObjectKey{Namespace: byoMachineList.Items[i].Namespace, Name: byoMachineList.Items[i].Name}})
		}
		return result
	}
}

// setNodeProviderID patches the provider id to the node using
// client pointing to workload cluster
func (r *ByoMachineReconciler) setNodeProviderID(ctx context.Context, remoteClient client.Client, host *infrav1.ByoHost) (string, *corev1.Node, error) {
	node := &corev1.Node{}
	key := client.ObjectKey{Name: host.Name, Namespace: host.Namespace}
	err := remoteClient.Get(ctx, key, node)
	if err != nil {
		return "", nil, err
	}

	if node.Spec.ProviderID != "" {
		var match bool
		// Validate existing providerID matches expected format
		match, err = validateProviderID(node.Spec.ProviderID, host.Name)
		if err != nil {
			return "", nil, fmt.Errorf("failed to validate providerID: %w", err)
		}
		if match {
			return node.Spec.ProviderID, node, nil
		}
		return "", nil, fmt.Errorf("invalid format for node.Spec.ProviderID: %s (expected format: %s)", node.Spec.ProviderID, generateProviderID(host))
	}

	helper, err := patch.NewHelper(node, remoteClient)
	if err != nil {
		return "", nil, err
	}

	// Use standardized format to match Agent
	node.Spec.ProviderID = generateProviderID(host)

	return node.Spec.ProviderID, node, helper.Patch(ctx, node)
}

func (r *ByoMachineReconciler) getRemoteClient(ctx context.Context, byoMachine *infrav1.ByoMachine) (client.Client, error) {
	cluster, err := util.GetClusterFromMetadata(ctx, r.Client, byoMachine.ObjectMeta)
	if err != nil {
		return nil, err
	}
	remoteClient, err := r.Tracker.GetClient(ctx, util.ObjectKey(cluster))
	if err != nil {
		return nil, err
	}

	return remoteClient, nil
}

func (r *ByoMachineReconciler) setPausedConditionForByoHost(ctx context.Context, machineScope *byoMachineScope, isPaused bool) error {
	helper, err := patch.NewHelper(machineScope.ByoHost, r.Client)
	if err != nil {
		return err
	}

	if isPaused {
		desired := map[string]string{
			clusterv1.PausedAnnotation: "",
		}
		annotations.AddAnnotations(machineScope.ByoHost, desired)
	} else {
		delete(machineScope.ByoHost.Annotations, clusterv1.PausedAnnotation)
	}

	return helper.Patch(ctx, machineScope.ByoHost)
}

func (r *ByoMachineReconciler) setInstallationSecretForByoHost(ctx context.Context, machineScope *byoMachineScope) (ctrl.Result, error) {
	logger := log.FromContext(ctx).WithValues("cluster", machineScope.Cluster.Name)
	installerConfig, ready, err := r.getInstallerConfigAndStatus(ctx, machineScope)
	if err != nil {
		return ctrl.Result{}, err
	}
	if !ready {
		logger.Info("Installer config is not ready, requeuing")
		return ctrl.Result{RequeueAfter: RequeueInstallerConfigTime}, nil
	}

	helper, err := patch.NewHelper(machineScope.ByoHost, r.Client)
	if err != nil {
		return ctrl.Result{}, err
	}
	secret, found, err := unstructured.NestedFieldNoCopy(installerConfig.Object, "status", "installationSecret")
	if err != nil {
		return ctrl.Result{}, err
	}
	if !found {
		return ctrl.Result{}, fmt.Errorf("installation secret not set on ready installerconfig %s %s", installerConfig.GetKind(), installerConfig.GetName())
	}
	secretRef := &corev1.ObjectReference{}
	if err = runtime.DefaultUnstructuredConverter.FromUnstructured(secret.(map[string]interface{}), secretRef); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to convert unstructured field, %s", err.Error())
	}
	machineScope.ByoHost.Spec.InstallationSecret = secretRef
	return ctrl.Result{}, helper.Patch(ctx, machineScope.ByoHost)
}

func (r *ByoMachineReconciler) getInstallerConfigAndStatus(ctx context.Context, machineScope *byoMachineScope) (*unstructured.Unstructured, bool, error) {
	installerConfig, err := r.getInstallerConfig(ctx, machineScope.ByoMachine)
	if err != nil {
		return nil, false, err
	}
	ready, err := external.IsReady(installerConfig)
	if err != nil {
		return installerConfig, false, err
	}
	return installerConfig, ready, nil
}

func (r *ByoMachineReconciler) attachByoHost(ctx context.Context, machineScope *byoMachineScope) (ctrl.Result, error) {
	logger := log.FromContext(ctx).WithValues("cluster", machineScope.Cluster.Name)
	var selector labels.Selector
	var err error
	if machineScope.ByoHost != nil {
		return ctrl.Result{}, nil
	}

	hostsList := &infrav1.ByoHostList{}
	// LabelSelector filter for byohosts
	if machineScope.ByoMachine.Spec.Selector != nil {
		selector, err = metav1.LabelSelectorAsSelector(machineScope.ByoMachine.Spec.Selector)
		if err != nil {
			logger.Error(err, "Label Selector as selector failed")
			return ctrl.Result{}, err
		}
	} else {
		selector = labels.NewSelector()
	}

	byohostLabels, _ := labels.NewRequirement(clusterv1.ClusterNameLabel, selection.DoesNotExist, nil)
	selector = selector.Add(*byohostLabels)

	err = r.Client.List(ctx, hostsList, &client.ListOptions{LabelSelector: selector})
	if err != nil {
		logger.Error(err, "failed to list byohosts")
		return ctrl.Result{RequeueAfter: RequeueForbyohost}, err
	}
	if len(hostsList.Items) == 0 {
		logger.Info("No hosts found, waiting..")
		r.Recorder.Eventf(machineScope.ByoMachine, corev1.EventTypeWarning, "ByoHostSelectionFailed", "No available ByoHost")
		conditions.MarkFalse(machineScope.ByoMachine, infrav1.BYOHostReady, infrav1.BYOHostsUnavailableReason, clusterv1.ConditionSeverityInfo, "")
		return ctrl.Result{RequeueAfter: RequeueForbyohost}, errors.New("no hosts found")
	}

	// Try to attach a host with lease-based concurrency control
	clusterName := machineScope.ByoMachine.Labels[clusterv1.ClusterNameLabel]
	controllerID := fmt.Sprintf("byomachine-controller-%s", machineScope.ByoMachine.Name)

	for attempt := 0; attempt < MaxRetries; attempt++ {
		// Select a host using round-robin to avoid bias
		selectedHost := r.selectHostForClaim(hostsList.Items, clusterName, machineScope.ByoMachine)
		if selectedHost == nil {
			logger.Error(nil, "no host selected by round-robin algorithm")
			return ctrl.Result{RequeueAfter: RequeueForbyohost}, errors.New("no host selected")
		}

		// Re-fetch the host from the API server to get the latest version
		latestHost := &infrav1.ByoHost{}
		if err := r.Client.Get(ctx, client.ObjectKey{Namespace: selectedHost.Namespace, Name: selectedHost.Name}, latestHost); err != nil {
			logger.Error(err, "failed to re-fetch byohost", "byohost", selectedHost.Name)
			// Wait with exponential backoff before trying another host
			time.Sleep(exponentialBackoff(attempt))
			continue
		}

		// Check if another reconciler already claimed this host
		if latestHost.Status.MachineRef != nil {
			// Check if the referenced ByoMachine still exists
			existingMachine := &infrav1.ByoMachine{}
			err := r.Client.Get(ctx, client.ObjectKey{
				Namespace: latestHost.Status.MachineRef.Namespace,
				Name:      latestHost.Status.MachineRef.Name,
			}, existingMachine)
			if err != nil {
				if apierrors.IsNotFound(err) {
					// Referenced ByoMachine no longer exists, this host is orphaned
					// Clear the stale MachineRef and proceed to claim it
					logger.Info("Host has stale MachineRef pointing to deleted ByoMachine, clearing and proceeding",
						"staleMachineRef", latestHost.Status.MachineRef.Name, "byohost", latestHost.Name)

					// Create a helper to patch the host
					hostHelper, patchErr := patch.NewHelper(latestHost, r.Client)
					if patchErr == nil {
						latestHost.Status.MachineRef = nil
						if patchErr := hostHelper.Patch(ctx, latestHost); patchErr != nil {
							logger.Error(patchErr, "failed to clear stale MachineRef", "byohost", latestHost.Name)
						}
					}

					// Re-fetch the host from API server to get the latest version
					// This is necessary because tryAcquireLease uses Update which requires current ResourceVersion
					if err := r.Client.Get(ctx, client.ObjectKey{Namespace: latestHost.Namespace, Name: latestHost.Name}, latestHost); err != nil {
						logger.Error(err, "failed to re-fetch byohost after clearing stale machineRef", "byohost", latestHost.Name)
						time.Sleep(exponentialBackoff(attempt))
						continue
					}
					// Proceed to claim this host
				} else {
					// Error checking ByoMachine, skip this host
					logger.Error(err, "failed to check existing ByoMachine, trying another host", "byohost", latestHost.Name)
					time.Sleep(exponentialBackoff(attempt))
					continue
				}
			} else if existingMachine.DeletionTimestamp.IsZero() {
				// Referenced ByoMachine still exists and is not being deleted
				logger.Info("Host already claimed by another machine, trying another host", "byohost", latestHost.Name)
				time.Sleep(exponentialBackoff(attempt))
				continue
			}
			// If we reach here, either:
			// 1. The referenced ByoMachine was deleted (we cleared the MachineRef above)
			// 2. The referenced ByoMachine is being deleted (shouldn't happen, but we proceed anyway)
			// We continue to try claiming this host
		}

		// Try to acquire lease on this host
		leaseAcquired, err := r.tryAcquireLease(ctx, latestHost, machineScope.ByoMachine.Name, controllerID)
		if err != nil {
			logger.Error(err, "failed to acquire lease", "byohost", latestHost.Name)
			// Wait with exponential backoff before trying another host
			time.Sleep(exponentialBackoff(attempt))
			continue
		}

		if !leaseAcquired {
			logger.Info("Lease held by another controller, trying another host", "byohost", latestHost.Name)
			// Wait with exponential backoff before trying another host
			time.Sleep(exponentialBackoff(attempt))
			continue
		}

		// Lease acquired successfully, now try to claim the host
		byohostHelper, err := patch.NewHelper(latestHost, r.Client)
		if err != nil {
			logger.Error(err, "Creating patch helper failed", "byohost", latestHost.Name)
			// Release the lease before retrying
			_ = r.releaseLease(ctx, latestHost)
			time.Sleep(exponentialBackoff(attempt))
			continue
		}

		// Set MachineRef
		latestHost.Status.MachineRef = &corev1.ObjectReference{
			APIVersion: machineScope.ByoMachine.APIVersion,
			Kind:       machineScope.ByoMachine.Kind,
			Namespace:  machineScope.ByoMachine.Namespace,
			Name:       machineScope.ByoMachine.Name,
			UID:        machineScope.ByoMachine.UID,
		}
		// Set the cluster Label
		hostLabels := latestHost.Labels
		if hostLabels == nil {
			hostLabels = make(map[string]string)
		}
		hostLabels[clusterv1.ClusterNameLabel] = clusterName
		hostLabels[infrav1.AttachedByoMachineLabel] = machineScope.ByoMachine.Namespace + "." + machineScope.ByoMachine.Name
		latestHost.Labels = hostLabels

		// For TLS Bootstrap mode, create and use the TLS bootstrap secret
		if machineScope.ByoMachine.Spec.JoinMode == infrav1.JoinModeTLSBootstrap {
			tlsBootstrapSecret, err := r.createBootstrapSecretTLSBootstrap(ctx, machineScope)
			if err != nil {
				logger.Error(err, "failed to create TLS bootstrap secret")
				// Release the lease before returning
				_ = r.releaseLease(ctx, latestHost)
				return ctrl.Result{}, err
			}
			latestHost.Spec.BootstrapSecret = &corev1.ObjectReference{
				Kind:      "Secret",
				Namespace: tlsBootstrapSecret.Namespace,
				Name:      tlsBootstrapSecret.Name,
			}
			logger.Info("Using TLS bootstrap secret", "secret", tlsBootstrapSecret.Name)
		} else {
			// For kubeadm mode, use the original bootstrap secret
			latestHost.Spec.BootstrapSecret = &corev1.ObjectReference{
				Kind:      "Secret",
				Namespace: machineScope.ByoMachine.Namespace,
				Name:      *machineScope.Machine.Spec.Bootstrap.DataSecretName,
			}
		}

		// Sync JoinMode from ByoMachine to ByoHost
		latestHost.Spec.JoinMode = machineScope.ByoMachine.Spec.JoinMode

		// Sync DownloadMode from ByoMachine to ByoHost (only for TLSBootstrap mode)
		latestHost.Spec.DownloadMode = machineScope.ByoMachine.Spec.DownloadMode

		// Sync KubernetesVersion from ByoMachine to ByoHost
		latestHost.Spec.KubernetesVersion = machineScope.ByoMachine.Spec.KubernetesVersion

		// Sync ManageKubeProxy from ByoMachine to ByoHost (only for TLSBootstrap mode)
		// For TLSBootstrap mode, default to true if not explicitly set
		manageKubeProxy := machineScope.ByoMachine.Spec.ManageKubeProxy
		if machineScope.ByoMachine.Spec.JoinMode == infrav1.JoinModeTLSBootstrap && !manageKubeProxy {
			manageKubeProxy = true
		}
		latestHost.Spec.ManageKubeProxy = manageKubeProxy

		if latestHost.Annotations == nil {
			latestHost.Annotations = make(map[string]string)
		}
		latestHost.Annotations[infrav1.EndPointIPAnnotation] = machineScope.Cluster.Spec.ControlPlaneEndpoint.Host
		// Safely extract Kubernetes version, handling nil Machine.Spec.Version
		if machineScope.Machine.Spec.Version != nil {
			latestHost.Annotations[infrav1.K8sVersionAnnotation] = strings.Split(*machineScope.Machine.Spec.Version, "+")[0]
		}
		latestHost.Annotations[infrav1.BundleLookupBaseRegistryAnnotation] = machineScope.ByoCluster.Spec.BundleLookupBaseRegistry

		err = byohostHelper.Patch(ctx, latestHost)
		if err != nil {
			logger.Error(err, "failed to patch byohost, will retry", "byohost", latestHost.Name)
			// Release the lease before retrying
			_ = r.releaseLease(ctx, latestHost)
			// Wait with exponential backoff before trying another host
			time.Sleep(exponentialBackoff(attempt))
			continue
		}

		// Successfully attached the host, release the lease
		err = r.releaseLease(ctx, latestHost)
		if err != nil {
			logger.Error(err, "failed to release lease", "byohost", latestHost.Name)
			// Don't return error here, as the host is already claimed successfully
		}
		logger.Info("Successfully attached Byohost", "byohost", latestHost.Name)
		machineScope.ByoHost = latestHost
		return ctrl.Result{}, nil
	}

	logger.Error(nil, "failed to attach byohost after all retries")
	return ctrl.Result{RequeueAfter: RequeueForbyohost}, errors.New("failed to attach byohost after all retries")
}

// ByoHostToByoMachineMapFunc returns a handler.ToRequestsFunc that watches for
// Machine events and returns reconciliation requests for an infrastructure provider object
func ByoHostToByoMachineMapFunc(gvk schema.GroupVersionKind) handler.MapFunc {
	return func(o client.Object) []reconcile.Request {
		h, ok := o.(*infrav1.ByoHost)
		if !ok {
			return nil
		}
		if h.Status.MachineRef == nil {
			// TODO, we can enqueue byomachine which providerID is nil to get better performance than requeue
			return nil
		}

		gk := gvk.GroupKind()
		// Return early if the GroupKind doesn't match what we expect
		byomachineGK := h.Status.MachineRef.GroupVersionKind().GroupKind()
		if gk != byomachineGK {
			return nil
		}

		return []reconcile.Request{
			{
				NamespacedName: client.ObjectKey{
					Namespace: h.Status.MachineRef.Namespace,
					Name:      h.Status.MachineRef.Name,
				},
			},
		}
	}
}

func (r *ByoMachineReconciler) markHostForCleanup(ctx context.Context, machineScope *byoMachineScope) error {
	helper, _ := patch.NewHelper(machineScope.ByoHost, r.Client)

	if machineScope.ByoHost.Annotations == nil {
		machineScope.ByoHost.Annotations = map[string]string{}
	}
	machineScope.ByoHost.Annotations[infrav1.HostCleanupAnnotation] = ""

	// Immediately clear the MachineRef to signal the Agent that the host is being released
	// This is critical for scale-down scenarios where the Node needs to be deleted
	machineScope.ByoHost.Status.MachineRef = nil

	// Issue the patch for byohost
	return helper.Patch(ctx, machineScope.ByoHost)
}

func (r *ByoMachineReconciler) getInstallerConfig(ctx context.Context, byoMachine *infrav1.ByoMachine) (*unstructured.Unstructured, error) {
	installerConfig := &unstructured.Unstructured{}
	gvk := byoMachine.Spec.InstallerRef.GroupVersionKind()
	gvk.Kind = strings.Replace(gvk.Kind, "Template", "", -1)
	installerConfig.SetGroupVersionKind(gvk)
	installerConfigName := client.ObjectKey{
		Namespace: byoMachine.Namespace,
		Name:      byoMachine.Name,
	}
	if err := r.Client.Get(ctx, installerConfigName, installerConfig); err != nil {
		return nil, err
	}
	return installerConfig, nil
}

func (r *ByoMachineReconciler) createInstallerConfig(ctx context.Context, machineScope *byoMachineScope) error {
	logger := log.FromContext(ctx).WithValues("cluster", machineScope.Cluster.Name)
	var (
		installerConfig *unstructured.Unstructured
		err             error
	)
	_, err = r.getInstallerConfig(ctx, machineScope.ByoMachine)
	if err != nil && apierrors.IsNotFound(err) {
		template := &unstructured.Unstructured{}
		template.SetGroupVersionKind(machineScope.ByoMachine.Spec.InstallerRef.GroupVersionKind())
		installerTemplateName := client.ObjectKey{
			Namespace: machineScope.ByoMachine.Spec.InstallerRef.Namespace,
			Name:      machineScope.ByoMachine.Spec.InstallerRef.Name,
		}
		if err = r.Client.Get(ctx, installerTemplateName, template); err != nil {
			logger.Error(err, "failed to get installer config template")
			return err
		}
		installerAnnotations := map[string]string{
			infrav1.K8sVersionAnnotation: strings.Split(*machineScope.Machine.Spec.Version, "+")[0],
		}
		// Propagate proxy annotations from ByoCluster
		for k, v := range machineScope.ByoCluster.Annotations {
			if strings.HasPrefix(k, "infrastructure.cluster.x-k8s.io/http-proxy") ||
				strings.HasPrefix(k, "infrastructure.cluster.x-k8s.io/https-proxy") ||
				strings.HasPrefix(k, "infrastructure.cluster.x-k8s.io/no-proxy") {
				installerAnnotations[k] = v
			}
		}
		// Add scale-from-zero capacity annotations to Machine
		// Capacity annotations should be set on MachineDeployment/MachineSet
		// CAPI will propagate them to Machine.Annotations automatically
		// Only log if not present, don't set empty values
		if machineScope.Machine.Annotations == nil {
			machineScope.Machine.Annotations = map[string]string{}
		}
		if machineScope.Machine.Annotations[infrav1.CapacityCPUAnnotation] == "" {
			logger.V(4).Info("Capacity annotations not set on Machine, autoscaler will use default values")
		}
		if machineScope.Machine.Annotations[infrav1.CapacityMemoryAnnotation] == "" {
			logger.V(4).Info("Memory capacity annotation not set on Machine, autoscaler will use default values")
		}
		installerConfig, err = external.GenerateTemplate(&external.GenerateTemplateInput{
			Template:    template,
			TemplateRef: machineScope.ByoMachine.Spec.InstallerRef,
			Namespace:   machineScope.ByoMachine.Namespace,
			Annotations: installerAnnotations,
			ClusterName: machineScope.Cluster.Name,
			OwnerRef:    metav1.NewControllerRef(machineScope.ByoMachine, machineScope.ByoMachine.GroupVersionKind()),
		})
		if err != nil {
			return err
		} else {
			installerConfig.SetName(machineScope.ByoMachine.Name)
			if err = r.Client.Create(ctx, installerConfig); err != nil {
				logger.Error(err, "failed to create installer config")
				return err
			}
		}
	} else if err != nil {
		logger.Error(err, "failed to get installer config")
		return err
	}
	return nil
}

// convertNetworkToAddresses converts ByoHost.Network status to MachineAddress slice.
// IP addresses are mapped to MachineExternalIP (both IPv4 and IPv6).
// Network interface names are not mapped as there is no suitable MachineAddressType.
func (r *ByoMachineReconciler) convertNetworkToAddresses(network []infrav1.NetworkStatus) []clusterv1.MachineAddress {
	var addresses []clusterv1.MachineAddress
	for _, net := range network {
		for _, ip := range net.IPAddrs {
			addresses = append(addresses, clusterv1.MachineAddress{
				Type:    clusterv1.MachineExternalIP,
				Address: ip,
			})
		}
	}
	return addresses
}

// createBootstrapSecretTLSBootstrap creates a bootstrap secret for TLS Bootstrap mode.
// This secret contains the CA certificate and bootstrap kubeconfig that the Agent
// uses to connect to the cluster and perform TLS bootstrapping.
func (r *ByoMachineReconciler) createBootstrapSecretTLSBootstrap(ctx context.Context, machineScope *byoMachineScope) (*corev1.Secret, error) {
	logger := log.FromContext(ctx).WithValues("cluster", machineScope.Cluster.Name)
	logger.Info("Creating TLS Bootstrap secret")

	var caData []byte
	var bootstrapKubeconfigData []byte

	// Method 1: Check if ByoMachineSpec.BootstrapConfigRef is specified
	if machineScope.ByoMachine.Spec.BootstrapConfigRef != nil {
		bkc := &infrav1.BootstrapKubeconfig{}
		if err := r.Client.Get(ctx, client.ObjectKey{
			Namespace: machineScope.ByoMachine.Spec.BootstrapConfigRef.Namespace,
			Name:      machineScope.ByoMachine.Spec.BootstrapConfigRef.Name,
		}, bkc); err != nil {
			logger.Error(err, "failed to get BootstrapKubeconfig from spec.bootstrapConfigRef")
		} else if bkc.Status.BootstrapKubeconfigData != nil && len(*bkc.Status.BootstrapKubeconfigData) > 0 {
			bootstrapKubeconfigData = []byte(*bkc.Status.BootstrapKubeconfigData)
			if caData == nil {
				caData = extractCAFromKubeconfig(bootstrapKubeconfigData)
			}
			logger.Info("Found BootstrapKubeconfig from spec.bootstrapConfigRef", "name", bkc.Name)
		}
	}

	// Method 2: If still no data, try to get from BootstrapKubeconfig list (for backward compatibility)
	// BUT only for non-TLS-Bootstrap mode or if explicitly required.
	// For TLS Bootstrap mode, we ALWAYS generate a new bootstrap token to ensure fresh credentials.
	if len(bootstrapKubeconfigData) == 0 {
		// Skip finding existing BootstrapKubeconfig for TLS Bootstrap mode
		// Always generate a new token for security and to avoid stale token issues
		if machineScope.ByoMachine.Spec.JoinMode != infrav1.JoinModeTLSBootstrap {
			bootstrapKubeconfigList := &infrav1.BootstrapKubeconfigList{}
			if err := r.Client.List(ctx, bootstrapKubeconfigList, client.InNamespace(machineScope.ByoMachine.Namespace)); err != nil {
				logger.Error(err, "failed to list BootstrapKubeconfig objects")
			} else {
				for _, bkc := range bootstrapKubeconfigList.Items {
					if bkc.Status.BootstrapKubeconfigData != nil && len(*bkc.Status.BootstrapKubeconfigData) > 0 {
						bootstrapKubeconfigData = []byte(*bkc.Status.BootstrapKubeconfigData)
						if caData == nil {
							caData = extractCAFromKubeconfig(bootstrapKubeconfigData)
						}
						logger.Info("Found BootstrapKubeconfig with data", "name", bkc.Name)
						break
					}
				}
			}
		} else {
			logger.V(4).Info("Skipping existing BootstrapKubeconfig for TLS Bootstrap mode, will generate new token")
		}
	}

	// Method 3: If still no data, try to get from the existing bootstrap secret
	if len(bootstrapKubeconfigData) == 0 && machineScope.Machine.Spec.Bootstrap.DataSecretName != nil {
		bootstrapSecret := &corev1.Secret{}
		if err := r.Client.Get(ctx, client.ObjectKey{
			Namespace: machineScope.ByoMachine.Namespace,
			Name:      *machineScope.Machine.Spec.Bootstrap.DataSecretName,
		}, bootstrapSecret); err != nil {
			logger.Error(err, "failed to get bootstrap secret")
		} else {
			// Check for pre-existing bootstrap-kubeconfig
			if data, ok := bootstrapSecret.Data["bootstrap-kubeconfig"]; ok && len(data) > 0 {
				bootstrapKubeconfigData = data
				if caData == nil {
					caData = extractCAFromKubeconfig(data)
				}
				logger.Info("Found bootstrap-kubeconfig in bootstrap secret")
			}

			// Check for CA certificate directly
			if caData == nil {
				if data, ok := bootstrapSecret.Data["ca.crt"]; ok && len(data) > 0 {
					caData = data
					logger.Info("Found ca.crt in bootstrap secret")
				}
			}

			// Try to extract CA from the cloud-init value
			if caData == nil {
				if data, ok := bootstrapSecret.Data["value"]; ok && len(data) > 0 {
					caData = extractCAFromCloudInit(string(data))
					if caData != nil {
						logger.Info("Extracted CA from cloud-init script")
					}
				}
			}
		}
	}

	// Method 3: For TLS Bootstrap mode with external clusters, generate bootstrap kubeconfig
	// from the local cluster (where this controller is running)
	// Only generate if BOTH caData and bootstrapKubeconfigData are nil
	// This prevents overriding data already obtained from BootstrapKubeconfig in Method 2
	var generatedTokenStr string
	if caData == nil && bootstrapKubeconfigData == nil {
		logger.V(4).Info("Generating bootstrap kubeconfig from local cluster for TLS Bootstrap mode")

		// Get the API server endpoint from ByoHost annotation
		apiServerEndpoint := "https://127.0.0.1:6443"
		if machineScope.ByoHost != nil {
			if endpointIP, ok := machineScope.ByoHost.Annotations[infrav1.EndPointIPAnnotation]; ok && endpointIP != "" {
				apiServerEndpoint = "https://" + endpointIP + ":6443"
				logger.V(4).Info("Using API server endpoint from ByoHost annotation", "endpoint", apiServerEndpoint)
			}
		}

		// Get the in-cluster config to create a bootstrap kubeconfig
		restConfig, err := clientcmd.DefaultClientConfig.ClientConfig()
		if err == nil {
			bootstrapKubeconfigContent, tokenStr, err := generateBootstrapKubeconfigWithToken(ctx, restConfig, r.Client, apiServerEndpoint)
			if err == nil {
				logger.Info("Generated bootstrap kubeconfig with new bootstrap token")
				bootstrapKubeconfigData = []byte(bootstrapKubeconfigContent)
				generatedTokenStr = tokenStr

				// Extract CA from the generated kubeconfig
				if caData == nil {
					caData = extractCAFromKubeconfig(bootstrapKubeconfigData)
				}
			} else {
				logger.V(4).Info("Failed to generate bootstrap kubeconfig", "error", err)
			}
		} else {
			logger.V(4).Info("Could not get rest config, trying alternative methods")
		}
	}

	// Validate that we have at least some data
	if len(caData) == 0 && len(bootstrapKubeconfigData) == 0 {
		return nil, fmt.Errorf("failed to obtain CA certificate or bootstrap kubeconfig for TLS Bootstrap mode")
	}

	logger.Info("Creating TLS Bootstrap secret",
		"hasCA", len(caData) > 0,
		"hasKubeconfig", len(bootstrapKubeconfigData) > 0)

	// Create the TLS bootstrap secret
	tlsBootstrapSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      machineScope.ByoMachine.Name + "-tls-bootstrap",
			Namespace: machineScope.ByoMachine.Namespace,
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion:         machineScope.ByoMachine.APIVersion,
					Kind:               machineScope.ByoMachine.Kind,
					Name:               machineScope.ByoMachine.Name,
					UID:                machineScope.ByoMachine.UID,
					BlockOwnerDeletion: func(b bool) *bool { return &b }(true),
					Controller:         func(b bool) *bool { return &b }(true),
				},
			},
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{},
	}

	if len(caData) > 0 {
		tlsBootstrapSecret.Data["ca.crt"] = caData
	}
	if len(bootstrapKubeconfigData) > 0 {
		tlsBootstrapSecret.Data["bootstrap-kubeconfig"] = bootstrapKubeconfigData
	}

	// Try to fetch additional configurations (kubelet-config, kube-proxy)
	// Priority 1: Fetch from target cluster (emulate kubeadm sync)
	// This ensures we use the EXACT config that kubeadm would download
	remoteClient, err := r.getRemoteClient(ctx, machineScope.ByoMachine)
	if err == nil {
		// Try to get kubelet-config ConfigMap (kube-system/kubelet-config-1.x)
		// We try a few versions since we don't know the exact minor version
		// Or we can try to guess from the machine version
		k8sVersion := *machineScope.Machine.Spec.Version
		// Normalize version (e.g. v1.22.2 -> 1.22)
		re := regexp.MustCompile(`v?(\d+\.\d+)`)
		match := re.FindStringSubmatch(k8sVersion)
		if len(match) > 1 {
			shortVer := match[1]
			configMapName := fmt.Sprintf("kubelet-config-%s", shortVer)
			cm := &corev1.ConfigMap{}
			if err := remoteClient.Get(ctx, client.ObjectKey{Namespace: "kube-system", Name: configMapName}, cm); err == nil {
				if data, ok := cm.Data["kubelet"]; ok {
					tlsBootstrapSecret.Data["kubelet-config.yaml"] = []byte(data)
					logger.Info("Fetched kubelet-config from target cluster", "configMap", configMapName)
				}
			} else {
				logger.V(4).Info("Could not fetch kubelet-config from target cluster, trying fallback", "configMap", configMapName, "error", err)
				// Fallback: Try unversioned "kubelet-config"
				if err := remoteClient.Get(ctx, client.ObjectKey{Namespace: "kube-system", Name: "kubelet-config"}, cm); err == nil {
					if data, ok := cm.Data["kubelet"]; ok {
						tlsBootstrapSecret.Data["kubelet-config.yaml"] = []byte(data)
						logger.Info("Fetched unversioned kubelet-config from target cluster")
					}
				} else {
					// Fallback: Generate a default kubelet-config if none exists
					// This is common for non-kubeadm (binary) clusters
					logger.Info("No kubelet-config ConfigMap found in target cluster, generating default")

					// Try to detect CoreDNS ClusterIP to set correct clusterDNS
					var detectedClusterDNS string

					// Priority 1: Check for NodeLocal DNS Cache (nodelocaldns)
					// If present, it usually runs as a DaemonSet and listens on a link-local IP (e.g., 169.254.20.10)
					// or a cluster IP. We need to find the listening IP from arguments.
					dsList := &appsv1.DaemonSetList{}
					if err := remoteClient.List(ctx, dsList, client.InNamespace("kube-system")); err == nil {
						for _, ds := range dsList.Items {
							if ds.Name == "node-local-dns" || ds.Name == "nodelocaldns" {
								// Parse arguments to find -localip
								for _, container := range ds.Spec.Template.Spec.Containers {
									for i, arg := range container.Args {
										if arg == "-localip" && i+1 < len(container.Args) {
											// The next argument is the IP(s)
											ips := strings.Split(container.Args[i+1], ",")
											if len(ips) > 0 {
												detectedClusterDNS = strings.TrimSpace(ips[0])
												logger.Info("Detected NodeLocal DNS", "ip", detectedClusterDNS)
											}
										}
									}
								}
								if detectedClusterDNS != "" {
									break
								}
							}
						}
					}

					// Priority 2: Check standard Services if NodeLocal DNS not found
					if detectedClusterDNS == "" {
						coreDNSSvc := &corev1.Service{}
						// Try standard kube-system/kube-dns
						if err := remoteClient.Get(ctx, client.ObjectKey{Namespace: "kube-system", Name: "kube-dns"}, coreDNSSvc); err == nil {
							if len(coreDNSSvc.Spec.ClusterIP) > 0 {
								detectedClusterDNS = coreDNSSvc.Spec.ClusterIP
								logger.Info("Detected clusterDNS from kube-dns Service", "ip", detectedClusterDNS)
							}
						}
						// If not found, try coredns
						if detectedClusterDNS == "" {
							if err := remoteClient.Get(ctx, client.ObjectKey{Namespace: "kube-system", Name: "coredns"}, coreDNSSvc); err == nil {
								if len(coreDNSSvc.Spec.ClusterIP) > 0 {
									detectedClusterDNS = coreDNSSvc.Spec.ClusterIP
									logger.Info("Detected clusterDNS from coredns Service", "ip", detectedClusterDNS)
								}
							}
						}
					}

					defaultConfig := generateDefaultKubeletConfig(machineScope.Cluster, detectedClusterDNS)
					tlsBootstrapSecret.Data["kubelet-config.yaml"] = []byte(defaultConfig)
				}
			}
		}

		// Try to get kube-proxy ConfigMap (kube-system/kube-proxy)
		cmProxy := &corev1.ConfigMap{}
		if err := remoteClient.Get(ctx, client.ObjectKey{Namespace: "kube-system", Name: "kube-proxy"}, cmProxy); err == nil {
			// kube-proxy configmap usually has config.conf or config.yaml
			if data, ok := cmProxy.Data["config.conf"]; ok {
				tlsBootstrapSecret.Data["kube-proxy-config.yaml"] = []byte(data)
				logger.Info("Fetched kube-proxy config from target cluster")
			} else if data, ok := cmProxy.Data["config.yaml"]; ok {
				tlsBootstrapSecret.Data["kube-proxy-config.yaml"] = []byte(data)
				logger.Info("Fetched kube-proxy config from target cluster")
			}
			// Also fetch kubeconfig for kube-proxy if possible (usually in a secret or same configmap?)
			// Kubeadm puts kube-proxy.kubeconfig in a ConfigMap "kube-proxy" as well? No, usually it's generated.
			// But for BYOH, we might need to rely on the bootstrap secret for the kubeconfig part.
		} else {
			// Fallback: Generate default kube-proxy config
			logger.Info("No kube-proxy ConfigMap found, generating default")
			defaultProxyConfig := generateDefaultKubeProxyConfig(machineScope.Cluster)
			tlsBootstrapSecret.Data["kube-proxy-config.yaml"] = []byte(defaultProxyConfig)
		}
	} else {
		logger.Info("Could not get remote client to fetch configs", "error", err)
	}

	// Priority 2: Fallback to local bootstrap secret (if provided manually)
	if machineScope.Machine.Spec.Bootstrap.DataSecretName != nil {
		bootstrapSecret := &corev1.Secret{}
		if err := r.Client.Get(ctx, client.ObjectKey{
			Namespace: machineScope.ByoMachine.Namespace,
			Name:      *machineScope.Machine.Spec.Bootstrap.DataSecretName,
		}, bootstrapSecret); err == nil {
			// Copy kubelet-config.yaml if present (and not already set)
			if _, ok := tlsBootstrapSecret.Data["kubelet-config.yaml"]; !ok {
				if data, ok := bootstrapSecret.Data["kubelet-config.yaml"]; ok {
					tlsBootstrapSecret.Data["kubelet-config.yaml"] = data
				}
			}
			// Copy kube-proxy.kubeconfig if present
			if data, ok := bootstrapSecret.Data["kube-proxy.kubeconfig"]; ok {
				tlsBootstrapSecret.Data["kube-proxy.kubeconfig"] = data
			}
		}
	}

	// Generate kube-proxy.kubeconfig if not already present
	// Priority: use generated token, or extract from existing bootstrap-kubeconfig
	if _, ok := tlsBootstrapSecret.Data["kube-proxy.kubeconfig"]; !ok {
		var tokenToUse string

		// Priority 1: Use the generated bootstrap token if available
		if generatedTokenStr != "" {
			tokenToUse = generatedTokenStr
		} else if len(bootstrapKubeconfigData) > 0 {
			// Priority 2: Extract token from existing bootstrap-kubeconfig data
			tokenToUse = extractTokenFromBootstrapKubeconfig(string(bootstrapKubeconfigData))
		}

		// Generate kube-proxy.kubeconfig if we have a token
		if tokenToUse != "" {
			kubeProxyKubeconfig := generateKubeProxyKubeconfig(tokenToUse, apiServerEndpoint)
			tlsBootstrapSecret.Data["kube-proxy.kubeconfig"] = []byte(kubeProxyKubeconfig)
			logger.Info("Generated kube-proxy.kubeconfig with bootstrap token")
		}
	}

	if err := r.Client.Create(ctx, tlsBootstrapSecret); err != nil {
		return nil, fmt.Errorf("failed to create TLS bootstrap secret: %w", err)
	}

	logger.Info("Successfully created TLS bootstrap secret", "secret", tlsBootstrapSecret.Name)
	return tlsBootstrapSecret, nil
}

// generateDefaultKubeletConfig generates a default KubeletConfiguration
func generateDefaultKubeletConfig(cluster *clusterv1.Cluster, detectedDNS string) string {
	// Try to derive ClusterDNS from Service CIDR (convention: 10th IP)
	// Default to standard Kubeadm default if not found
	clusterDNS := "10.96.0.10"

	// If we detected a real CoreDNS IP from the cluster, use it!
	if detectedDNS != "" {
		clusterDNS = detectedDNS
	} else if cluster.Spec.ClusterNetwork != nil &&
		cluster.Spec.ClusterNetwork.Services != nil &&
		len(cluster.Spec.ClusterNetwork.Services.CIDRBlocks) > 0 {
		// Calculate standard 10th IP logic or just pick the 10th if it's a standard /12 or /16
		// For robustness, we'll stick to 10.96.0.10 if we can't easily calc,
		// but ideally we should parse the CIDR.
		// For now, using a safe default for standard Kubeadm clusters.
		// If users have custom DNS, they SHOULD provide kubelet-config ConfigMap.
	}

	return fmt.Sprintf(`apiVersion: kubelet.config.k8s.io/v1beta1
kind: KubeletConfiguration
authentication:
  anonymous:
    enabled: false
  webhook:
    cacheTTL: 0s
    enabled: true
  x509:
    clientCAFile: /etc/kubernetes/pki/ca.crt
authorization:
  mode: Webhook
  webhook:
    cacheAuthorizedTTL: 0s
    cacheUnauthorizedTTL: 0s
cgroupDriver: systemd
clusterDNS:
- %s
clusterDomain: cluster.local
containerLogMaxFiles: 5
containerLogMaxSize: 10Mi
contentType: application/vnd.kubernetes.protobuf
cpuManagerReconcilePeriod: 0s
evictionHard:
  imagefs.available: 15%%
  memory.available: 100Mi
  nodefs.available: 10%%
  nodefs.inodesFree: 5%%
evictionPressureTransitionPeriod: 5m0s
fileCheckFrequency: 0s
healthzBindAddress: 127.0.0.1
healthzPort: 10248
httpCheckFrequency: 0s
imageMinimumGCAge: 2m0s
imageGCHighThresholdPercent: 85
imageGCLowThresholdPercent: 80
logging:
  flushFrequency: 0
  text:
    infoBufferSize: "0"
  verbosity: 0
memorySwap: {}
nodeStatusReportFrequency: 0s
nodeStatusUpdateFrequency: 0s
rotateCertificates: true
runtimeRequestTimeout: 0s
shutdownGracePeriod: 0s
shutdownGracePeriodCriticalPods: 0s
staticPodPath: /etc/kubernetes/manifests
streamingConnectionIdleTimeout: 0s
syncFrequency: 0s
volumeStatsAggPeriod: 0s
`, clusterDNS)
}

// generateDefaultKubeProxyConfig generates a default KubeProxyConfiguration
func generateDefaultKubeProxyConfig(cluster *clusterv1.Cluster) string {
	return `apiVersion: kubeproxy.config.k8s.io/v1alpha1
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
`
}

// generateBootstrapKubeconfigWithToken creates a kubeconfig and returns the token used
func generateBootstrapKubeconfigWithToken(ctx context.Context, restConfig *rest.Config, client client.Client, apiServerEndpoint string) (string, string, error) {
	// Generate a new bootstrap token
	tokenStr, err := bootstraputil.GenerateBootstrapToken()
	if err != nil {
		return "", "", fmt.Errorf("failed to generate bootstrap token: %w", err)
	}

	// Create bootstrap token secret
	ttl := time.Minute * 30
	tokenSecret, err := bootstraptoken.GenerateSecretFromBootstrapToken(tokenStr, ttl)
	if err != nil {
		return "", "", fmt.Errorf("failed to create token secret: %w", err)
	}

	// Create the secret in the cluster
	if err := client.Create(ctx, tokenSecret); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			return "", "", fmt.Errorf("failed to create token secret: %w", err)
		}
	}

	// Create a simple kubeconfig YAML structure with the new bootstrap token
	var caData string
	if len(restConfig.CAData) > 0 {
		caData = base64.StdEncoding.EncodeToString(restConfig.CAData)
	} else {
		// Try to read CA from in-cluster service account token
		caPath := "/var/run/secrets/kubernetes.io/serviceaccount/ca.crt"
		if caBytes, err := os.ReadFile(caPath); err == nil {
			caData = base64.StdEncoding.EncodeToString(caBytes)
		}
	}

	kubeconfigYAML := fmt.Sprintf(`apiVersion: v1
kind: Config
clusters:
- cluster:
    certificate-authority-data: %s
    server: %s
  name: bootstrap
contexts:
- context:
    cluster: bootstrap
    user: bootstrap
  name: bootstrap
current-context: bootstrap
users:
- name: bootstrap
  user:
    token: %s
`, caData, apiServerEndpoint, tokenStr)

	return kubeconfigYAML, tokenStr, nil
}

// generateKubeProxyKubeconfig creates a kubeconfig for kube-proxy using the same bootstrap token
func generateKubeProxyKubeconfig(tokenStr, apiServerEndpoint string) string {
	var caData string
	// Try to read CA from service account token
	if caBytes, err := os.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/ca.crt"); err == nil {
		caData = base64.StdEncoding.EncodeToString(caBytes)
	}

	return fmt.Sprintf(`apiVersion: v1
kind: Config
clusters:
- cluster:
    certificate-authority-data: %s
    server: %s
  name: default-cluster
contexts:
- context:
    cluster: default-cluster
    namespace: default
    user: default-auth
  name: default-context
current-context: default-context
users:
- name: default-auth
  user:
    token: %s
`, caData, apiServerEndpoint, tokenStr)
}

// extractCAFromKubeconfig extracts CA data from a kubeconfig file
// Uses proper YAML parsing to extract certificate-authority-data from clusters
func extractCAFromKubeconfig(kubeconfigData []byte) []byte {
	// Define a minimal kubeconfig structure for parsing
	type kubeconfigCluster struct {
		Cluster struct {
			CertificateAuthorityData []byte `yaml:"certificate-authority-data"`
		} `yaml:"cluster"`
	}

	type kubeconfig struct {
		Clusters []kubeconfigCluster `yaml:"clusters"`
	}

	var config kubeconfig
	if err := yaml.Unmarshal(kubeconfigData, &config); err != nil {
		// Fallback to simple extraction if YAML parsing fails
		return extractCAFromKubeconfigSimple(kubeconfigData)
	}

	// Look for certificate-authority-data in any cluster
	for _, cluster := range config.Clusters {
		if len(cluster.Cluster.CertificateAuthorityData) > 0 {
			return cluster.Cluster.CertificateAuthorityData
		}
	}

	return nil
}

// extractCAFromKubeconfigSimple provides a simple fallback extraction method
// for kubeconfig files that may not parse correctly with the structured approach
func extractCAFromKubeconfigSimple(kubeconfigData []byte) []byte {
	dataStr := string(kubeconfigData)
	if !strings.Contains(dataStr, "certificate-authority-data:") {
		return nil
	}

	lines := strings.Split(dataStr, "\n")
	for i, line := range lines {
		if strings.Contains(line, "certificate-authority-data:") && i+1 < len(lines) {
			caBase64 := strings.TrimSpace(lines[i+1])
			// Remove potential quotes and extra whitespace
			caBase64 = strings.Trim(caBase64, "\"'\"")

			if decoded, err := base64.StdEncoding.DecodeString(caBase64); err == nil {
				return decoded
			}
		}
	}
	return nil
}

// extractCAFromCloudInit extracts CA from a cloud-init script
func extractCAFromCloudInit(script string) []byte {
	// Look for CA certificate in various formats in the cloud-init script
	// Pattern 1: echo "<base64>" | base64 -d > /etc/kubernetes/pki/ca.crt
	patterns := []string{
		`ca\.crt["']?\s*:\s*["']?([A-Za-z0-9+/=]+)["']?`,
		`certificate-authority-data["']?\s*:\s*["']?([A-Za-z0-9+/=]+)["']?`,
	}

	for _, pattern := range patterns {
		re := regexp.MustCompile(pattern)
		matches := re.FindStringSubmatch(script)
		if len(matches) > 1 {
			if decoded, err := base64.StdEncoding.DecodeString(matches[1]); err == nil {
				return decoded
			}
		}
	}
	return nil
}

// tryAcquireLease attempts to acquire a lease on the given ByoHost
// Returns true if lease was acquired, false if lease is held by another instance
func (r *ByoMachineReconciler) tryAcquireLease(ctx context.Context, byoHost *infrav1.ByoHost, machineName string, controllerID string) (bool, error) {
	now := time.Now()

	// Check if lease exists and is still valid
	if leaseStr, exists := byoHost.Annotations[HostLeaseAnnotationKey]; exists {
		var currentLock lockInfo
		if err := json.Unmarshal([]byte(leaseStr), &currentLock); err == nil {
			// Check if lease has expired
			if currentLock.AcquireTime.Add(time.Duration(HostLeaseTimeoutSeconds) * time.Second).After(now) {
				// Lease is still valid and held by someone
				return false, nil
			}
		}
	}

	// Try to acquire the lease
	newLock := lockInfo{
		Holder:      controllerID,
		AcquireTime: now,
		MachineName: machineName,
	}
	lockData, err := json.Marshal(newLock)
	if err != nil {
		return false, fmt.Errorf("failed to marshal lock data: %w", err)
	}

	// Use Update to atomically acquire the lease with optimistic locking (ResourceVersion check)
	if byoHost.Annotations == nil {
		byoHost.Annotations = make(map[string]string)
	}
	byoHost.Annotations[HostLeaseAnnotationKey] = string(lockData)

	// We use Update instead of Patch to ensure we don't overwrite if someone else updated the object
	// This relies on ResourceVersion check enforced by the API server
	if err := r.Client.Update(ctx, byoHost); err != nil {
		if apierrors.IsConflict(err) {
			// Optimistic lock failed - someone else updated the object
			return false, nil
		}
		return false, fmt.Errorf("failed to update lease: %w", err)
	}

	return true, nil
}

// releaseLease releases the lease on the given ByoHost
func (r *ByoMachineReconciler) releaseLease(ctx context.Context, byoHost *infrav1.ByoHost) error {
	if byoHost.Annotations == nil {
		return nil
	}

	// Check if our lease exists
	if _, exists := byoHost.Annotations[HostLeaseAnnotationKey]; !exists {
		return nil
	}

	patchHelper, err := patch.NewHelper(byoHost, r.Client)
	if err != nil {
		return fmt.Errorf("failed to create patch helper: %w", err)
	}

	delete(byoHost.Annotations, HostLeaseAnnotationKey)

	if err := patchHelper.Patch(ctx, byoHost); err != nil {
		return fmt.Errorf("failed to release lease: %w", err)
	}

	return nil
}

// exponentialBackoff returns the delay for the nth attempt (0-indexed)
// First attempt: 0ms, Second: 100ms, Third: 200ms, Fourth: 400ms, Fifth: 800ms
func exponentialBackoff(attempt int) time.Duration {
	if attempt == 0 {
		return 0
	}
	return time.Duration(1<<uint(attempt-1)) * 100 * time.Millisecond
}

// selectHostForClaim implements priority-based selection with round-robin for hosts with the same priority
func (r *ByoMachineReconciler) selectHostForClaim(hostsList []infrav1.ByoHost, clusterName string, machine *infrav1.ByoMachine) *infrav1.ByoHost {
	if len(hostsList) == 0 {
		return nil
	}

	// Filter available hosts that match capacity requirements
	var availableHosts []infrav1.ByoHost
	for _, host := range hostsList {
		if !host.IsAvailable() {
			continue
		}

		// Check if host matches capacity requirements
		if machine.Spec.CapacityRequirements != nil {
			if !host.MatchesRequirements(nil, machine.Spec.CapacityRequirements) {
				continue
			}
		}

		availableHosts = append(availableHosts, host)
	}

	if len(availableHosts) == 0 {
		return nil
	}

	// Find the maximum priority among available hosts
	maxPriority := int32(0)
	for _, host := range availableHosts {
		if priority := host.GetPriority(); priority > maxPriority {
			maxPriority = priority
		}
	}

	// Collect hosts with the highest priority
	var highPriorityHosts []infrav1.ByoHost
	for _, host := range availableHosts {
		if host.GetPriority() == maxPriority {
			highPriorityHosts = append(highPriorityHosts, host)
		}
	}

	// Initialize round-robin index for this cluster if not exists
	if r.roundRobinIndex == nil {
		r.roundRobinIndex = make(map[string]int)
	}
	if _, exists := r.roundRobinIndex[clusterName]; !exists {
		r.roundRobinIndex[clusterName] = 0
	}

	// Get current index and return the host (using high priority hosts)
	currentIndex := r.roundRobinIndex[clusterName]
	selectedHost := &highPriorityHosts[currentIndex]

	// Increment index for next selection (wrap around)
	r.roundRobinIndex[clusterName] = (currentIndex + 1) % len(highPriorityHosts)

	// Return the selected host
	return selectedHost
}

// generateProviderID generates a standardized ProviderID for a ByoHost
// This ensures consistency across all injection points (cloud-init, kubelet args, Node objects)
func generateProviderID(host *infrav1.ByoHost) string {
	return common.GenerateProviderID(host.Name)
}

// validateProviderID validates that a ProviderID matches the expected format
func validateProviderID(providerID, hostName string) (bool, error) {
	return common.ValidateProviderID(providerID, hostName)
}

// extractTokenFromBootstrapKubeconfig extracts the bootstrap token from a kubeconfig string
func extractTokenFromBootstrapKubeconfig(kubeconfigContent string) string {
	type kubeconfigAuthInfo struct {
		User struct {
			Token string `yaml:"token"`
		} `yaml:"user"`
	}

	type kubeconfig struct {
		Users []kubeconfigAuthInfo `yaml:"users"`
	}

	var config kubeconfig
	if err := yaml.Unmarshal([]byte(kubeconfigContent), &config); err != nil {
		return ""
	}

	for _, user := range config.Users {
		if len(user.User.Token) > 0 {
			return user.User.Token
		}
	}

	return ""
}
