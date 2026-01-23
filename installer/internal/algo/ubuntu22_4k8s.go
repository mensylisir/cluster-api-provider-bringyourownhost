// Copyright 2024 VMware, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package algo

import (
	"bytes"
	"context"
	"fmt"
	"html/template"
)

// Ubuntu22_04Installer represent the installer implementation for ubunto22.04.* os distribution
type Ubuntu22_04Installer struct {
	install   string
	uninstall string
	upgrade   string
}

// NewUbuntu22_04Installer will return new Ubuntu22_04Installer instance
func NewUbuntu22_04Installer(ctx context.Context, arch, bundleAddrs, k8sVersion string, proxyConfig map[string]string) (*Ubuntu22_04Installer, error) {
	parseFn := func(script string) (string, error) {
		parser, err := template.New("parser").Parse(script)
		if err != nil {
			return "", fmt.Errorf("unable to parse install script")
		}
		var tpl bytes.Buffer
		if err = parser.Execute(&tpl, map[string]string{
			"BundleAddrs":        bundleAddrs,
			"Arch":               arch,
			"ImgpkgVersion":      ImgpkgVersion,
			"BundleDownloadPath": "{{.BundleDownloadPath}}",
			"K8sVersion":         k8sVersion,
			"HttpProxy":          proxyConfig["http-proxy"],
			"HttpsProxy":         proxyConfig["https-proxy"],
			"NoProxy":            proxyConfig["no-proxy"],
		}); err != nil {
			return "", fmt.Errorf("unable to apply install parsed template to the data object")
		}
		return tpl.String(), nil
	}

	install, err := parseFn(DoUbuntu22_4K8s)
	if err != nil {
		return nil, err
	}
	uninstall, err := parseFn(UndoUbuntu22_4K8s)
	if err != nil {
		return nil, err
	}
	upgrade, err := parseFn(UpgradeUbuntu22_4K8s)
	if err != nil {
		return nil, err
	}
	return &Ubuntu22_04Installer{
		install:   install,
		uninstall: uninstall,
		upgrade:   upgrade,
	}, nil
}

// Install will return k8s install script
func (s *Ubuntu22_04Installer) Install() string {
	return s.install
}

// Uninstall will return k8s uninstall script
func (s *Ubuntu22_04Installer) Uninstall() string {
	return s.uninstall
}

// Upgrade will return k8s upgrade script
func (s *Ubuntu22_04Installer) Upgrade() string {
	return s.upgrade
}

