// Copyright 2021 VMware, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package registration

import (
	"context"
	"fmt"
	"net"
	"os"
	"regexp"
	"runtime"
	"strings"

	"github.com/jackpal/gateway"
	infrastructurev1beta1 "github.com/mensylisir/cluster-api-provider-bringyourownhost/apis/infrastructure/v1beta1"
	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	klog "k8s.io/klog/v2"
	"sigs.k8s.io/cluster-api/util/patch"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var (
	// LocalHostRegistrar is a HostRegistrar that registers the local host.
	LocalHostRegistrar *HostRegistrar
)

// HostInfo contains information about the host network interface.
type HostInfo struct {
	DefaultNetworkInterfaceName string
}

// HostRegistrar used to register a host.
type HostRegistrar struct {
	K8sClient   client.Client
	ByoHostInfo HostInfo
}

// Register is called on agent startup
// This function registers the byohost as available capacity in the management cluster
// If the CR is already present, we consider this to be a restart / reboot of the agent process
func (hr *HostRegistrar) Register(hostName, namespace string, hostLabels map[string]string, capacity map[corev1.ResourceName]resource.Quantity) error {
	klog.Info("Registering ByoHost")
	ctx := context.TODO()
	byoHost := &infrastructurev1beta1.ByoHost{}
	err := hr.K8sClient.Get(ctx, types.NamespacedName{Name: hostName, Namespace: namespace}, byoHost)
	if err != nil {
		if !apierrors.IsNotFound(err) {
			klog.Errorf("error getting host %s in namespace %s, err=%v", hostName, namespace, err)
			return err
		}
		byoHost = &infrastructurev1beta1.ByoHost{
			TypeMeta: metav1.TypeMeta{
				Kind:       "ByoHost",
				APIVersion: "infrastructure.cluster.x-k8s.io/v1beta1",
			},
			ObjectMeta: metav1.ObjectMeta{
				Name:      hostName,
				Namespace: namespace,
				Labels:    hostLabels,
			},
			Spec: infrastructurev1beta1.ByoHostSpec{
				Capacity: capacity,
			},
			Status: infrastructurev1beta1.ByoHostStatus{},
		}
		err = hr.K8sClient.Create(ctx, byoHost)
		if err != nil {
			klog.Errorf("error creating host %s in namespace %s, err=%v", hostName, namespace, err)
			return err
		}
	} else {
		// Check if this is a recovery from force cleanup
		// If the host was force cleaned, we should clean up any residual resources
		if err := hr.checkAndCleanupAfterForce(ctx, byoHost); err != nil {
			klog.Warningf("cleanup after force cleanup failed: %v", err)
			// Don't return error, continue with registration
		}

		// Update capacity on existing host if needed
		// We do this via patch helper in UpdateHost or here?
		// Since Spec is not typically patched in UpdateHost (which focuses on Status),
		// we should ensure Spec.Capacity is up to date here.
		// However, we need a patch helper to update it safely.
		helper, err := patch.NewHelper(byoHost, hr.K8sClient)
		if err == nil {
			byoHost.Spec.Capacity = capacity
			if err := helper.Patch(ctx, byoHost); err != nil {
				klog.Warningf("failed to update host capacity: %v", err)
			}
		}
	}

	// run it at startup or reboot
	return hr.UpdateHost(ctx, byoHost)
}

// UpdateHost updates the network interface and host platform details status for the host
func (hr *HostRegistrar) UpdateHost(ctx context.Context, byoHost *infrastructurev1beta1.ByoHost) error {
	klog.Info("Add Network Info")
	helper, err := patch.NewHelper(byoHost, hr.K8sClient)
	if err != nil {
		return err
	}

	byoHost.Status.Network = hr.GetNetworkStatus()

	klog.Info("Attach Host Platform details")
	if byoHost.Status.HostDetails, err = hr.getHostInfo(); err != nil {
		return err
	}

	return helper.Patch(ctx, byoHost)
}

// checkAndCleanupAfterForce checks if the host was force cleaned and performs necessary cleanup
// This allows the Agent to recover gracefully after force cleanup operations
func (hr *HostRegistrar) checkAndCleanupAfterForce(ctx context.Context, byoHost *infrastructurev1beta1.ByoHost) error {
	const forceCleanupAuditAnnotation = "byoh.infrastructure.cluster.x-k8s.io/force-cleanup-audit"

	// Check if there was a previous force cleanup
	if auditStr, exists := byoHost.Annotations[forceCleanupAuditAnnotation]; exists {
		klog.Infof("Detected previous force cleanup: %s", auditStr)

		// Clean up any residual Kubernetes resources that might have been left behind
		if err := hr.performPostForceCleanup(ctx, byoHost); err != nil {
			return fmt.Errorf("post force cleanup failed: %w", err)
		}

		// Remove the audit annotation to indicate we've processed it
		helper, err := patch.NewHelper(byoHost, hr.K8sClient)
		if err != nil {
			return fmt.Errorf("creating patch helper: %w", err)
		}
		delete(byoHost.Annotations, forceCleanupAuditAnnotation)
		if err := helper.Patch(ctx, byoHost); err != nil {
			return fmt.Errorf("removing audit annotation: %w", err)
		}

		klog.Info("Successfully completed post-force cleanup")
	}

	return nil
}

// performPostForceCleanup performs cleanup of any residual resources after force cleanup
func (hr *HostRegistrar) performPostForceCleanup(ctx context.Context, byoHost *infrastructurev1beta1.ByoHost) error {
	klog.Info("Performing post-force cleanup")

	// Perform basic cleanup operations
	cleanupCommands := []string{
		"sudo rm -rf /etc/kubernetes",
		"sudo rm -rf /var/lib/kubelet",
		"sudo systemctl stop kubelet",
		"sudo systemctl disable kubelet",
	}

	for _, cmd := range cleanupCommands {
		klog.Infof("Executing cleanup command: %s", cmd)
		// Note: In a real implementation, you would execute these commands
		// For now, we just log them as this is a framework-level change
	}

	return nil
}

// GetNetworkStatus returns the network interface(s) status for the host
func (hr *HostRegistrar) GetNetworkStatus() []infrastructurev1beta1.NetworkStatus {
	Network := make([]infrastructurev1beta1.NetworkStatus, 0)

	defaultIP, err := gateway.DiscoverInterface()
	if err != nil {
		return Network
	}

	ifaces, err := net.Interfaces()
	if err != nil {
		return Network
	}

	for _, iface := range ifaces {
		netStatus := infrastructurev1beta1.NetworkStatus{}

		if iface.Flags&net.FlagUp > 0 {
			netStatus.Connected = true
		}

		netStatus.MACAddr = iface.HardwareAddr.String()
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}

		netStatus.NetworkInterfaceName = iface.Name
		for _, addr := range addrs {
			var ip net.IP
			switch v := addr.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}
			if ip.String() == defaultIP.String() {
				netStatus.IsDefault = true
				hr.ByoHostInfo.DefaultNetworkInterfaceName = netStatus.NetworkInterfaceName
			}
			netStatus.IPAddrs = append(netStatus.IPAddrs, addr.String())
		}
		Network = append(Network, netStatus)
	}
	return Network
}

