#!/bin/bash
# Use this script to deploy the amazon-np-controller deployment on Dataplane nodes

# Parameters:
# KUBECONFIG: path to the kubeconfig file, default ~/.kube/config
# NP_CONTROLLER_IMAGE: Custom network policy controller image repository
# NP_CONTROLLER_TAG: Custom network policy controller image tag
# NP_CONTROLLER_ENDPOINT_CHUNK_SIZE: The number of endpoints in policy endpoint resource
# NP_CONTROLLER_MEMORY_LIMIT: Controller memory limit, defaults to 4Gi. The startup scale test (test/e2e/startup)
#   caches 10k PolicyEndpoints of ~600 endpoints each, which needs more than the chart's 1Gi default.

set -e
DIR=$(cd "$(dirname "$0")"; pwd)
source ${DIR}/lib/network-policy.sh

echo "Ensuring Control Plane Controller is disabled"
disable_cp_network_policy_controller

HELM_NPC_ARGS=""
if [[ ! -z $NP_CONTROLLER_IMAGE ]]; then
    HELM_NPC_ARGS+=" --set image.repository=$NP_CONTROLLER_IMAGE"
fi

if [[ ! -z $NP_CONTROLLER_TAG ]]; then
    HELM_NPC_ARGS+=" --set image.tag=$NP_CONTROLLER_TAG"
fi

if [[ ! -z $NP_CONTROLLER_ENDPOINT_CHUNK_SIZE ]]; then
    HELM_NPC_ARGS+=" --set endpointChunkSize=$NP_CONTROLLER_ENDPOINT_CHUNK_SIZE"
fi

HELM_NPC_ARGS+=" --set resources.limits.memory=${NP_CONTROLLER_MEMORY_LIMIT:-4Gi}"

echo "Deploy the default Amazon Network Policy Controller on Dataplane"
helm template amazon-network-policy-controller-k8s ${DIR}/../charts/amazon-network-policy-controller-k8s \
    --namespace kube-system \
    --set enableConfigMapCheck=false \
    --set useExistingClusterRole=true $HELM_NPC_ARGS | kubectl apply -f -

echo "Restarting the Controller"
kubectl rollout restart deployment.v1.apps/amazon-network-policy-controller-k8s -n kube-system

echo "Ensuring Controller is Running on Dataplane"
kubectl rollout status deployment.v1.apps/amazon-network-policy-controller-k8s -n kube-system --timeout=2m || (echo "Amazon Network Policy controller is unhealthy" && exit 1)

echo "Ensuring the dataplane Controller is the leader"
verify_dataplane_controller_is_leader
