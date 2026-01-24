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
    echo "Running in ONLINE mode, using binary download..."

    # Download Kubernetes binaries directly from official releases
    K8S_MAJOR_MINOR=$(echo $K8S_VERSION | cut -d. -f1,2)
    K8S_PATCH=$(echo $K8S_VERSION | cut -d. -f3)
    K8S_DOWNLOAD_URL="https://dl.k8s.io/${K8S_VERSION}/bin/linux/${ARCH}"
    CRI_TOOLS_VERSION="${K8S_VERSION}"
    
    echo "Downloading Kubernetes ${K8S_VERSION} binaries for ${ARCH}..."
    
    # Download kubeadm
    echo "Downloading kubeadm..."
    curl -fsSL "${K8S_DOWNLOAD_URL}/kubeadm" -o /usr/local/bin/kubeadm
    chmod +x /usr/local/bin/kubeadm
    
    # Download kubectl
    echo "Downloading kubectl..."
    curl -fsSL "${K8S_DOWNLOAD_URL}/kubectl" -o /usr/local/bin/kubectl
    chmod +x /usr/local/bin/kubectl
    
    # Download kubelet
    echo "Downloading kubelet..."
    curl -fsSL "${K8S_DOWNLOAD_URL}/kubelet" -o /usr/local/bin/kubelet
    chmod +x /usr/local/bin/kubelet
    
    # Download cri-tools (crictl)
    echo "Downloading cri-tools..."
    curl -fsSL "https://github.com/kubernetes-sigs/cri-tools/releases/download/${CRI_TOOLS_VERSION}/crictl-${CRI_TOOLS_VERSION}-linux-${ARCH}.tar.gz" -o /tmp/crictl.tar.gz
    tar -xzf /tmp/crictl.tar.gz -C /tmp
    mv /tmp/crictl-${CRI_TOOLS_VERSION}-linux-${ARCH}/crictl /usr/local/bin/
    rm -rf /tmp/crictl.tar.gz /tmp/crictl-${CRI_TOOLS_VERSION}-linux-${ARCH}
    
    # Download CNI plugins
    echo "Downloading CNI plugins..."
    mkdir -p /opt/cni/bin
    curl -fsSL "https://github.com/containernetworking/plugins/releases/download/v1.4.0/cni-plugins-linux-${ARCH}-v1.4.0.tgz" -o /tmp/cni-plugins.tgz
    tar -xzf /tmp/cni-plugins.tgz -C /opt/cni/bin/
    rm /tmp/cni-plugins.tgz
    
    # Download containerd and runc binaries
    echo "Downloading containerd..."
    CONTAINERD_VERSION="v1.7.0"
    CONTAINERD_URL="https://github.com/containerd/containerd/releases/download/${CONTAINERD_VERSION}/containerd-${CONTAINERD_VERSION}-linux-${ARCH}.tar.gz"
    curl -fsSL "$CONTAINERD_URL" -o /tmp/containerd.tar.gz
    tar -xzf /tmp/containerd.tar.gz -C /usr/local/
    rm /tmp/containerd.tar.gz
    
    echo "Downloading runc..."
    RUNC_VERSION="v1.1.10"
    curl -fsSL "https://github.com/opencontainers/runc/releases/download/${RUNC_VERSION}/runc.${ARCH}" -o /usr/local/bin/runc
    chmod +x /usr/local/bin/runc
    
    # Create dummy bundle path for subsequent logic compatibility
    mkdir -p $BUNDLE_PATH
    
