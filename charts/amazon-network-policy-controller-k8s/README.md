# AMAZON NETWORK POLICY CONTROLLER

This chart provides a Kubernetes deployment for the Amazon Network Policy Controller

## Prerequisites

- Kubernetes 1.24+ running on AWS
- Helm 3.0+

## Installing the Chart

To install the chart with the release name `amazon-network-policy-controller-k8s` and default configuration:

```shell
$ helm install amazon-network-policy-controller-k8s --namespace kube-system charts/amazon-network-policy-controller-k8s
```


## Configuration

The following table lists the configurable parameters for this chart and their default values.

| Parameter                    | Description                                                   | Default                                                 |
|------------------------------|---------------------------------------------------------------|---------------------------------------------------------|
| fullnameOverride             | Override the fullname of the chart                            | amazon-network-policy-controller-k8s                    |
| nameOverride                 | override for the name of the Helm Chart                       | amazon-network-policy-controller-k8s                    |
| image.repository             | ECR repository to use. Should match your cluster              | public.ecr.aws/eks/amazon-network-policy-controller-k8s |
| image.tag                    | Image tag                                                     | v1.0.4                                                  |
| enableConfigMapCheck         | Enable configmap check to enable/disable controller in Control Plane | false                                            |
| endpointChunkSize            | Number of endpoints to include in a single policy endpoints resource | 1000                                             |
| maxConcurrentReconciles      | Maximum number of concurrent reconcile loops                  | 3                                                       |
| podUpdateBatchPeriodDuration | Duration between batch updates of pods in seconds             | 1                                                       |
| livenessProbe                | Liveness Probe configuration for controller                   | see `values.yaml`                                       |
| readinessProbe               | Readiness Probe configuration for controller                  | see `values.yaml`                                       |

Specify each parameter using the `--set key=value[,key=value]` argument to `helm install` or provide a YAML file containing the values for the above parameters:

```shell
$ helm install amazon-network-policy-controller-k8s --namespace kube-system ./charts/amazon-network-policy-controller-k8s --values values.yaml
```