// contains the installation and uninstallation steps for the supported os and k8s
var (
	DoUbuntu22_4K8s = `
set -euox pipefail

# Debug mode: capture logs on failure
trap 'echo "Installation failed. Collecting logs..."; journalctl -u kubelet --no-pager | tail -n 100; cat /var/log/byoh-agent.log || true' ERR

BUNDLE_DOWNLOAD_PATH={{.BundleDownloadPath}}
BUNDLE_ADDR={{.BundleAddrs}}
IMGPKG_VERSION={{.ImgpkgVersion}}
ARCH={{.Arch}}
BUNDLE_PATH=$BUNDLE_DOWNLOAD_PATH/$BUNDLE_ADDR


if ! command -v imgpkg >>/dev/null; then
	echo "installing imgpkg"	
	
	if command -v wget >>/dev/null; then
		dl_bin="wget -nv -O-"
	elif command -v curl >>/dev/null; then
		dl_bin="curl -s -L"
	else
		echo "installing curl"
		apt-get install -y curl
		dl_bin="curl -s -L"
	fi
	
	$dl_bin github.com/vmware-tanzu/carvel-imgpkg/releases/download/$IMGPKG_VERSION/imgpkg-linux-$ARCH > /tmp/imgpkg
	mv /tmp/imgpkg /usr/local/bin/imgpkg
	chmod +x /usr/local/bin/imgpkg
fi

echo "Checking for local bundle..."
mkdir -p $BUNDLE_PATH

# Check if critical files exist to determine if we can skip download
if [ -f "$BUNDLE_PATH/kubeadm.deb" ] && [ -f "$BUNDLE_PATH/containerd.tar" ]; then
    echo "Local bundle found. Skipping download."
else
    echo "Local bundle not found or incomplete. Downloading..."
    imgpkg pull -i $BUNDLE_ADDR -o $BUNDLE_PATH
fi

## Pre-flight Check: Swap
if swapon --show | grep -q .; then
    echo "Error: Swap is enabled. Please disable swap before proceeding."
    exit 1
fi

## Pre-flight Check: Apt Lock
if fuser /var/lib/dpkg/lock >/dev/null 2>&1 || fuser /var/lib/apt/lists/lock >/dev/null 2>&1; then
    echo "Error: Apt is currently locked by another process. Please wait and try again."
    exit 1
fi


## disable swap
swapoff -a && sed -ri '/\sswap\s/s/^#?/#/' /etc/fstab

## disable firewall
if command -v ufw >>/dev/null; then
	ufw disable
fi

## ensure iptables is installed (required for kube-proxy)
if ! command -v iptables >>/dev/null; then
	echo "installing iptables"
	apt-get update && apt-get install -y iptables
fi

## load kernal modules
modprobe overlay && modprobe br_netfilter

## adding os configuration
tar -C / -xvf "$BUNDLE_PATH/conf.tar" && sysctl --system 

## installing deb packages
for pkg in cri-tools kubernetes-cni kubectl kubelet kubeadm; do
	dpkg --install "$BUNDLE_PATH/$pkg.deb" && apt-mark hold $pkg
done

## intalling containerd
tar -C / -xvf "$BUNDLE_PATH/containerd.tar"

## configuring containerd with SystemdCgroup = true (required for cgroup v2)
mkdir -p /etc/containerd
containerd config default > /etc/containerd/config.toml
sed -i 's/SystemdCgroup = false/SystemdCgroup = true/' /etc/containerd/config.toml

## starting containerd service
systemctl daemon-reload && systemctl enable containerd && systemctl start containerd`

	UndoUbuntu22_4K8s = `
set -euox pipefail

BUNDLE_DOWNLOAD_PATH={{.BundleDownloadPath}}
BUNDLE_ADDR={{.BundleAddrs}}
BUNDLE_PATH=$BUNDLE_DOWNLOAD_PATH/$BUNDLE_ADDR

## Reset Kubernetes state (Best Effort)
echo "Resetting Kubernetes state..."
if command -v kubeadm >/dev/null; then
    kubeadm reset -f || true
fi

## disabling containerd service
systemctl stop containerd && systemctl disable containerd && systemctl daemon-reload

## Deep Clean: Remove Data Directories
echo "Cleaning up data directories..."
rm -rf /var/lib/etcd
rm -rf /var/lib/kubelet
rm -rf /etc/kubernetes
rm -rf /var/lib/cni
rm -rf /etc/cni
rm -rf /opt/cni
rm -rf /opt/containerd
rm -rf /etc/containerd

## removing containerd files
tar tf "$BUNDLE_PATH/containerd.tar" | xargs -n 1 echo '/' | sed 's/ //g'  | grep -e '[^/]$' | xargs rm -f || true

## removing deb packages
for pkg in kubeadm kubelet kubectl kubernetes-cni cri-tools; do
	dpkg --purge $pkg || true
done

## removing os configuration
tar tf "$BUNDLE_PATH/conf.tar" | xargs -n 1 echo '/' | sed 's/ //g' | grep -e "[^/]$" | xargs rm -f || true

## remove kernal modules
modprobe -rq overlay && modprobe -r br_netfilter || true

## enable firewall
if command -v ufw >>/dev/null; then
	ufw enable
fi

## enable swap
swapon -a && sed -ri '/\sswap\s/s/^#?//' /etc/fstab

rm -rf $BUNDLE_PATH`

	UpgradeUbuntu22_4K8s = `
set -euox pipefail

BUNDLE_DOWNLOAD_PATH={{.BundleDownloadPath}}
BUNDLE_ADDR={{.BundleAddrs}}
BUNDLE_PATH=$BUNDLE_DOWNLOAD_PATH/$BUNDLE_ADDR

echo "Checking for local bundle..."
mkdir -p $BUNDLE_PATH

if [ -f "$BUNDLE_PATH/kubeadm.deb" ] && [ -f "$BUNDLE_PATH/kubelet.deb" ]; then
    echo "Local bundle found. Skipping download."
else
    echo "Local bundle not found or incomplete. Downloading..."
    imgpkg pull -i $BUNDLE_ADDR -o $BUNDLE_PATH
fi

echo "Upgrading kubeadm..."
dpkg --install "$BUNDLE_PATH/kubeadm.deb"
apt-mark hold kubeadm

# Determine version from new kubeadm
NEW_K8S_VERSION=$(kubeadm version -o short)

echo "Applying kubeadm upgrade to $NEW_K8S_VERSION..."

if [ -f /etc/kubernetes/manifests/kube-apiserver.yaml ]; then
    kubeadm upgrade apply -y $NEW_K8S_VERSION
else
    kubeadm upgrade node
fi

echo "Upgrading kubelet and kubectl..."
dpkg --install "$BUNDLE_PATH/kubelet.deb" "$BUNDLE_PATH/kubectl.deb"
apt-mark hold kubelet kubectl

echo "Restarting kubelet..."
systemctl daemon-reload
systemctl restart kubelet

echo "Upgrade complete!"
`
)
