// Copyright 2024 VMware, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bufio"
	"os/exec"
	"strings"

	"k8s.io/klog/v2"
)

// GPUInfo holds information about detected GPUs
type GPUInfo struct {
	Present bool
	Model   string
}

// GetGPUInfo detects if an NVIDIA GPU is present and attempts to identify the model
func GetGPUInfo() GPUInfo {
	info := GPUInfo{Present: false}

	// Check for NVIDIA GPU using lspci
	// 10de is the vendor ID for NVIDIA
	cmd := exec.Command("lspci", "-d", "10de:")
	output, err := cmd.Output()
	if err != nil {
		// lspci might not be installed or failed
		klog.V(4).Infof("lspci failed or not found: %v", err)
		return info
	}

	if len(output) > 0 {
		info.Present = true
		info.Model = parseGPUModel(string(output))
	}

	return info
}

// parseGPUModel extracts a simplified model name from lspci output
// Example output: "00:06.0 3D controller: NVIDIA Corporation Tesla T4 (rev a1)"
func parseGPUModel(output string) string {
	scanner := bufio.NewScanner(strings.NewReader(output))
	for scanner.Scan() {
		line := scanner.Text()
		// Look for "NVIDIA Corporation"
		if idx := strings.Index(line, "NVIDIA Corporation"); idx != -1 {
			// Extract everything after "NVIDIA Corporation "
			// e.g. "Tesla T4 (rev a1)"
			remaining := strings.TrimSpace(line[idx+len("NVIDIA Corporation"):])

			// Remove revision info if present
			if revIdx := strings.LastIndex(remaining, "("); revIdx != -1 {
				remaining = strings.TrimSpace(remaining[:revIdx])
			}

			// Replace spaces with underscores for label safety
			return strings.ReplaceAll(remaining, " ", "_")
		}
	}
	return "Unknown"
}
