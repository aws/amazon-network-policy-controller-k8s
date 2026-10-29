## Scripts

This package contains shell scripts and libraries used for running e2e tests and some helper scripts.

`run-cyclonus-tests.sh` Runs cyclonus tests against an existing cluster and validates the output
`update-controller-image-dataplane.sh` Deploys the `amazon-network-policy-controller-k8s` controller on Dataplane

### Cyclonus tests
`run-cyclonus-tests.sh` script runs the cyclonus suite against an existing cluster. It provides the option of disabling the control plane `amazon-network-policy-controller-k8s` controller if you are deploying a custom/dev version of the controller installed on dataplane. The script also skips CNI installation if `SKIP_CNI_INSTALLATION` environment variable is set. 
Use `make run-cyclonus-test` to run this script

### Deploy Controller on Dataplane
`update-controller-image-dataplane.sh` script helps in installing the Dataplane manifests for `amazon-network-policy-controller-k8s` controller. It provides the option to run a custom/dev image of the controller on dataplane if you set `AMAZON_NP_CONTROLLER` to the image URI.
Use `make deploy-controller-on-dataplane` action to run this script or `make deploy-and-test` to use run this script and cyclonus suite.

`deploy-controller-on-dataplane.sh` sets `enable-network-policy-controller: "false"` in the `kube-system/amazon-vpc-cni` configmap, which makes the control plane controller stop competing for the leader lease, and runs the dataplane controller with `--enable-configmap-check=false` so it ignores that key. Both scripts then fail unless a dataplane controller pod holds the `kube-system/amazon-network-policy-controller-k8s` lease; `run-cyclonus-tests.sh` checks again after the suite, so a run that drifted back to the control plane controller fails instead of passing against the wrong controller.

### PR e2e workflow
`.github/workflows/pr-e2e-tests.yaml` runs on a PR when someone with write access comments `/test e2e <full PR head SHA>`. It builds and pushes the controller image to ECR, then, for each IP family, creates an EKS cluster with `eks-test-cluster.sh create`, deploys the image with `deploy-controller-on-dataplane.sh`, and runs the cyclonus suite. The IPv4 leg also runs the controller startup scale test (`test/e2e/startup`). Results are posted back to the PR, and the clusters are deleted with `eks-test-cluster.sh delete` whether the tests pass or fail.

The workflow needs the `OSS_ROLE_ARN` repository secret: an IAM role trusted for GitHub OIDC from this repository (`repo:aws/amazon-network-policy-controller-k8s:*`), with permissions for EKS/eksctl (CloudFormation, EC2, IAM) and ECR push to `amazon/amazon-network-policy-controller-k8s`. The workflow does not create that ECR repository; create it once in the CI account and region:

```bash
aws ecr create-repository --repository-name amazon/amazon-network-policy-controller-k8s --region us-west-2
``` `TEST_IMAGE_REGISTRY` is optional and points cyclonus at a mirror of its test images.
