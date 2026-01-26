// Copyright 2021 VMware, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package cloudinit

import (
	"context"
	"encoding/base64"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/pkg/errors"
	"github.com/mensylisir/cluster-api-provider-bringyourownhost/common"
	"sigs.k8s.io/yaml"
)

// ScriptExecutor bootstrap script executor
type ScriptExecutor struct {
	WriteFilesExecutor    IFileWriter
	RunCmdExecutor        ICmdRunner
	ParseTemplateExecutor ITemplateParser
	Hostname              string
}

type bootstrapConfig struct {
	FilesToWrite      []Files  `json:"write_files"`
	CommandsToExecute []string `json:"runCmd"`
}

// Files details required for files written by bootstrap script
type Files struct {
	Path        string `json:"path,"`
	Encoding    string `json:"encoding,omitempty"`
	Owner       string `json:"owner,omitempty"`
	Permissions string `json:"permissions,omitempty"`
	Content     string `json:"content"`
	Append      bool   `json:"append,omitempty"`
}

// Execute performs the following operations on the bootstrap script
//   - parse the script to get the cloudinit data
//   - execute the write_files directive
//   - execute the run_cmd directive
func (se ScriptExecutor) Execute(bootstrapScript string) error {
	cloudInitData := bootstrapConfig{}
	if err := yaml.Unmarshal([]byte(bootstrapScript), &cloudInitData); err != nil {
		return errors.Wrapf(err, "error parsing write_files action: %s", bootstrapScript)
	}

	for i := range cloudInitData.FilesToWrite {
		directoryToCreate := filepath.Dir(cloudInitData.FilesToWrite[i].Path)
		err := se.WriteFilesExecutor.MkdirIfNotExists(directoryToCreate)
		if err != nil {
			return errors.Wrap(err, fmt.Sprintf("Error creating the directory %s", directoryToCreate))
		}

		encodings := parseEncodingScheme(cloudInitData.FilesToWrite[i].Encoding)
		cloudInitData.FilesToWrite[i].Content, err = decodeContent(cloudInitData.FilesToWrite[i].Content, encodings)
		if err != nil {
			return errors.Wrap(err, fmt.Sprintf("error decoding content for %s", cloudInitData.FilesToWrite[i].Path))
		}

		cloudInitData.FilesToWrite[i].Content, err = se.ParseTemplateExecutor.ParseTemplate(cloudInitData.FilesToWrite[i].Content)
		if err != nil {
			return errors.Wrap(err, fmt.Sprintf("error parse template content for %s", cloudInitData.FilesToWrite[i].Path))
		}

		// Phase 18: Auto-Scaling Integration
		// Intercept kubeadm config to inject ProviderID
		if se.Hostname != "" && (strings.Contains(cloudInitData.FilesToWrite[i].Path, "kubeadm") || strings.HasSuffix(cloudInitData.FilesToWrite[i].Path, ".yaml")) {
			// Try to parse as YAML and check for nodeRegistration
			var config map[string]interface{}
			if err := yaml.Unmarshal([]byte(cloudInitData.FilesToWrite[i].Content), &config); err == nil {
				if _, ok := config["nodeRegistration"]; ok {
					// It looks like a kubeadm config
					nodeReg, _ := config["nodeRegistration"].(map[string]interface{})
					if nodeReg == nil {
						nodeReg = make(map[string]interface{})
					}

					extraArgs, _ := nodeReg["kubeletExtraArgs"].(map[string]interface{})
					if extraArgs == nil {
						extraArgs = make(map[string]interface{})
					}

					// Inject provider-id if not present using standardized format
					if _, exists := extraArgs["provider-id"]; !exists {
						extraArgs["provider-id"] = common.GenerateProviderID(se.Hostname)
						nodeReg["kubeletExtraArgs"] = extraArgs
						config["nodeRegistration"] = nodeReg

						// Marshal back
						newContent, err := yaml.Marshal(config)
						if err == nil {
							cloudInitData.FilesToWrite[i].Content = string(newContent)
						}
					}
				}
			}
		}

		err = se.WriteFilesExecutor.WriteToFile(&cloudInitData.FilesToWrite[i])
		if err != nil {
			return errors.Wrap(err, fmt.Sprintf("Error writing the file %s", cloudInitData.FilesToWrite[i].Path))
		}
	}

	for _, cmd := range cloudInitData.CommandsToExecute {
		err := se.RunCmdExecutor.RunCmd(context.TODO(), cmd)
		if err != nil {
			return errors.Wrap(err, fmt.Sprintf("Error running the command %s", cmd))
		}
	}
	return nil
}

func parseEncodingScheme(e string) []string {
	e = strings.ToLower(e)
	e = strings.TrimSpace(e)

	switch e {
	case "gz+base64", "gzip+base64", "gz+b64", "gzip+b64":
		return []string{"application/base64", "application/x-gzip"}
	case "base64", "b64":
		return []string{"application/base64"}
	}

	return []string{"text/plain"}
}

func decodeContent(content string, encodings []string) (string, error) {
	for _, e := range encodings {
		switch e {
		case "application/base64":
			rByte, err := base64.StdEncoding.DecodeString(content)
			if err != nil {
				return content, errors.WithStack(err)
			}
			content = string(rByte)
		case "application/x-gzip":
			rByte, err := common.GunzipData([]byte(content))
			if err != nil {
				return content, err
			}
			content = string(rByte)
		case "text/plain":
			continue
		default:
			return content, errors.Errorf("Unknown bootstrap data encoding: %q", content)
		}
	}
	return content, nil
}
