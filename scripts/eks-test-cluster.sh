#!/usr/bin/env bash
# Creates and deletes the EKS cluster used by the PR e2e workflow.
#
# Parameters:
# CLUSTER_NAME: name of the cluster
# K8S_VERSION: Kubernetes version (create only)
# IP_FAMILY: IPv4 or IPv6 (create only)
# INSTANCE_TYPE: worker node instance type (create only)
# REGION: defaults to us-west-2
# KUBECONFIG: defaults to ~/.kube/config
# NODES_CAPACITY: number of worker nodes, defaults to 3
set -euo pipefail

REGION="${REGION:-us-west-2}"
KUBECONFIG="${KUBECONFIG:-$HOME/.kube/config}"
NODES_CAPACITY="${NODES_CAPACITY:-3}"

require() {
    local name
    for name in "$@"; do
        [[ -n "${!name:-}" ]] || { echo "$name is required" >&2; exit 1; }
    done
}

create_cluster() {
    require CLUSTER_NAME K8S_VERSION IP_FAMILY INSTANCE_TYPE

    # enableNetworkPolicy starts the node agent; the controller itself is deployed on the
    # dataplane by deploy-controller-on-dataplane.sh, which disables the control plane one.
    cat >/tmp/eks-test-cluster.yaml <<EOF
apiVersion: eksctl.io/v1alpha5
kind: ClusterConfig
metadata:
  name: ${CLUSTER_NAME}
  region: ${REGION}
  version: "${K8S_VERSION}"
iam:
  withOIDC: true
kubernetesNetworkConfig:
  ipFamily: ${IP_FAMILY}
autoModeConfig:
  enabled: false
addons:
  - name: vpc-cni
    version: latest
    configurationValues: '{"enableNetworkPolicy":"true"}'
    resolveConflicts: overwrite
  - name: coredns
  - name: kube-proxy
managedNodeGroups:
  - name: ${CLUSTER_NAME}-ng
    amiFamily: AmazonLinux2023
    instanceType: ${INSTANCE_TYPE}
    desiredCapacity: ${NODES_CAPACITY}
    minSize: 1
    maxSize: ${NODES_CAPACITY}
EOF

    eksctl create cluster -f /tmp/eks-test-cluster.yaml
    mkdir -p "$(dirname "$KUBECONFIG")"
    aws eks update-kubeconfig \
        --name "$CLUSTER_NAME" --region "$REGION" --kubeconfig "$KUBECONFIG"
}

delete_cluster() {
    require CLUSTER_NAME
    local attempt out
    for attempt in 1 2 3; do
        if eksctl delete cluster --name "$CLUSTER_NAME" --region "$REGION" --disable-nodegroup-eviction; then
            return
        fi
        if ! out=$(aws eks describe-cluster --name "$CLUSTER_NAME" --region "$REGION" 2>&1); then
            grep -q ResourceNotFoundException <<<"$out" && return
            echo "describe-cluster failed: $out" >&2
        fi
        [[ $attempt -eq 3 ]] || sleep 30
    done
    echo "failed to delete $CLUSTER_NAME" >&2
    exit 1
}

case "${1:-}" in
    create) create_cluster ;;
    delete) delete_cluster ;;
    *) echo "usage: $0 {create|delete}" >&2; exit 2 ;;
esac
