// Copyright 2024 VMware, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/xml"
	"os/exec"
	"strings"

	"k8s.io/klog/v2"
)

type NvidiaSmiLog struct {
	AttachedGpus int         `xml:"attached_gpus"`
	Gpus         []GpuDetail `xml:"gpu"`
}

type GpuDetail struct {
	ProductName string `xml:"product_name"`
	Uuid        string `xml:"uuid"`
	MigMode     struct {
		CurrentMig string `xml:"current_mig"`
	} `xml:"mig_mode"`
	MigDevices struct {
		MigDevice []struct {
			Index       int    `xml:"index"`
			Uuid        string `xml:"uuid"`
			ProductName string `xml:"product_name"`
		} `xml:"mig_device"`
	} `xml:"mig_devices"`
}

type GPUInfo struct {
	Present bool
	Model   string
	Count   int
}

func GetGPUInfo() GPUInfo {
	info := GPUInfo{Present: false, Count: 0}

	cmd := exec.Command("nvidia-smi", "-q", "-x")
	output, err := cmd.Output()
	if err != nil {
		klog.V(4).Infof("nvidia-smi failed or not found: %v", err)
		return info
	}

	var smiLog NvidiaSmiLog
	if err := xml.Unmarshal(output, &smiLog); err != nil {
		klog.V(4).Infof("Failed to parse nvidia-smi XML: %v", err)
		return info
	}

	if len(smiLog.Gpus) == 0 {
		return info
	}

	info.Present = true
	info.Count = countLogicalGPUs(smiLog.Gpus)
	info.Model = getGPUModel(smiLog.Gpus)

	return info
}

func countLogicalGPUs(gpus []GpuDetail) int {
	total := 0
	for _, gpu := range gpus {
		if gpu.MigMode.CurrentMig == "Enabled" {
			total += len(gpu.MigDevices.MigDevice)
		} else {
			total++
		}
	}
	return total
}

func getGPUModel(gpus []GpuDetail) string {
	for _, gpu := range gpus {
		if gpu.MigMode.CurrentMig == "Enabled" && len(gpu.MigDevices.MigDevice) > 0 {
			return sanitizeLabelForK8s(gpu.ProductName)
		}
		if gpu.MigMode.CurrentMig != "Enabled" {
			return sanitizeLabelForK8s(gpu.ProductName)
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
