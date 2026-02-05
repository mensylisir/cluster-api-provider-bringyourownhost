// Copyright 2024 VMware, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bufio"
	"os/exec"
	"regexp"
	"strings"

	"k8s.io/klog/v2"
)

// GPUInfo holds information about detected GPUs
type GPUInfo struct {
	Present bool
	Model   string
	Count   int
}

// GetGPUInfo detects if an NVIDIA GPU is present and attempts to identify the model
func GetGPUInfo() GPUInfo {
	info := GPUInfo{Present: false, Count: 0}

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
		info.Count = countGPUs(string(output))
	}

	return info
}

// countGPUs counts the number of NVIDIA devices found in lspci output
func countGPUs(output string) int {
	scanner := bufio.NewScanner(strings.NewReader(output))
	count := 0
	for scanner.Scan() {
		line := scanner.Text()
		// Basic validation: ensure line contains NVIDIA
		if strings.Contains(line, "NVIDIA") {
			count++
		}
	}
	return count
}

// parseGPUModel extracts a simplified model name from lspci output
// Example output: "00:06.0 3D controller: NVIDIA Corporation Tesla T4 (rev a1)"
func parseGPUModel(output string) string {
	// Regex to match valid label value characters
	// Label values must match: (([A-Za-z0-9][-A-Za-z0-9_.]*)?[A-Za-z0-9])?
	var invalidCharRegex = regexp.MustCompile(`[^A-Za-z0-9_-.]`)

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

			// Replace invalid characters with underscores for label safety
			sanitized := invalidCharRegex.ReplaceAllString(remaining, "_")
			return sanitized
		}
	}
	return "Unknown"
}