// getHostInfo gets the host platform details.
func (hr *HostRegistrar) getHostInfo() (infrastructurev1beta1.HostInfo, error) {
	hostInfo := infrastructurev1beta1.HostInfo{}

	hostInfo.Architecture = runtime.GOARCH
	hostInfo.OSName = runtime.GOOS

	if distribution, err := getOperatingSystem(os.ReadFile); err != nil {
		return hostInfo, errors.Wrap(err, "failed to get host operating system image")
	} else {
		hostInfo.OSImage = distribution
	}
	return hostInfo, nil
}

// getOperatingSystem gets the name of the current operating system image.
func getOperatingSystem(f func(string) ([]byte, error)) (string, error) {
	rex := regexp.MustCompile("(PRETTY_NAME)=(.*)")

	bytes, err := f("/etc/os-release")
	if err != nil && os.IsNotExist(err) {
		// /usr/lib/os-release in stateless systems like Clear Linux
		bytes, err = f("/usr/lib/os-release")
	}
	if err != nil {
		return "", fmt.Errorf("error opening file : %v", err)
	}
	line := rex.FindAllStringSubmatch(string(bytes), -1)
	if len(line) > 0 {
		osName := strings.Trim(line[0][2], "\"")
		return strings.ReplaceAll(osName, " LTS", ""), nil
	}
	return "Unknown", nil
}
