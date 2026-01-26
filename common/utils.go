// Copyright 2021 VMware, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package common

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"

	"github.com/pkg/errors"
)

// GzipData compresses the data bytes
func GzipData(data []byte) ([]byte, error) {
	var b bytes.Buffer
	gz := gzip.NewWriter(&b)

	if _, err := gz.Write(data); err != nil {
		return nil, err
	}

	if err := gz.Flush(); err != nil {
		return nil, err
	}

	if err := gz.Close(); err != nil {
		return nil, err
	}

	return b.Bytes(), nil
}

// GunzipData un-compress the compressed data bytes
func GunzipData(data []byte) ([]byte, error) {
	var r io.Reader
	var err error
	b := bytes.NewBuffer(data)
	r, err = gzip.NewReader(b)
	if err != nil {
		return nil, errors.WithStack(err)
	}

	var resB bytes.Buffer
	_, err = resB.ReadFrom(r)
	if err != nil {
		return nil, errors.WithStack(err)
	}

	return resB.Bytes(), nil
}

// RemoveGlob removes glob file as specified path
func RemoveGlob(path string) error {
	contents, err := filepath.Glob(path)
	if err != nil {
		return err
	}
	for _, item := range contents {
		err = os.RemoveAll(item)
		if err != nil {
			return err
		}
	}
	return nil
}

const (
	// ProviderIDPrefix is the prefix for BYOH provider IDs
	ProviderIDPrefix = "byoh://"
)

// GenerateProviderID generates a standardized ProviderID for a host
// This ensures consistency across all injection points (cloud-init, kubelet args, Node objects)
func GenerateProviderID(hostname string) string {
	return fmt.Sprintf("%s%s", ProviderIDPrefix, hostname)
}

// ValidateProviderID validates that a ProviderID matches the expected format
func ValidateProviderID(providerID, hostname string) (bool, error) {
	if providerID == "" {
		return false, errors.New("providerID is empty")
	}

	// Match "byoh://<hostname>" or "byoh://<hostname>/<suffix>"
	pattern := fmt.Sprintf("^%s%s(/(.+))?$", ProviderIDPrefix, hostname)
	match, err := regexp.MatchString(pattern, providerID)
	if err != nil {
		return false, err
	}

	return match, nil
}
