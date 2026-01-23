#!/bin/bash
# Usage: ./gen-bundle.sh <OS_VERSION> <K8S_VERSION> <BUNDLE_TAG>
# Example: ./gen-bundle.sh 24.04 v1.31.0 my-registry/bundle:v1.31.0-u24.04

set -e

if [ "$#" -ne 3 ]; then
    echo "Usage: $0 <OS_VERSION> <K8S_VERSION> <BUNDLE_TAG>"
    echo "Example: $0 24.04 v1.31.0 my-registry/bundle:v1.31.0-u24.04"
    exit 1
fi

OS_VER=$1
K8S_VER=$2
BUNDLE_TAG=$3

WORKDIR="bundle-build"
rm -rf $WORKDIR && mkdir -p $WORKDIR/debs

echo "Step 1: Downloading packages inside Docker..."
# Use Docker to download debs in a clean environment matching the target OS
docker run --rm -v $(pwd)/$WORKDIR:/out ubuntu:$OS_VER bash -c "
    apt-get update && apt-get install -y curl gpg
    K8S_MAJOR_MINOR=\$(echo $K8S_VER | cut -d. -f1,2)
    curl -fsSL https://pkgs.k8s.io/core:/stable:/\$K8S_MAJOR_MINOR/deb/Release.key | gpg --dearmor -o /usr/share/keyrings/kubernetes-apt-keyring.gpg
    echo 'deb [signed-by=/usr/share/keyrings/kubernetes-apt-keyring.gpg] https://pkgs.k8s.io/core:/stable:/\$K8S_MAJOR_MINOR/deb/ /' > /etc/apt/sources.list.d/kubernetes.list
    apt-get update
    cd /out/debs
    # Download packages without installing
    apt-get download kubelet kubeadm kubectl kubernetes-cni cri-tools containerd
"

echo "Step 2: Preparing config files..."
mkdir -p $WORKDIR/conf
# Default sysctl config
cat <<EOF > $WORKDIR/conf/k8s.conf
net.bridge.bridge-nf-call-iptables  = 1
net.bridge.bridge-nf-call-ip6tables = 1
net.ipv4.ip_forward                 = 1
EOF
tar -cvf $WORKDIR/conf.tar -C $WORKDIR/conf .

echo "Step 3: Pushing bundle with imgpkg..."
# Move debs to root of bundle dir
mv $WORKDIR/debs/*.deb $WORKDIR/
rmdir $WORKDIR/debs

# Push bundle
imgpkg push -b $BUNDLE_TAG -f $WORKDIR

echo "Success! Bundle pushed to $BUNDLE_TAG"
rm -rf $WORKDIR
