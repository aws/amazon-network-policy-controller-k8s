#!/bin/bash

# The script runs Network Policy Cyclonus tests on a existing cluster
# Parameters:
# CLUSTER_NAME: name of the cluster
# KUBECONFIG: path to the kubeconfig file, default ~/.kube/config
# REGION: defaults to us-west-2
# IP_FAMILY: defaults to IPv4
# ADDON_VERSION: Optional, defaults to the latest version
# ENDPOINT: Optional
# SKIP_CNI_INSTALLATION: Optional, defaults to false. Skips installing the aws-vpc-cni helm chart with network policy enabled
# DISABLE_CP_NETWORK_POLICY_CONTROLLER: Optional, defaults to false. Set to true when the controller under test runs on
#   the dataplane (deploy-controller-on-dataplane.sh); the run then fails unless the dataplane controller is the leader
# TEST_IMAGE_REGISTRY: Optional, registry mirroring the cyclonus and agnhost images, defaults to the upstream images

set -euoE pipefail
DIR=$(cd "$(dirname "$0")"; pwd)

source ${DIR}/lib/network-policy.sh
source ${DIR}/lib/tests.sh

: "${ENDPOINT_FLAG:=""}"
: "${ENDPOINT:=""}"
: "${ADDON_VERSION:=""}"
: "${IP_FAMILY:="IPv4"}"
: "${REGION:="us-west-2"}"
: "${SKIP_CNI_INSTALLATION:="false"}"
: "${K8S_VERSION:=""}"
: "${DISABLE_CP_NETWORK_POLICY_CONTROLLER="false"}"
: "${TEST_IMAGE_REGISTRY:=""}"
: "${KUBECONFIG:="$HOME/.kube/config"}"
export KUBECONFIG

if [[ ! -z $ENDPOINT ]]; then
    ENDPOINT_FLAG="--endpoint-url $ENDPOINT"
fi

if [[ -z $K8S_VERSION ]]; then
    K8S_VERSION=$(aws eks describe-cluster $ENDPOINT_FLAG --name $CLUSTER_NAME --region $REGION | jq -r '.cluster.version')
fi

TEST_FAILED="false"

echo "Running Cyclonus e2e tests with the following variables
KUBECONFIG: $KUBECONFIG
CLUSTER_NAME: $CLUSTER_NAME
REGION: $REGION
IP_FAMILY: $IP_FAMILY
K8S_VERSION: $K8S_VERSION

Optional args
ENDPOINT: $ENDPOINT
"

if [[ $SKIP_CNI_INSTALLATION == "false" ]]; then
    install_network_policy_helm
else
    echo "Skipping CNI installation. Make sure you have enabled network policy support in your cluster before executing the test"
fi

if [[ $DISABLE_CP_NETWORK_POLICY_CONTROLLER == "true" ]]; then
    echo "Disable CP Network Policy Controller on controller plane"
    disable_cp_network_policy_controller
    verify_dataplane_controller_is_leader
else
    echo "Skip disabling CP Network Policy controller. Tests will be evaulated against control plane NP controller"

    # Temporarily add CRD permissions to the ClusterRole
    kubectl patch clusterrole eks:network-policy-controller --type=json --patch '[{"op": "add", "path": "/rules/-", "value": {"apiGroups": ["apiextensions.k8s.io"], "resources": ["customresourcedefinitions"], "verbs": ["create", "get", "list", "watch"]}}]'
fi

run_cyclonus_tests

# Leadership could have moved back to the control plane during the run, e.g. if the configmap was reconciled
if [[ $DISABLE_CP_NETWORK_POLICY_CONTROLLER == "true" ]]; then
    verify_dataplane_controller_is_leader || TEST_FAILED=true
fi

if [[ $TEST_FAILED == "true" ]]; then
    echo "Test run failed, check failures"
    exit 1
fi
