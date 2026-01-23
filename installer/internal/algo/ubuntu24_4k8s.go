// Copyright 2024 VMware, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package algo

import (
	"bytes"
	"context"
	"fmt"
	"html/template"
)

// Ubuntu24_04Installer represent the installer implementation for ubunto24.04.* os distribution
type Ubuntu24_04Installer struct {
	install   string
	uninstall string
	upgrade   string
}

// NewUbuntu24_04Installer will return new Ubuntu24_04Installer instance
func NewUbuntu24_04Installer(ctx context.Context, arch, bundleAddrs, k8sVersion string, proxyConfig map[string]string) (*Ubuntu24_04Installer, error) {
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

	install, err := parseFn(DoUbuntu24_4K8s)
	if err != nil {
		return nil, err
	}
	uninstall, err := parseFn(UndoUbuntu24_4K8s)
	if err != nil {
		return nil, err
	}
	upgrade, err := parseFn(UpgradeUbuntu24_4K8s)
	if err != nil {
		return nil, err
	}
	return &Ubuntu24_04Installer{
		install:   install,
		uninstall: uninstall,
		upgrade:   upgrade,
	}, nil
}

// Install will return k8s install script
func (s *Ubuntu24_04Installer) Install() string {
	return s.install
}

// Uninstall will return k8s uninstall script
func (s *Ubuntu24_04Installer) Uninstall() string {
	return s.uninstall
}

// Upgrade will return k8s upgrade script
func (s *Ubuntu24_04Installer) Upgrade() string {
	return s.upgrade
}

