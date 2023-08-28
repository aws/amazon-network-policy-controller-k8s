## Amazon Network Policy Controller for Kubernetes

Kubernetes controller for NetworkPolicy resources for the [Amazon VPC CNI](https://github.com/aws/amazon-vpc-cni-k8s/).

This controller resolves the pod addresses for the configured network policies and publishes them via the CustomResourceDefinition `policyendpoints.networking.k8s.aws` for the VPC CNI node agent to consume.

📝 EKS Customers do not need to install this controller. Review the instructions in the [EKS User Guide](https://docs.aws.amazon.com/eks/latest/userguide/cni-network-policy.html). EKS installs and manages it automatically. This cluster is for self managed clusters, such as [kops](https://kops.sigs.k8s.io) clusters.

## Getting Started

The controller image is published to AWS ECR.
The directory `config/default` contains a default configuration for deploying the controller. 

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

