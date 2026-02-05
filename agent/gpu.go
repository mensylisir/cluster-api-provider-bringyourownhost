// Copyright 2024 VMware, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"strings"

	"github.com/NVIDIA/go-nvml/pkg/nvml"
	"k8s.io/klog/v2"
)

// GPUInfo holds information about detected GPUs
type GPUInfo struct {
	Present bool
	Model   string
	Count   int
}

// GetGPUInfo detects NVIDIA GPUs using NVML
func GetGPUInfo() GPUInfo {
	info := GPUInfo{Present: false, Count: 0}

	ret := nvml.Init()
	if ret != nvml.SUCCESS {
		klog.V(4).Infof("NVML Init failed: %v", nvml.ErrorString(ret))
		return info
	}
	defer nvml.Shutdown()

	count, ret := nvml.DeviceGetCount()
	if ret != nvml.SUCCESS {
		klog.V(4).Infof("DeviceGetCount failed: %v", nvml.ErrorString(ret))
		return info
	}

	if count == 0 {
		return info
	}

	info.Present = true
	info.Count = countLogicalGPUs()

	// Get model name from first physical GPU
	info.Model = getGPUModel()

	return info
}

func countLogicalGPUs() int {
	count, ret := nvml.DeviceGetCount()
	if ret != nvml.SUCCESS {
		return 0
	}

	total := 0
	for i := 0; i < count; i++ {
		device, ret := nvml.DeviceGetHandleByIndex(i)
		if ret != nvml.SUCCESS {
			continue
		}

		mode, _, ret := device.GetMigMode()
		if ret == nvml.SUCCESS && mode == nvml.DEVICE_MIG_ENABLE {
			// MIG enabled: count MIG instances
			maxMig, _ := device.GetMaxMigDeviceCount()
			for j := 0; j < maxMig; j++ {
				if _, ret := device.GetMigDeviceHandleByIndex(j); ret == nvml.SUCCESS {
					total++
				}
			}
		} else {
			// No MIG: count as 1
			total++
		}
	}
	return total
}

func getGPUModel() string {
	count, ret := nvml.DeviceGetCount()
	if ret != nvml.SUCCESS {
		return "Unknown"
	}

	for i := 0; i < count; i++ {
		device, ret := nvml.DeviceGetHandleByIndex(i)
		if ret != nvml.SUCCESS {
			continue
		}

		name, ret := device.GetName()
		if ret == nvml.SUCCESS {
			return sanitizeLabelForK8s(name)
		}
	}
	return "Unknown"
}

func isValidLabelChar(r rune) bool {
	return (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.'
}

func sanitizeLabelForK8s(s string) string {
	return strings.TrimRight(strings.Map(func(r rune) rune {
		if isValidLabelChar(r) {
			return r
		}
		return '_'
	}, s), "_")
}