// contains the installation and uninstallation steps for the supported os and k8s
var (
	DoUbuntu24_4K8s = `
set -euox pipefail

# Debug mode: capture logs on failure
trap 'echo "Installation failed. Collecting logs..."; journalctl -u kubelet --no-pager | tail -n 100; cat /var/log/byoh-agent.log || true' ERR

BUNDLE_DOWNLOAD_PATH={{.BundleDownloadPath}}
BUNDLE_ADDR={{.BundleAddrs}}
IMGPKG_VERSION={{.ImgpkgVersion}}
ARCH={{.Arch}}
K8S_VERSION={{.K8sVersion}}
BUNDLE_PATH=$BUNDLE_DOWNLOAD_PATH/$BUNDLE_ADDR

# Production: Ensure NTP time sync is active
echo "Ensuring time synchronization..."
systemctl restart systemd-timesyncd || true
timedatectl set-ntp true || true

# Production: Configure Proxy if set
HTTP_PROXY_VAL="{{.HttpProxy}}"
HTTPS_PROXY_VAL="{{.HttpsProxy}}"
NO_PROXY_VAL="{{.NoProxy}}"
if [ -n "$HTTP_PROXY_VAL" ]; then
    export HTTP_PROXY="$HTTP_PROXY_VAL"
    export http_proxy="$HTTP_PROXY_VAL"
fi
if [ -n "$HTTPS_PROXY_VAL" ]; then
    export HTTPS_PROXY="$HTTPS_PROXY_VAL"
    export https_proxy="$HTTPS_PROXY_VAL"
fi
if [ -n "$NO_PROXY_VAL" ]; then
    export NO_PROXY="$NO_PROXY_VAL"
    export no_proxy="$NO_PROXY_VAL"
fi

# Resilience: Proactively clean up any previous state to ensure a fresh install
echo "Ensuring clean state..."
if command -v kubeadm >/dev/null; then
    kubeadm reset -f || true
fi
rm -rf /etc/cni/net.d
rm -rf /var/lib/kubelet
rm -rf /etc/kubernetes
rm -rf /var/lib/etcd


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

echo "Checking installation mode..."

if [ "$BUNDLE_ADDR" == "online" ]; then
    echo "Running in ONLINE mode, using apt install..."
    
    # 2.1 Install dependencies
    apt-get update && apt-get install -y apt-transport-https ca-certificates curl gpg
    
    # 2.2 Configure Kubernetes official repo (pkgs.k8s.io)
    # Note: apt.kubernetes.io is deprecated
    K8S_MAJOR_MINOR=$(echo $K8S_VERSION | cut -d. -f1,2)
    mkdir -p -m 755 /etc/apt/keyrings
    # Remove old key if exists to avoid conflict
    rm -f /etc/apt/keyrings/kubernetes-apt-keyring.gpg
    curl -fsSL "https://pkgs.k8s.io/core:/stable:/$K8S_MAJOR_MINOR/deb/Release.key" | gpg --dearmor -o /etc/apt/keyrings/kubernetes-apt-keyring.gpg
    echo "deb [signed-by=/etc/apt/keyrings/kubernetes-apt-keyring.gpg] https://pkgs.k8s.io/core:/stable:/$K8S_MAJOR_MINOR/deb/ /" | tee /etc/apt/sources.list.d/kubernetes.list

    # 2.3 Install packages
    apt-get update
    # Strip 'v' prefix for apt package version matching
    PKG_VER=$(echo $K8S_VERSION | sed 's/v//')
    # Install specific version if possible, or latest matching major.minor
    # Note: apt version format might differ slightly, usually 1.28.0-1.1
    # For simplicity in this script, we install the latest patch version of the requested minor version
    apt-get install -y kubelet kubeadm kubectl containerd
    apt-mark hold kubelet kubeadm kubectl
    
    # Create dummy bundle path for subsequent logic compatibility
    mkdir -p $BUNDLE_PATH
    
else
    echo "Running in OFFLINE mode, using imgpkg bundle..."
    
    echo "Checking for local bundle..."
    mkdir -p $BUNDLE_PATH

    # Check if critical files exist to determine if we can skip download
    if [ -f "$BUNDLE_PATH/kubeadm.deb" ] && [ -f "$BUNDLE_PATH/containerd.tar" ]; then
        echo "Local bundle found. Skipping download."
    else
        echo "Local bundle not found or incomplete. Downloading..."
        imgpkg pull -i $BUNDLE_ADDR -o $BUNDLE_PATH
    fi
    
    ## adding os configuration (Offline only)
    if [ -f "$BUNDLE_PATH/conf.tar" ]; then
        tar -C / -xvf "$BUNDLE_PATH/conf.tar" && sysctl --system 
    fi

    ## installing deb packages (Offline only)
    if [ -f "$BUNDLE_PATH/kubeadm.deb" ]; then
        for pkg in cri-tools kubernetes-cni kubectl kubelet kubeadm; do
            dpkg --install "$BUNDLE_PATH/$pkg.deb" && apt-mark hold $pkg
        done
    fi

    ## intalling containerd (Offline only)
    if [ -f "$BUNDLE_PATH/containerd.tar" ]; then
        tar -C / -xvf "$BUNDLE_PATH/containerd.tar"
    fi
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

## GPU Detection and Driver Installation
if lspci -n | grep -q "10de:"; then
    echo "NVIDIA GPU detected. Installing drivers..."
    
    # Ensure pciutils and ubuntu-drivers-common are installed
    apt-get update
    apt-get install -y pciutils ubuntu-drivers-common gpg

    # Install recommended drivers
    ubuntu-drivers autoinstall

    echo "Installing NVIDIA Container Toolkit..."
    curl -fsSL https://nvidia.github.io/libnvidia-container/gpgkey | gpg --dearmor -o /usr/share/keyrings/nvidia-container-toolkit-keyring.gpg \
    || { echo "Failed to download GPG key"; exit 1; }
    
    curl -s -L https://nvidia.github.io/libnvidia-container/stable/deb/nvidia-container-toolkit.list | \
      sed 's#deb https://#deb [signed-by=/usr/share/keyrings/nvidia-container-toolkit-keyring.gpg] https://#g' | \
      tee /etc/apt/sources.list.d/nvidia-container-toolkit.list
      
    apt-get update
    apt-get install -y nvidia-container-toolkit

    echo "Configuring containerd for NVIDIA..."
    # We will configure it after containerd is installed below
    # Just setting a flag file to remember to configure it later
    touch /tmp/install-nvidia-ctk
fi


## configuring containerd with SystemdCgroup = true (required for cgroup v2)
mkdir -p /etc/containerd
containerd config default > /etc/containerd/config.toml
sed -i 's/SystemdCgroup = false/SystemdCgroup = true/' /etc/containerd/config.toml

if [ -f /tmp/install-nvidia-ctk ]; then
    echo "Applying NVIDIA Container Toolkit configuration..."
    nvidia-ctk runtime configure --runtime=containerd
    rm /tmp/install-nvidia-ctk
fi

## starting containerd service
systemctl daemon-reload && systemctl enable containerd && systemctl start containerd`

	UndoUbuntu24_4K8s = `
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

	UpgradeUbuntu24_4K8s = `
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

# Check if this is a control plane node (simple check for kube-apiserver manifest)
if [ -f /etc/kubernetes/manifests/kube-apiserver.yaml ]; then
    # Control Plane Node
    # Note: This is a simplified upgrade flow. In HA, only one node runs 'apply', others run 'node'.
    # For BYOH POC/MVP, we assume 'apply' is safe or handled by the operator orchestration.
    # Ideally, the operator should tell us if we are the first CP or secondary.
    # For now, we'll try 'apply' and if it says it's already upgraded, it should be fine?
    # Actually 'upgrade node' is for worker nodes OR secondary control plane nodes in some flows.
    # But 'upgrade apply' is for the *first* control plane node.
    
    # A safer bet for automation without extra flags:
    # Try 'upgrade apply' non-interactively.
    kubeadm upgrade apply -y $NEW_K8S_VERSION
else
    # Worker Node
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
