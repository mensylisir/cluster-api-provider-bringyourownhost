#!/bin/bash
set -euo pipefail

# Usage: ./build-bundle.sh --k8s-version v1.30.0 --os-version ubuntu:22.04
# Example: ./build-bundle.sh --k8s-version v1.30.0 --os-version ubuntu:24.04

K8S_VERSION=""
OS_IMAGE="ubuntu:22.04"
ARCH="amd64"
OUTPUT_DIR=""

while [[ $# -gt 0 ]]; do
  case $1 in
    --k8s-version)
      K8S_VERSION="$2"
      shift 2
      ;;
    --os-version)
      OS_IMAGE="$2"
      shift 2
      ;;
    --arch)
      ARCH="$2"
      shift 2
      ;;
    --output)
      OUTPUT_DIR="$2"
      shift 2
      ;;
    *)
      echo "Unknown argument: $1"
      exit 1
      ;;
  esac
done

if [ -z "$K8S_VERSION" ]; then
    echo "Usage: $0 --k8s-version <version> [--os-version <image>] [--arch <amd64|arm64>] [--output <dir>]"
    exit 1
fi

# Normalize OS name for output (e.g., ubuntu:22.04 -> ubuntu22.04)
OS_TAG=$(echo "$OS_IMAGE" | tr ':' '_')
BUNDLE_DIR=${OUTPUT_DIR:-"bundle-${K8S_VERSION}-${OS_TAG}-${ARCH}"}
DOWNLOAD_DIR="${BUNDLE_DIR}/downloads"
BIN_DIR="${BUNDLE_DIR}/bin"
CNI_DIR="${BUNDLE_DIR}/cni"
CONTAINERD_DIR="${BUNDLE_DIR}/containerd"

# Versions
CONTAINERD_VERSION="1.7.0"
RUNC_VERSION="v1.1.10"
CNI_PLUGINS_VERSION="v1.4.0"
CRICTL_VERSION="${K8S_VERSION}"

echo "Building bundle for Kubernetes ${K8S_VERSION} on ${OS_IMAGE} (${ARCH})..."
mkdir -p "${DOWNLOAD_DIR}" "${BIN_DIR}" "${CNI_DIR}/bin" "${CONTAINERD_DIR}/bin"

# 1. Download Kubernetes Binaries directly
echo "Downloading Kubernetes ${K8S_VERSION} binaries for ${ARCH}..."

# Download kubeadm
echo "  Downloading kubeadm..."
curl -L --fail --output "${BIN_DIR}/kubeadm" \
    "https://dl.k8s.io/${K8S_VERSION}/bin/linux/${ARCH}/kubeadm"

# Download kubectl
echo "  Downloading kubectl..."
curl -L --fail --output "${BIN_DIR}/kubectl" \
    "https://dl.k8s.io/${K8S_VERSION}/bin/linux/${ARCH}/kubectl"

# Download kubelet
echo "  Downloading kubelet..."
curl -L --fail --output "${BIN_DIR}/kubelet" \
    "https://dl.k8s.io/${K8S_VERSION}/bin/linux/${ARCH}/kubelet"

# Download cri-tools (crictl)
echo "  Downloading cri-tools..."
curl -L --fail --output "${DOWNLOAD_DIR}/crictl.tar.gz" \
    "https://github.com/kubernetes-sigs/cri-tools/releases/download/${CRICTL_VERSION}/crictl-${CRICTL_VERSION}-linux-${ARCH}.tar.gz"
tar -xzf "${DOWNLOAD_DIR}/crictl.tar.gz" -C "${DOWNLOAD_DIR}"
mv "${DOWNLOAD_DIR}/crictl-${CRICTL_VERSION}-linux-${ARCH}/crictl" "${BIN_DIR}/"
rm -rf "${DOWNLOAD_DIR}/crictl-${CRICTL_VERSION}-linux-${ARCH}"
rm "${DOWNLOAD_DIR}/crictl.tar.gz"

# 2. Download CNI Plugins
echo "Downloading CNI Plugins ${CNI_PLUGINS_VERSION}..."
curl -L --fail --output "${DOWNLOAD_DIR}/cni-plugins.tgz" \
    "https://github.com/containernetworking/plugins/releases/download/${CNI_PLUGINS_VERSION}/cni-plugins-linux-${ARCH}-${CNI_PLUGINS_VERSION}.tgz"
tar -xzf "${DOWNLOAD_DIR}/cni-plugins.tgz" -C "${CNI_DIR}/bin/"
rm "${DOWNLOAD_DIR}/cni-plugins.tgz"

# 3. Download Containerd
echo "Downloading Containerd ${CONTAINERD_VERSION}..."
curl -L --fail --output "${DOWNLOAD_DIR}/containerd.tar.gz" \
    "https://github.com/containerd/containerd/releases/download/v${CONTAINERD_VERSION}/containerd-${CONTAINERD_VERSION}-linux-${ARCH}.tar.gz"
tar -xzf "${DOWNLOAD_DIR}/containerd.tar.gz" -C "${CONTAINERD_DIR}/"
rm "${DOWNLOAD_DIR}/containerd.tar.gz"

# 4. Download runc
echo "Downloading runc ${RUNC_VERSION}..."
curl -L --fail --output "${CONTAINERD_DIR}/bin/runc" \
    "https://github.com/opencontainers/runc/releases/download/${RUNC_VERSION}/runc.${ARCH}"
chmod +x "${CONTAINERD_DIR}/bin/runc"

# 5. Create Configuration Tarball (conf.tar)
echo "Creating conf.tar..."
CONF_TMP=$(mktemp -d)
mkdir -p "$CONF_TMP/etc/sysctl.d"
mkdir -p "$CONF_TMP/etc/modules-load.d"

# Sysctl
cat <<EOF > "$CONF_TMP/etc/sysctl.d/k8s.conf"
net.bridge.bridge-nf-call-iptables  = 1
net.bridge.bridge-nf-call-ip6tables = 1
net.ipv4.ip_forward                 = 1
EOF

# Modules
cat <<EOF > "$CONF_TMP/etc/modules-load.d/k8s.conf"
overlay
br_netfilter
EOF

tar -C "$CONF_TMP" -cvf "${DOWNLOAD_DIR}/conf.tar" .
rm -rf "$CONF_TMP"

# 6. Cleanup download dir and move everything to bundle root
rm -rf "${DOWNLOAD_DIR}"
rmdir "${BUNDLE_DIR}" 2>/dev/null || true

echo "Bundle created at ${BUNDLE_DIR}"
echo ""
echo "Bundle structure:"
find "${BUNDLE_DIR}" -type f | head -20
echo ""
echo "Bundle contents:"
ls -lh "${BUNDLE_DIR}/bin/" 2>/dev/null || echo "  (no bin dir)"
ls -lh "${BUNDLE_DIR}/cni/bin/" 2>/dev/null || echo "  (no cni/bin dir)"
ls -lh "${BUNDLE_DIR}/containerd/bin/" 2>/dev/null || echo "  (no containerd/bin dir)"
ls -lh "${BUNDLE_DIR}/conf.tar" 2>/dev/null || echo "  (no conf.tar)"
echo ""
echo "Done!"
