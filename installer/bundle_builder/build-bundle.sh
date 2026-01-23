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
    echo "Usage: $0 --k8s-version <version> [--os-version <image>] [--output <dir>]"
    exit 1
fi

# Normalize OS name for output (e.g., ubuntu:22.04 -> ubuntu22.04)
OS_TAG=$(echo "$OS_IMAGE" | tr ':' '_')
BUNDLE_DIR=${OUTPUT_DIR:-"bundle-${K8S_VERSION}-${OS_TAG}"}
DOWNLOAD_DIR="${BUNDLE_DIR}/downloads"

# Versions
CONTAINERD_VERSION="1.7.13"
CNI_PLUGINS_VERSION="v1.4.0"

echo "Building bundle for Kubernetes ${K8S_VERSION} on ${OS_IMAGE}..."
mkdir -p "${DOWNLOAD_DIR}"

# 1. Download Containerd (Generic Linux Binary)
# We stick to the binary tarball for containerd to ensure specific version control independent of OS repos
echo "Downloading Containerd ${CONTAINERD_VERSION}..."
curl -L --fail --output "${DOWNLOAD_DIR}/containerd.tar.gz" \
    "https://github.com/containerd/containerd/releases/download/v${CONTAINERD_VERSION}/containerd-${CONTAINERD_VERSION}-linux-${ARCH}.tar.gz"

# 2. Download CNI Plugins
echo "Downloading CNI Plugins ${CNI_PLUGINS_VERSION}..."
curl -L --fail --output "${DOWNLOAD_DIR}/cni-plugins.tgz" \
    "https://github.com/containernetworking/plugins/releases/download/${CNI_PLUGINS_VERSION}/cni-plugins-linux-${ARCH}-${CNI_PLUGINS_VERSION}.tgz"

# 3. Download Kubernetes Debs using Docker
# We use a container to ensure we get the correct .deb packages for the target OS
echo "Downloading Kubernetes .deb packages using ${OS_IMAGE}..."

# Create a script to run inside the container
cat <<EOF > "${DOWNLOAD_DIR}/download-debs.sh"
#!/bin/bash
set -euo pipefail

apt-get update && apt-get install -y apt-transport-https ca-certificates curl gpg

# Configure K8s Repo
K8S_MAJOR_MINOR=\$(echo "${K8S_VERSION}" | cut -d. -f1,2)
mkdir -p -m 755 /etc/apt/keyrings
curl -fsSL "https://pkgs.k8s.io/core:/stable:/\${K8S_MAJOR_MINOR}/deb/Release.key" | gpg --dearmor -o /etc/apt/keyrings/kubernetes-apt-keyring.gpg
echo "deb [signed-by=/etc/apt/keyrings/kubernetes-apt-keyring.gpg] https://pkgs.k8s.io/core:/stable:/\${K8S_MAJOR_MINOR}/deb/ /" | tee /etc/apt/sources.list.d/kubernetes.list

apt-get update

# Determine package version
# apt versions often look like 1.30.0-1.1
PKG_VER=\$(echo "${K8S_VERSION}" | sed 's/v//')

# Download packages
# We download: kubelet, kubeadm, kubectl, cri-tools, kubernetes-cni
# We rely on apt-get download to fetch the specific version if available, or fail if not.
# We try to be specific.
apt-get download "kubelet=\${PKG_VER}*" "kubeadm=\${PKG_VER}*" "kubectl=\${PKG_VER}*" "cri-tools" "kubernetes-cni"

# Rename them to standard names for the installer script (optional, but helps consistency)
# The installer expects: kubeadm.deb, kubelet.deb, etc.
# But apt-get download gives filenames like kubeadm_1.30.0-1.1_amd64.deb
# We will rename them in the host script after this container finishes.
EOF

chmod +x "${DOWNLOAD_DIR}/download-debs.sh"

# Run Docker
# Mount the download directory to /output
docker run --rm -v "${DOWNLOAD_DIR}:/output" -w /output "${OS_IMAGE}" /output/download-debs.sh

# Rename downloaded debs to simple names expected by installer
# kubeadm_*.deb -> kubeadm.deb
echo "Renaming .deb files..."
find "${DOWNLOAD_DIR}" -name "kubeadm_*.deb" -exec mv {} "${DOWNLOAD_DIR}/kubeadm.deb" \;
find "${DOWNLOAD_DIR}" -name "kubelet_*.deb" -exec mv {} "${DOWNLOAD_DIR}/kubelet.deb" \;
find "${DOWNLOAD_DIR}" -name "kubectl_*.deb" -exec mv {} "${DOWNLOAD_DIR}/kubectl.deb" \;
find "${DOWNLOAD_DIR}" -name "cri-tools_*.deb" -exec mv {} "${DOWNLOAD_DIR}/cri-tools.deb" \;
find "${DOWNLOAD_DIR}" -name "kubernetes-cni_*.deb" -exec mv {} "${DOWNLOAD_DIR}/kubernetes-cni.deb" \;

# Cleanup helper script
rm "${DOWNLOAD_DIR}/download-debs.sh"

# 4. Create Configuration Tarball (conf.tar)
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

# 5. Finalize Bundle
# The installer expects the files to be at the root of the bundle directory (or handled by imgpkg)
# For this script, we leave them in 'downloads' or move them up?
# The previous script had:
#   BUNDLE_DIR/bin/ (binaries)
#   DOWNLOAD_DIR/ (tarballs)
# The installer (Go) looks for:
#   $BUNDLE_PATH/kubeadm.deb
#   $BUNDLE_PATH/containerd.tar
#   $BUNDLE_PATH/conf.tar
# So we should move everything to the root of BUNDLE_DIR and remove DOWNLOAD_DIR
mv "${DOWNLOAD_DIR}"/* "${BUNDLE_DIR}/"
rmdir "${DOWNLOAD_DIR}"

echo "Bundle created at ${BUNDLE_DIR}"
ls -lh "${BUNDLE_DIR}"
echo "Done!"
