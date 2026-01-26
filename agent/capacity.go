// Copyright 2024 VMware, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bufio"
	"os"
	"runtime"
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/klog/v2"
)

// GetCapacity detects the host's resources (CPU, Memory, GPU) and returns them as a ResourceList.
func GetCapacity() map[corev1.ResourceName]resource.Quantity {
	capacity := make(map[corev1.ResourceName]resource.Quantity)

	// CPU
	numCPU := runtime.NumCPU()
	capacity[corev1.ResourceCPU] = *resource.NewQuantity(int64(numCPU), resource.DecimalSI)

	// Memory
	memBytes, err := getMemoryBytes()
	if err != nil {
		klog.Errorf("Failed to get memory info: %v", err)
	} else {
		capacity[corev1.ResourceMemory] = *resource.NewQuantity(memBytes, resource.BinarySI)
	}

	// GPU
	gpuInfo := GetGPUInfo()
	if gpuInfo.Present && gpuInfo.Count > 0 {
		capacity["nvidia.com/gpu"] = *resource.NewQuantity(int64(gpuInfo.Count), resource.DecimalSI)
	}

	return capacity
}

// getMemoryBytes reads MemTotal from /proc/meminfo and returns bytes
func getMemoryBytes() (int64, error) {
	file, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0, err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "MemTotal:") {
			// Format: MemTotal:        16326656 kB
			parts := strings.Fields(line)
			if len(parts) >= 2 {
				kbStr := parts[1]
				kb, err := strconv.ParseInt(kbStr, 10, 64)
				if err != nil {
					return 0, err
				}
				return kb * 1024, nil
			}
		}
	}
	return 0, nil
}
