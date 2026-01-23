// Copyright 2024 VMware, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"k8s.io/klog/v2"
)

// StartDriftDetector starts the periodic drift detection loop
func StartDriftDetector(interval time.Duration) {
	klog.Info("Starting Drift Detector")
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for range ticker.C {
			checkAndRemediate()
		}
	}()
}

func checkAndRemediate() {
	checkSwap()
	checkKernelModules()
	checkIPForwarding()
	checkServices()
	checkSysctl()
}

func checkSysctl() {
	configPath := "/etc/byoh/sysctl.conf"
	data, err := os.ReadFile(configPath)
	if err != nil {
		if !os.IsNotExist(err) {
			klog.Errorf("Drift: Failed to read %s: %v", configPath, err)
		}
		return
	}

	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		val := strings.TrimSpace(parts[1])

		// Check current value
		// key "net.ipv4.ip_forward" -> path "/proc/sys/net/ipv4/ip_forward"
		procPath := "/proc/sys/" + strings.ReplaceAll(key, ".", "/")
		currentVal, err := os.ReadFile(procPath)
		if err != nil {
			klog.Warningf("Drift: Failed to read sysctl %s: %v", key, err)
			continue
		}

		if strings.TrimSpace(string(currentVal)) != val {
			klog.Infof("Drift: Sysctl drift detected for %s. Expected %s, got %s. Remediating...", key, val, strings.TrimSpace(string(currentVal)))
			if err := exec.Command("sysctl", "-w", fmt.Sprintf("%s=%s", key, val)).Run(); err != nil {
				klog.Errorf("Drift: Failed to set sysctl %s: %v", key, err)
			}
		}
	}
}

func checkSwap() {
	// Check /proc/swaps
	data, err := os.ReadFile("/proc/swaps")
	if err != nil {
		klog.Errorf("Drift: Failed to read /proc/swaps: %v", err)
		return
	}
	// If there is more than just the header line, swap is enabled
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) > 1 {
		klog.Warning("Drift: Swap detected enabled. Remediating...")
		if err := exec.Command("swapoff", "-a").Run(); err != nil {
			klog.Errorf("Drift: Failed to disable swap: %v", err)
		} else {
			klog.Info("Drift: Swap disabled successfully")
		}
	}
}

func checkKernelModules() {
	modules := []string{"overlay", "br_netfilter"}
	// Simple check: try to load them. If already loaded, it does nothing.
	for _, mod := range modules {
		if err := exec.Command("modprobe", mod).Run(); err != nil {
			klog.Errorf("Drift: Failed to load kernel module %s: %v", mod, err)
		}
	}
}

func checkIPForwarding() {
	path := "/proc/sys/net/ipv4/ip_forward"
	data, err := os.ReadFile(path)
	if err != nil {
		klog.Errorf("Drift: Failed to read %s: %v", path, err)
		return
	}
	if strings.TrimSpace(string(data)) != "1" {
		klog.Warning("Drift: IP forwarding disabled. Remediating...")
		if err := os.WriteFile(path, []byte("1"), 0644); err != nil {
			klog.Errorf("Drift: Failed to enable IP forwarding: %v", err)
		} else {
			klog.Info("Drift: IP forwarding enabled successfully")
		}
	}
}

func checkServices() {
	services := []string{"containerd", "kubelet"}
	for _, svc := range services {
		// Check if active
		if err := exec.Command("systemctl", "is-active", "--quiet", svc).Run(); err != nil {
			klog.Warningf("Drift: Service %s is not active. Remediating...", svc)
			if err := exec.Command("systemctl", "start", svc).Run(); err != nil {
				klog.Errorf("Drift: Failed to start service %s: %v", svc, err)
			} else {
				klog.Infof("Drift: Service %s started successfully", svc)
			}
		}
	}
}
