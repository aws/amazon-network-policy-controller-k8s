#!/bin/bash
# Use this script to deploy the amazon-np-controller deployment on Dataplane nodes

# Parameters:
# KUBECONFIG: path to the kubeconfig file, default ~/.kube/config
# AMAZON_NP_CONTROLLER: node agent image

set -e
DIR=$(cd "$(dirname "$0")"; pwd)

echo "Deploy the default Amazon Network Policy Controller on Dataplane"
kubectl apply -k config/default

if [[ ! -z $AMAZON_NP_CONTROLLER ]];then
    echo "Setting the Controller Image: $AMAZON_NP_CONTROLLER"
    kubectl set image deployment.v1.apps/amazon-network-policy-controller-k8s controller=$AMAZON_NP_CONTROLLER
fi

echo "Restarting the Controller"
kubectl rollout restart deployment.v1.apps/amazon-network-policy-controller-k8s -n kube-system

echo "Ensuring Controller is Running on Dataplane"
kubectl rollout status deployment.v1.apps/amazon-network-policy-controller-k8s -n kube-system --timeout=2m || (echo "Amazon Network Policy controller is unhealthy" && exit 1)