else
    echo "Running in OFFLINE mode, using binary bundle..."
    
    echo "Checking for local bundle..."
    mkdir -p $BUNDLE_PATH

    # Check if critical binary files exist
    if [ -f "$BUNDLE_PATH/kubeadm" ] && [ -f "$BUNDLE_PATH/containerd/bin/containerd" ]; then
        echo "Local binary bundle found. Skipping download."
    else
        echo "Local bundle not found or incomplete. Downloading..."
        imgpkg pull -i $BUNDLE_ADDR -o $BUNDLE_PATH
    fi
    
    # Extract and install Kubernetes binaries
    if [ -d "$BUNDLE_PATH/bin" ]; then
        echo "Installing Kubernetes binaries from bundle..."
        cp -f $BUNDLE_PATH/bin/* /usr/local/bin/
        chmod +x /usr/local/bin/*
    fi
    
    # Install CNI plugins
    if [ -d "$BUNDLE_PATH/cni/bin" ]; then
        echo "Installing CNI plugins from bundle..."
        mkdir -p /opt/cni/bin
        cp -f $BUNDLE_PATH/cni/bin/* /opt/cni/bin/
    fi
    
    # Install containerd
    if [ -d "$BUNDLE_PATH/containerd" ]; then
        echo "Installing containerd from bundle..."
        cp -rf $BUNDLE_PATH/containerd/* /usr/local/
    fi
fi

## Pre-flight Check: Swap
if swapon --show | grep -q .; then
    echo "Error: Swap is enabled. Please disable swap before proceeding."
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

## Removing Kubernetes binaries
echo "Removing Kubernetes binaries..."
rm -f /usr/local/bin/kubeadm
rm -f /usr/local/bin/kubectl
rm -f /usr/local/bin/kubelet
rm -f /usr/local/bin/crictl
rm -f /usr/local/bin/containerd
rm -f /usr/local/bin/containerd-shim-runc-v2
rm -f /usr/local/bin/runc

## Removing CNI plugins
echo "Removing CNI plugins..."
rm -rf /opt/cni/bin/*

## removing os configuration
if [ -f "$BUNDLE_PATH/conf.tar" ]; then
    tar tf "$BUNDLE_PATH/conf.tar" | xargs -n 1 echo '/' | sed 's/ //g' | grep -e "[^/]$" | xargs rm -f || true
fi

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
ARCH={{.Arch}}
K8S_VERSION={{.K8sVersion}}
BUNDLE_PATH=$BUNDLE_DOWNLOAD_PATH/$BUNDLE_ADDR

echo "Checking upgrade mode..."

if [ "$BUNDLE_ADDR" == "online" ]; then
    echo "Running in ONLINE mode, upgrading via binary download..."
    
    K8S_DOWNLOAD_URL="https://dl.k8s.io/${K8S_VERSION}/bin/linux/${ARCH}"
    
    echo "Upgrading kubeadm..."
    curl -fsSL "${K8S_DOWNLOAD_URL}/kubeadm" -o /usr/local/bin/kubeadm
    chmod +x /usr/local/bin/kubeadm
    
    # Determine version from new kubeadm
    NEW_K8S_VERSION=$(kubeadm version -o short)
    
    echo "Applying kubeadm upgrade to $NEW_K8S_VERSION..."
    
    # Check if this is a control plane node (simple check for kube-apiserver manifest)
    if [ -f /etc/kubernetes/manifests/kube-apiserver.yaml ]; then
        kubeadm upgrade apply -y $NEW_K8S_VERSION
    else
        kubeadm upgrade node
    fi
    
    echo "Upgrading kubelet and kubectl..."
    curl -fsSL "${K8S_DOWNLOAD_URL}/kubelet" -o /usr/local/bin/kubelet
    chmod +x /usr/local/bin/kubelet
    
    curl -fsSL "${K8S_DOWNLOAD_URL}/kubectl" -o /usr/local/bin/kubectl
    chmod +x /usr/local/bin/kubectl

else
    echo "Running in OFFLINE mode, upgrading via binary bundle..."
    
    echo "Checking for local bundle..."
    mkdir -p $BUNDLE_PATH

    if [ -f "$BUNDLE_PATH/bin/kubeadm" ]; then
        echo "Upgrading Kubernetes binaries from bundle..."
        cp -f $BUNDLE_PATH/bin/* /usr/local/bin/
        chmod +x /usr/local/bin/*
    else
        echo "Bundle not found. Downloading..."
        imgpkg pull -i $BUNDLE_ADDR -o $BUNDLE_PATH
        cp -f $BUNDLE_PATH/bin/* /usr/local/bin/
        chmod +x /usr/local/bin/*
    fi
    
    # Determine version from new kubeadm
    NEW_K8S_VERSION=$(kubeadm version -o short)
    
    echo "Applying kubeadm upgrade to $NEW_K8S_VERSION..."
    
    if [ -f /etc/kubernetes/manifests/kube-apiserver.yaml ]; then
        kubeadm upgrade apply -y $NEW_K8S_VERSION
    else
        kubeadm upgrade node
    fi
fi

echo "Restarting kubelet..."
systemctl daemon-reload
systemctl restart kubelet

echo "Upgrade complete!"
`
)
