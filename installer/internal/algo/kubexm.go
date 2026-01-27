// Copyright 2024 VMware, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package algo

import (
	"bytes"
	"context"
	"fmt"
	"html/template"
)

// KubexmInstaller represents the installer for kubexm (TLS Bootstrap) mode
// In this mode, we install Kubernetes binaries directly without using kubeadm
type KubexmInstaller struct {
	install   string
	uninstall string
	upgrade   string
}

// NewKubexmInstaller creates a new KubexmInstaller for kubexm (TLS Bootstrap) mode
func NewKubexmInstaller(ctx context.Context, arch, bundleAddrs, k8sVersion string, downloadMode string, proxyConfig map[string]string) (*KubexmInstaller, error) {
	parseFn := func(script string) (string, error) {
		parser, err := template.New("parser").Parse(script)
		if err != nil {
			return "", fmt.Errorf("unable to parse kubexm install script")
		}
		var tpl bytes.Buffer
		if err = parser.Execute(&tpl, map[string]string{
			"Arch":               arch,
			"K8sVersion":         k8sVersion,
			"DownloadMode":       downloadMode,
			"BundleAddrs":        bundleAddrs,
			"BundleDownloadPath": "{{.BundleDownloadPath}}",
			"ImgpkgVersion":      ImgpkgVersion,
			"HttpProxy":          proxyConfig["http-proxy"],
			"HttpsProxy":         proxyConfig["https-proxy"],
			"NoProxy":            proxyConfig["no-proxy"],
		}); err != nil {
			return "", fmt.Errorf("unable to apply parsed template to kubexm installer")
		}
		return tpl.String(), nil
	}

	install, err := parseFn(DoKubexm)
	if err != nil {
		return nil, err
	}
	uninstall, err := parseFn(UndoKubexm)
	if err != nil {
		return nil, err
	}
	upgrade, err := parseFn(UpgradeKubexm)
	if err != nil {
		return nil, err
	}

	return &KubexmInstaller{
		install:   install,
		uninstall: uninstall,
		upgrade:   upgrade,
	}, nil
}

// Install returns the kubexm installation script
func (s *KubexmInstaller) Install() string {
	return s.install
}

// Uninstall returns the kubexm uninstallation script
func (s *KubexmInstaller) Uninstall() string {
	return s.uninstall
}

// Upgrade returns the kubexm upgrade script
func (s *KubexmInstaller) Upgrade() string {
	return s.upgrade
}

