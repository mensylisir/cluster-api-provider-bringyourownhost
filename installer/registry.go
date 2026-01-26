// Copyright 2021 VMware, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package installer

import (
	"errors"
	"fmt"
	"regexp"
)

// ErrBundleInstallerAlreadyExists is returned when a bundle installer already exists
var ErrBundleInstallerAlreadyExists = errors.New("bundle installer already exists")

type osk8sInstaller interface{}
type k8sInstallerMap map[string]osk8sInstaller
type osk8sInstallerMap map[string]k8sInstallerMap
type filterOsBundlePair struct {
	osFilter string
	osBundle string
}

type filterK8sBundle struct {
	k8sFilter string
}

type filterOSBundleList []filterOsBundlePair
type filterK8sBundleList []filterK8sBundle

// Registry contains
// 1. Entries associating BYOH Bundle i.e. (OS,K8sVersion) in the Repository with Installer in Host Agent
// 2. Entries that match a concrete OS to a BYOH Bundle OS from the Repository
// 3. Entries that match a Major & Minor versions of K8s to any of their patch sub-versions (e.g.: 1.22.3 -> 1.22.*)
type registry struct {
	osk8sInstallerMap
	filterOSBundleList
	filterK8sBundleList
}

func newRegistry() registry {
	return registry{osk8sInstallerMap: make(osk8sInstallerMap)}
}

// AddBundleInstaller adds a bundle installer to the registry
func (r *registry) AddBundleInstaller(os, k8sVer string) error {
	var empty interface{}

	if _, ok := r.osk8sInstallerMap[os]; !ok {
		r.osk8sInstallerMap[os] = make(k8sInstallerMap)
	}

	if _, alreadyExist := r.osk8sInstallerMap[os][k8sVer]; alreadyExist {
		return ErrBundleInstallerAlreadyExists
	}

	r.osk8sInstallerMap[os][k8sVer] = empty
	return nil
}

// AddOsFilter adds an OS filter to the filtered bundle list of registry
func (r *registry) AddOsFilter(osFilter, osBundle string) {
	r.filterOSBundleList = append(r.filterOSBundleList, filterOsBundlePair{osFilter: osFilter, osBundle: osBundle})
}

func (r *registry) AddK8sFilter(k8sFilter string) {
	r.filterK8sBundleList = append(r.filterK8sBundleList, filterK8sBundle{k8sFilter: k8sFilter})
}

// ListOS returns a list of OSes supported by the registry
func (r *registry) ListOS() (osFilter, osBundle []string) {
	osFilter = make([]string, 0, len(r.filterOSBundleList))
	osBundle = make([]string, 0, len(r.filterOSBundleList))

	for _, fbp := range r.filterOSBundleList {
		osFilter = append(osFilter, fbp.osFilter)
		osBundle = append(osBundle, fbp.osBundle)
	}

	return
}

// ListK8s returns a list of K8s versions supported by the registry
func (r *registry) ListK8s(osBundleHost string) []string {
	var result []string

	// os bundle
	if k8sMap, ok := r.osk8sInstallerMap[osBundleHost]; ok {
		for k8s := range k8sMap {
			result = append(result, k8s)
		}

		return result
	}

	// os host
	for k8s := range r.osk8sInstallerMap[r.ResolveOsToOsBundle(osBundleHost)] {
		result = append(result, k8s)
	}

	return result
}

func (r *registry) ResolveOsToOsBundle(os string) string {
	for _, fbp := range r.filterOSBundleList {
		matched, _ := regexp.MatchString(fbp.osFilter, os)
		if matched {
			return fbp.osBundle
		}
	}

	return ""
}

// GetSupportedRegistry returns a registry with installers for the supported OS and K8s
func GetSupportedRegistry() registry {
	reg := newRegistry()

	// Helper to add bundle installer, ignoring duplicate errors during initialization
	addBundle := func(os, k8sVer string) {
		_ = reg.AddBundleInstaller(os, k8sVer)
	}

	{
		// Ubuntu

		// Ubuntu 20.04
		linuxDistro := "Ubuntu_20.04.1_x86-64"
		addBundle(linuxDistro, "v1.24.*")
		addBundle(linuxDistro, "v1.25.*")
		addBundle(linuxDistro, "v1.26.*")

		reg.AddK8sFilter("v1.24.*")
		reg.AddK8sFilter("v1.25.*")
		reg.AddK8sFilter("v1.26.*")

		reg.AddOsFilter("Ubuntu_20.04.*_x86-64", linuxDistro)

		// Ubuntu 20.04 ARM64
		linuxDistroArm := "Ubuntu_20.04.1_aarch64"
		addBundle(linuxDistroArm, "v1.24.*")
		addBundle(linuxDistroArm, "v1.25.*")
		addBundle(linuxDistroArm, "v1.26.*")
		reg.AddOsFilter("Ubuntu_20.04.*_aarch64", linuxDistroArm)

		// Ubuntu 24.04
		linuxDistro24 := "Ubuntu_24.04.1_x86-64"
		for i := 27; i <= 35; i++ {
			version := fmt.Sprintf("v1.%d.*", i)
			addBundle(linuxDistro24, version)
			reg.AddK8sFilter(version)
		}

		// Ubuntu 22.04
		linuxDistro22 := "Ubuntu_22.04.1_x86-64"
		for i := 25; i <= 35; i++ {
			version := fmt.Sprintf("v1.%d.*", i)
			addBundle(linuxDistro22, version)
			reg.AddK8sFilter(version)
		}
		reg.AddOsFilter("Ubuntu_22.04.*_x86-64", linuxDistro22)

		reg.AddOsFilter("Ubuntu_24.04.*_x86-64", linuxDistro24)

		// Ubuntu 24.04 ARM64
		linuxDistro24Arm := "Ubuntu_24.04.1_aarch64"
		for i := 27; i <= 35; i++ {
			version := fmt.Sprintf("v1.%d.*", i)
			addBundle(linuxDistro24Arm, version)
		}
		reg.AddOsFilter("Ubuntu_24.04.*_aarch64", linuxDistro24Arm)

		// Ubuntu 22.04 ARM64
		linuxDistro22Arm := "Ubuntu_22.04.1_aarch64"
		for i := 25; i <= 35; i++ {
			version := fmt.Sprintf("v1.%d.*", i)
			addBundle(linuxDistro22Arm, version)
		}
		reg.AddOsFilter("Ubuntu_22.04.*_aarch64", linuxDistro22Arm)
	}

	/*
	 * PLACEHOLDER - ADD MORE OS HERE
	 */

	return reg
}
