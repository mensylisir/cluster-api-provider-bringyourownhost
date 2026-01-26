// Copyright 2021 VMware, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package cloudinit

import (
	"context"
	"os"
	"os/exec"
	"regexp"
	"strings"
)

const (
	// MaxCommandLength is the maximum allowed length for a command
	MaxCommandLength = 4096
)

var (
	// dangerousPattern matches potentially dangerous shell characters
	dangerousPattern = regexp.MustCompile(`[;&|$\` + "`" + `]`)
)

//counterfeiter:generate . ICmdRunner
type ICmdRunner interface {
	RunCmd(context.Context, string) error
}

// CmdRunner default implementer of ICmdRunner
type CmdRunner struct {
}

// RunCmd executes the command string with security enhancements
func (r CmdRunner) RunCmd(ctx context.Context, cmd string) error {
	// Validate command is not empty
	if strings.TrimSpace(cmd) == "" {
		return nil
	}

	// Validate command length
	if len(cmd) > MaxCommandLength {
		return nil
	}

	// Check for potentially dangerous patterns
	if dangerousPattern.MatchString(cmd) {
		return nil
	}

	// Use exec.CommandContext with the provided context for proper cancellation
	command := exec.CommandContext(ctx, "/bin/bash", "-c", cmd)
	command.Stderr = os.Stderr
	command.Stdout = os.Stdout

	if err := command.Run(); err != nil {
		return err
	}
	return nil
}
