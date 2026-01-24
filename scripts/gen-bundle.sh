#!/bin/bash
# Usage: ./gen-bundle.sh <OS_VERSION> <K8S_VERSION> <BUNDLE_TAG> [--arch <amd64|arm64>]
# Example: ./gen-bundle.sh 24.04 v1.31.0 my-registry/bundle:v1.31.0-u24.04
# Example: ./gen-bundle.sh 24.04 v1.31.0 my-registry/bundle:v1.31.0-u24.04 --arch arm64

set -e

# Parse arguments
OS_VER=""
K8S_VER=""
BUNDLE_TAG=""
ARCH="amd64"

while [[ $# -gt 0 ]]; do
  case $1 in
    --arch)
      ARCH="$2"
      shift 2
      ;;
    *)
      if [ -z "$OS_VER" ]; then
        OS_VER=$1
      elif [ -z "$K8S_VER" ]; then
        K8S_VER=$1
      elif [ -z "$BUNDLE_TAG" ]; then
        BUNDLE_TAG=$1
      fi
      shift
      ;;
  esac
done

if [ -z "$OS_VER" ] || [ -z "$K8S_VER" ] || [ -z "$BUNDLE_TAG" ]; then
    echo "Usage: $0 <OS_VERSION> <K8S_VERSION> <BUNDLE_TAG> [--arch <amd64|arm64>]"
    echo "Example: $0 24.04 v1.31.0 my-registry/bundle:v1.31.0-u24.04"
    echo "Example: $0 24.04 v1.31.0 my-registry/bundle:v1.31.0-u24.04 --arch arm64"
    exit 1
fi

WORKDIR="bundle-build"
rm -rf $WORKDIR && mkdir -p $WORKDIR/bin $WORKDIR/cni/bin $WORKDIR/containerd/bin

echo "Step 1: Downloading Kubernetes binaries for ${K8S_VERSION} (${ARCH})..."

# Download kubeadm
curl -fsSL "https://dl.k8s.io/${K8S_VER}/bin/linux/${ARCH}/kubeadm" -o $WORKDIR/bin/kubeadm
chmod +x $WORKDIR/bin/kubeadm

# Download kubectl
curl -fsSL "https://dl.k8s.io/${K8S_VER}/bin/linux/${ARCH}/kubectl" -o $WORKDIR/bin/kubectl
chmod +x $WORKDIR/bin/kubectl

# Download kubelet
curl -fsSL "https://dl.k8s.io/${K8S_VER}/bin/linux/${ARCH}/kubelet" -o $WORKDIR/bin/kubelet
chmod +x $WORKDIR/bin/kubelet

# Download cri-tools (crictl)
curl -fsSL "https://github.com/kubernetes-sigs/cri-tools/releases/download/${K8S_VER}/crictl-${K8S_VER}-linux-${ARCH}.tar.gz" -o /tmp/crictl.tar.gz
tar -xzf /tmp/crictl.tar.gz -C /tmp
mv /tmp/crictl-${K8S_VER}-linux-${ARCH}/crictl $WORKDIR/bin/
rm -rf /tmp/crictl-${K8S_VER}-linux-${ARCH} /tmp/crictl.tar.gz

echo "Step 2: Downloading CNI plugins..."
CNI_VERSION="v1.4.0"
curl -fsSL "https://github.com/containernetworking/plugins/releases/download/${CNI_VERSION}/cni-plugins-linux-${ARCH}-${CNI_VERSION}.tgz" -o /tmp/cni.tar.gz
tar -xzf /tmp/cni.tar.gz -C $WORKDIR/cni/bin/
rm /tmp/cni.tar.gz

echo "Step 3: Downloading containerd..."
CONTAINERD_VERSION="v1.7.0"
RUNC_VERSION="v1.1.10"
curl -fsSL "https://github.com/containerd/containerd/releases/download/${CONTAINERD_VERSION}/containerd-${CONTAINERD_VERSION}-linux-${ARCH}.tar.gz" -o /tmp/containerd.tar.gz
tar -xzf /tmp/containerd.tar.gz -C $WORKDIR/containerd/
rm /tmp/containerd.tar.gz

curl -fsSL "https://github.com/opencontainers/runc/releases/download/${RUNC_VERSION}/runc.${ARCH}" -o $WORKDIR/containerd/bin/runc
chmod +x $WORKDIR/containerd/bin/runc

echo "Step 4: Preparing config files..."
mkdir -p $WORKDIR/conf
# Default sysctl config
cat <<EOF > $WORKDIR/conf/k8s.conf
net.bridge.bridge-nf-call-iptables  = 1
net.bridge.bridge-nf-call-ip6tables = 1
net.ipv4.ip_forward                 = 1
EOF
cat <<EOF > $WORKDIR/conf/modules.conf
overlay
br_netfilter
EOF
tar -cvf $WORKDIR/conf.tar -C $WORKDIR/conf .

echo "Step 5: Pushing bundle with imgpkg..."
# Push bundle
imgpkg push -b $BUNDLE_TAG -f $WORKDIR

echo "Success! Bundle pushed to $BUNDLE_TAG"
rm -rf $WORKDIR
