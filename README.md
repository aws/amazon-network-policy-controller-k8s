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

## Security Disclosures 

If you think you’ve found a potential security issue, please do not post it in the Issues. Instead, please follow the
instructions [here](https://aws.amazon.com/security/vulnerability-reporting/) or [email AWS security directly](mailto:aws-security@amazon.com).

## Contributing

See [CONTRIBUTING](CONTRIBUTING.md#security-issue-notifications) for further details.

## License

This project is licensed under the Apache-2.0 License.

