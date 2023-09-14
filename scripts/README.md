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