// KubexmInstallScript is the installation script for kubexm (TLS Bootstrap) mode
// This installs Kubernetes binaries directly and sets up kubelet for TLS bootstrapping
var (
	DoKubexm = `
set -euox pipefail

# Debug mode: capture logs on failure
trap 'echo "Kubexm Installation failed. Collecting logs..."; journalctl -u kubelet --no-pager | tail -n 100; cat /var/log/byoh-agent.log || true' ERR

ARCH={{.Arch}}
K8S_VERSION={{.K8sVersion}}
DOWNLOAD_MODE={{.DownloadMode}}

BUNDLE_DOWNLOAD_PATH={{.BundleDownloadPath}}
BUNDLE_ADDR={{.BundleAddrs}}
IMGPKG_VERSION={{.ImgpkgVersion}}
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
rm -rf /run/kubernetes

echo "Kubexm mode: Installing Kubernetes binaries for TLS Bootstrap..."

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

if [ "$DOWNLOAD_MODE" == "online" ]; then
    echo "Running in ONLINE mode, downloading binaries from official releases..."
    
    K8S_DOWNLOAD_URL="https://dl.k8s.io/${K8S_VERSION}/bin/linux/${ARCH}"
    CRI_TOOLS_VERSION="${K8S_VERSION}"
    
    echo "Downloading Kubernetes ${K8S_VERSION} binaries for ${ARCH}..."
    
    # Download kubelet
    echo "Downloading kubelet..."
    curl -fsSL "${K8S_DOWNLOAD_URL}/kubelet" -o /usr/local/bin/kubelet
    chmod +x /usr/local/bin/kubelet
    
    # Download kube-proxy
    echo "Downloading kube-proxy..."
    curl -fsSL "${K8S_DOWNLOAD_URL}/kube-proxy" -o /usr/local/bin/kube-proxy
    chmod +x /usr/local/bin/kube-proxy
    
    # Download kubectl (for troubleshooting)
    echo "Downloading kubectl..."
    curl -fsSL "${K8S_DOWNLOAD_URL}/kubectl" -o /usr/local/bin/kubectl
    chmod +x /usr/local/bin/kubectl
    
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
    
else
    echo "Running in OFFLINE mode, using binary bundle..."
    
    echo "Checking for local bundle..."
    mkdir -p $BUNDLE_PATH

    # Check if critical binary files exist
    if [ -f "$BUNDLE_PATH/bin/kubelet" ] && [ -f "$BUNDLE_PATH/containerd/bin/containerd" ]; then
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
        
        # Verify kube-proxy exists (critical for binary mode)
        if [ ! -f "/usr/local/bin/kube-proxy" ]; then
             echo "WARNING: kube-proxy not found in bundle! Binary mode installation might fail if ManageKubeProxy is true."
        fi
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

## load kernel modules
modprobe overlay && modprobe br_netfilter

## configuring containerd with SystemdCgroup = true (required for cgroup v2)
mkdir -p /etc/containerd
containerd config default > /etc/containerd/config.toml
sed -i 's/SystemdCgroup = false/SystemdCgroup = true/' /etc/containerd/config.toml

## Create directories for kubelet and kube-proxy
mkdir -p /var/lib/kubelet
mkdir -p /var/lib/kube-proxy
mkdir -p /etc/kubernetes/manifests
mkdir -p /etc/kubernetes/pki

## Create kubelet config directory
mkdir -p /var/lib/kubelet/config

## Create kubeconfig directories
mkdir -p /etc/kubernetes

# Create a placeholder kubelet.conf that will be replaced after TLS bootstrap
# This is needed for kubelet to have a valid kubeconfig path
echo "Creating placeholder kubelet.conf..."
cat > /etc/kubernetes/kubelet.conf << 'EOF'
apiVersion: v1
kind: Config
clusters:
- cluster:
    server: https://{{.PlaceholderAPI}}:6443
    insecure-skip-tls-verify: true
  name: default
contexts:
- context:
    cluster: default
    user: default
  name: default
current-context: default
users:
- name: default
  user: {}
EOF

# Create kubelet.service for systemd (optional, for systems that use systemd)
echo "Kubexm installation complete. Ready for TLS Bootstrap."
echo "Agent will start kubelet with --bootstrap-kubeconfig after CSR approval."

## starting containerd service
systemctl daemon-reload && systemctl enable containerd && systemctl start containerd
`

	UndoKubexm = `
set -euox pipefail

# Proxy configuration
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


BUNDLE_DOWNLOAD_PATH={{.BundleDownloadPath}}
BUNDLE_ADDR={{.BundleAddrs}}
BUNDLE_PATH=$BUNDLE_DOWNLOAD_PATH/$BUNDLE_ADDR

## Reset Kubernetes state (Best Effort)
echo "Resetting Kubernetes state..."
if command -v kubelet >/dev/null; then
    systemctl stop kubelet || true
    systemctl disable kubelet || true
fi

if command -v kube-proxy >/dev/null; then
    systemctl stop kube-proxy || true
    systemctl disable kube-proxy || true
fi

if command -v kubeadm >/dev/null; then
    kubeadm reset -f || true
fi

## disabling containerd service
systemctl stop containerd && systemctl disable containerd && systemctl daemon-reload

## Deep Clean: Remove Data Directories
echo "Cleaning up data directories..."
rm -rf /var/lib/etcd
rm -rf /var/lib/kubelet
rm -rf /var/lib/kube-proxy
rm -rf /etc/kubernetes
rm -rf /run/kubernetes

## Removing Kubernetes binaries
echo "Removing Kubernetes binaries..."
rm -f /usr/local/bin/kubelet
rm -f /usr/local/bin/kube-proxy
rm -f /usr/local/bin/kubectl
rm -f /usr/local/bin/crictl
rm -f /usr/local/bin/containerd
rm -f /usr/local/bin/containerd-shim-runc-v2
rm -f /usr/local/bin/runc

## Removing CNI plugins
echo "Removing CNI plugins..."
rm -rf /opt/cni/bin/*

## removing containerd configuration
rm -rf /etc/containerd

## remove kernel modules
modprobe -rq overlay && modprobe -r br_netfilter || true

## enable firewall
if command -v ufw >>/dev/null; then
	ufw enable
fi

## enable swap
swapon -a && sed -ri '/\sswap\s/s/^#?//' /etc/fstab

rm -rf $BUNDLE_PATH
echo "Kubexm cleanup complete."
`

	UpgradeKubexm = `
set -euox pipefail

# Proxy configuration
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


BUNDLE_DOWNLOAD_PATH={{.BundleDownloadPath}}
BUNDLE_ADDR={{.BundleAddrs}}
ARCH={{.Arch}}
K8S_VERSION={{.K8sVersion}}
DOWNLOAD_MODE={{.DownloadMode}}
BUNDLE_PATH=$BUNDLE_DOWNLOAD_PATH/$BUNDLE_ADDR

echo "Kubexm upgrade mode..."

if [ "$DOWNLOAD_MODE" == "online" ]; then
    echo "Running in ONLINE mode, upgrading binaries..."
    
    K8S_DOWNLOAD_URL="https://dl.k8s.io/${K8S_VERSION}/bin/linux/${ARCH}"
    
    echo "Upgrading kubelet..."
    curl -fsSL "${K8S_DOWNLOAD_URL}/kubelet" -o /usr/local/bin/kubelet
    chmod +x /usr/local/bin/kubelet
    
    echo "Upgrading kube-proxy..."
    curl -fsSL "${K8S_DOWNLOAD_URL}/kube-proxy" -o /usr/local/bin/kube-proxy
    chmod +x /usr/local/bin/kube-proxy
    
    echo "Upgrading kubectl..."
    curl -fsSL "${K8S_DOWNLOAD_URL}/kubectl" -o /usr/local/bin/kubectl
    chmod +x /usr/local/bin/kubectl
    
else
    echo "Running in OFFLINE mode, upgrading via binary bundle..."
    
    echo "Checking for local bundle..."
    mkdir -p $BUNDLE_PATH

    if [ -f "$BUNDLE_PATH/bin/kubelet" ]; then
        echo "Upgrading Kubernetes binaries from bundle..."
        cp -f $BUNDLE_PATH/bin/* /usr/local/bin/
        chmod +x /usr/local/bin/*
    else
        echo "Bundle not found. Downloading..."
        imgpkg pull -i $BUNDLE_ADDR -o $BUNDLE_PATH
        cp -f $BUNDLE_PATH/bin/* /usr/local/bin/
        chmod +x /usr/local/bin/*
    fi
    
    # Check if binaries exist
    if [ -f "/usr/local/bin/kubelet" ]; then
        echo "Using existing kubelet binary..."
    else
        echo "ERROR: Kubelet binary not found"
        exit 1
    fi
fi

echo "Restarting kubelet..."
systemctl daemon-reload
systemctl restart kubelet

echo "Upgrade complete!"
`
)
