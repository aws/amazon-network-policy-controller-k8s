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
# CONTROL_PLANE_TIER: EKS Provisioned Control Plane tier (standard, tier-xl, tier-2xl, tier-4xl), defaults to standard
#   (create only)
set -euo pipefail

REGION="${REGION:-us-west-2}"
KUBECONFIG="${KUBECONFIG:-$HOME/.kube/config}"
NODES_CAPACITY="${NODES_CAPACITY:-3}"
CONTROL_PLANE_TIER="${CONTROL_PLANE_TIER:-standard}"

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
controlPlaneScalingConfig:
  tier: ${CONTROL_PLANE_TIER}
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

# remaining_stacks prints "<name> <status>" for every eksctl stack of the cluster that still exists.
# Deleted stacks are not returned by describe-stacks, and names end in "-IPv4-"/"-IPv6-" plus a suffix,
# so the prefix cannot match another cluster's stacks.
remaining_stacks() {
    aws cloudformation describe-stacks --region "$REGION" \
        --query "Stacks[?starts_with(StackName, 'eksctl-${CLUSTER_NAME}-')].[StackName,StackStatus]" \
        --output text
}

# delete_detached_enis deletes the available (detached) ENIs in the cluster's VPC. ENIs leaked by the VPC CNI
# or by load balancers are the usual reason the subnets, security groups and VPC fail to delete.
delete_detached_enis() {
    local vpc eni
    vpc=$(aws cloudformation describe-stack-resource --region "$REGION" --stack-name "eksctl-${CLUSTER_NAME}-cluster" \
        --logical-resource-id VPC --query StackResourceDetail.PhysicalResourceId --output text 2>/dev/null) || return 0
    [[ $vpc == vpc-* ]] || return 0
    for eni in $(aws ec2 describe-network-interfaces --region "$REGION" \
            --filters "Name=vpc-id,Values=$vpc" "Name=status,Values=available" \
            --query 'NetworkInterfaces[].NetworkInterfaceId' --output text); do
        echo "deleting detached ENI $eni in $vpc"
        aws ec2 delete-network-interface --region "$REGION" --network-interface-id "$eni" || true
    done
}

print_delete_failures() {
    local stack
    while read -r stack _; do
        [[ -n $stack ]] || continue
        echo "DELETE_FAILED resources in $stack:" >&2
        aws cloudformation describe-stack-events --region "$REGION" --stack-name "$stack" \
            --query "StackEvents[?ResourceStatus=='DELETE_FAILED'].[LogicalResourceId,ResourceStatusReason]" \
            --output text >&2 || true
    done <<<"$1"
}

# delete_cluster succeeds only once every eksctl CloudFormation stack of the cluster is gone. The EKS cluster
# is deleted before its VPC, so describe-cluster returning ResourceNotFoundException says nothing about whether
# the VPC stack went away, and without --wait eksctl returns before the stack deletion has finished.
delete_cluster() {
    require CLUSTER_NAME
    local attempt stacks stack status
    for attempt in 1 2 3; do
        eksctl delete cluster --name "$CLUSTER_NAME" --region "$REGION" --disable-nodegroup-eviction --wait ||
            echo "eksctl delete cluster failed (attempt $attempt)" >&2

        if ! stacks=$(remaining_stacks); then
            echo "unable to list CloudFormation stacks" >&2
        elif [[ -z $stacks ]]; then
            echo "all stacks of $CLUSTER_NAME are deleted"
            return
        else
            echo "stacks of $CLUSTER_NAME still present:"$'\n'"$stacks"
            # eksctl may not retry stacks of a cluster that is already gone, so retry them directly.
            delete_detached_enis
            while read -r stack status; do
                [[ $status == DELETE_IN_PROGRESS ]] ||
                    aws cloudformation delete-stack --region "$REGION" --stack-name "$stack" || true
                aws cloudformation wait stack-delete-complete --region "$REGION" --stack-name "$stack" || true
            done <<<"$stacks"
            if stacks=$(remaining_stacks) && [[ -z $stacks ]]; then
                echo "all stacks of $CLUSTER_NAME are deleted"
                return
            fi
        fi
        [[ $attempt -eq 3 ]] || sleep 60
    done
    echo "failed to delete $CLUSTER_NAME" >&2
    print_delete_failures "${stacks:-}"
    exit 1
}

case "${1:-}" in
    create) create_cluster ;;
    delete) delete_cluster ;;
    *) echo "usage: $0 {create|delete}" >&2; exit 2 ;;
esac
