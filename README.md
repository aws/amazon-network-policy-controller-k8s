## Amazon Network Policy Controller for Kubernetes

Controller for Kubernetes NetworkPolicy resources.

Network Policy Controller resolves the configured network policies and publishes the resolved endpoints via Custom CRD (`PolicyEndpoints`) resource.

## Getting Started

When you create a new Amazon EKS cluster, the network policy controller is automatically installed on the EKS control plane. It actively monitors the creation of network policies within your cluster and reconciles policy endpoints. Subsequently, the controller instructs the node agent to create or update eBPF programs on the node by publishing pod information through the policy endpoints. Network policy controller configures policies for pods in parallel to pod provisioning, until then new pods will come up with default allow policy. All ingress and egress traffic is allowed to and from the new pods until they are reconciled against the existing policies. To effectively manage network policies on self-managed Kubernetes clusters, you need to deploy a network policy controller on a node.

Stay tuned for additional instructions for installing Network Policy Controller on nodes. The controller image is published to AWS ECR.

The controller does not require any IAM policies. It does not make AWS API calls. 

### Prerequisites

- Kubernetes Version - 1.25+
- Amazon VPC CNI version - 1.14.0+

## Deploy Controller on Dataplane for Development Testing

To deploy the network policy controller on dataplane nodes for development and testing:

1. **Deploy the controller:**
This will deploy the image specified in helm chart
   ```bash
   make deploy-controller-on-dataplane
   ```
If want to deploy a custom image, you can use the cmd
   ```bash
   make deploy-controller-on-dataplane NP_CONTROLLER_IMAGE=<your-image-repository> NP_CONTROLLER_TAG=<your-image-tag>
   ```
Verify the image deployed
```bash
kubectl get deployment amazon-network-policy-controller-k8s -n kube-system | grep -i image
```

**Optional steps (only needed if using custom images with additional CRDs/permissions):**

2. **Apply updated RBAC permissions:**
   ```bash
   kubectl apply -f config/rbac/role.yaml
   ```

3. **Apply latest CRDs:**
   ```bash
   kubectl apply -f config/crd/bases/
   ```

4. **Restart controller to pick up new permissions:**
   ```bash
   kubectl rollout restart deployment/amazon-network-policy-controller-k8s -n kube-system
   ```

**Verify deployment:**
```bash
kubectl get deployment amazon-network-policy-controller-k8s -n kube-system
kubectl logs deployment/amazon-network-policy-controller-k8s -n kube-system
```
## Considerations

### NetworkPolicy `podSelector` must match the target Service's selector

When a pod reaches another pod through its Service ClusterIP, the network policy controller resolves the traffic through the Service IP (pre-DNAT). To allow this traffic with a NetworkPolicy, the `podSelector` in the policy must select pods using the **same labels the Service uses in its `spec.selector`**.

Pods often carry several labels, and a Service may select them by only one of those labels. If a NetworkPolicy targets the pod using a *different* label than the one the Service selects on, traffic sent to the ClusterIP is not matched and is denied, even though the label technically matches the destination pod. Traffic sent directly to the pod IP is unaffected. See [issue #224](https://github.com/aws/amazon-network-policy-controller-k8s/issues/224) for background.

## Security Disclosures 

If you think you’ve found a potential security issue, please do not post it in the Issues. Instead, please follow the
instructions [here](https://aws.amazon.com/security/vulnerability-reporting/) or [email AWS security directly](mailto:aws-security@amazon.com).

## Contributing

See [CONTRIBUTING](CONTRIBUTING.md#security-issue-notifications) for further details.

## License

This project is licensed under the Apache-2.0 License.

