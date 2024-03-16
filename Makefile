# Copyright Amazon.com Inc. or its affiliates. All Rights Reserved.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#    http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# Image URL to use all building/pushing image targets
IMG ?= public.ecr.aws/eks/amazon-network-policy-controller-k8s:v1.0.2
# ENVTEST_K8S_VERSION refers to the version of kubebuilder assets to be downloaded by envtest binary.
ENVTEST_K8S_VERSION = 1.26.1
# ARCHS define the target architectures for the controller image be build
ARCHS ?= amd64 arm64
# IMG_SBOM defines the SBOM media type to use, we set to none since ECR doesn't support it yet
IMG_SBOM ?= none

# Disable the control plane network policy controller
DISABLE_CP_NETWORK_POLICY_CONTROLLER=false

# Get the currently used golang install path (in GOPATH/bin, unless GOBIN is set)
ifeq (,$(shell go env GOBIN))
GOBIN=$(shell go env GOPATH)/bin
else
GOBIN=$(shell go env GOBIN)
endif

# Setting SHELL to bash allows bash commands to be executed by recipes.
# Options are set to exit when a recipe line exits non-zero or a piped command fails.
SHELL = /usr/bin/env bash -o pipefail
.SHELLFLAGS = -ec

.PHONY: all
all: build

##@ General

# The help target prints out all targets with their descriptions organized
# beneath their categories. The categories are represented by '##@' and the
# target descriptions by '##'. The awk commands is responsible for reading the
# entire set of makefiles included in this invocation, looking for lines of the
# file as xyz: ## something, and then pretty-format the target and help. Then,
# if there's a line with ##@ something, that gets pretty-printed as a category.
# More info on the usage of ANSI control characters for terminal formatting:
# https://en.wikipedia.org/wiki/ANSI_escape_code#SGR_parameters
# More info on the awk command:
# http://linuxcommand.org/lc3_adv_awk.php

.PHONY: help
help: ## Display this help.
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n"} /^[a-zA-Z_0-9-]+:.*?##/ { printf "  \033[36m%-15s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) } ' $(MAKEFILE_LIST)

##@ Development

.PHONY: manifests
manifests: controller-gen ## Generate WebhookConfiguration, ClusterRole and CustomResourceDefinition objects.
	$(CONTROLLER_GEN) rbac:roleName=controller-k8s crd webhook paths="./..." output:crd:artifacts:config=config/crd/bases

.PHONY: generate
generate: controller-gen mockgen ## Generate code containing DeepCopy, DeepCopyInto, and DeepCopyObject method implementations.
	$(CONTROLLER_GEN) object:headerFile="hack/boilerplate.go.txt" paths="./..."
	MOCKGEN=$(MOCKGEN) ./scripts/gen_mocks.sh

.PHONY: fmt
fmt: ## Run go fmt against code.
	go fmt ./...

.PHONY: vet
vet: ## Run go vet against code.
	go vet ./...

.PHONY: test
test: manifests generate fmt vet envtest ## Run tests.
	KUBEBUILDER_ASSETS="$(shell $(ENVTEST) use $(ENVTEST_K8S_VERSION) --bin-dir $(LOCALBIN) -p path)" go test ./... -coverprofile cover.out

##@ Build

.PHONY: build
build: manifests generate fmt vet ## Build controller binary.
	go build -o bin/controller cmd/main.go

.PHONY: run
run: manifests generate fmt vet ## Run a controller from your host.
	go run ./cmd/main.go

.PHONY: docker-push
docker-push: image-push

.PHONY: image-push
image-push: ko
	KO_DOCKER_REPO=$(firstword $(subst :, ,${IMG})) \
	GIT_VERSION=$(shell git describe --tags --dirty --always) \
	GIT_COMMIT=$(shell git rev-parse HEAD)  \
	BUILD_DATE=$(shell date +%Y-%m-%dT%H:%M:%S%z) \
	$(KO) build --tags $(word 2,$(subst :, ,${IMG})) --platform=${PLATFORM} --bare --sbom ${IMG_SBOM} ./cmd

##@ Deployment

ifndef ignore-not-found
  ignore-not-found = false
endif

.PHONY: install
install: manifests kustomize ## Install CRDs into the K8s cluster specified in ~/.kube/config.
	$(KUSTOMIZE) build config/crd | kubectl apply -f -

.PHONY: uninstall
uninstall: manifests kustomize ## Uninstall CRDs from the K8s cluster specified in ~/.kube/config. Call with ignore-not-found=true to ignore resource not found errors during deletion.
	$(KUSTOMIZE) build config/crd | kubectl delete --ignore-not-found=$(ignore-not-found) -f -

.PHONY: deploy
deploy: manifests kustomize ## Deploy controller to the K8s cluster specified in ~/.kube/config.
	cd config/controller && $(KUSTOMIZE) edit set image controller=${IMG}
	$(KUSTOMIZE) build config/default | kubectl apply -f -

.PHONY: undeploy
undeploy: ## Undeploy controller from the K8s cluster specified in ~/.kube/config. Call with ignore-not-found=true to ignore resource not found errors during deletion.
	$(KUSTOMIZE) build config/default | kubectl delete --ignore-not-found=$(ignore-not-found) -f -

##@ Build Dependencies

## Location to install dependencies to
LOCALBIN ?= $(shell pwd)/bin
$(LOCALBIN):
	mkdir -p $(LOCALBIN)

## Tool Binaries
KUSTOMIZE ?= $(LOCALBIN)/kustomize
CONTROLLER_GEN ?= $(LOCALBIN)/controller-gen
ENVTEST ?= $(LOCALBIN)/setup-envtest
KO ?= $(LOCALBIN)/ko
MOCKGEN ?= $(LOCALBIN)/mockgen

## Tool Versions
KUSTOMIZE_VERSION ?= v5.0.0
CONTROLLER_TOOLS_VERSION ?= v0.11.3

KUSTOMIZE_INSTALL_SCRIPT ?= "https://raw.githubusercontent.com/kubernetes-sigs/kustomize/master/hack/install_kustomize.sh"
.PHONY: kustomize
kustomize: $(KUSTOMIZE) ## Download kustomize locally if necessary. If wrong version is installed, it will be removed before downloading.
$(KUSTOMIZE): $(LOCALBIN)
	@if test -x $(LOCALBIN)/kustomize && ! $(LOCALBIN)/kustomize version | grep -q $(KUSTOMIZE_VERSION); then \
		echo "$(LOCALBIN)/kustomize version is not expected $(KUSTOMIZE_VERSION). Removing it before installing."; \
		rm -rf $(LOCALBIN)/kustomize; \
	fi
	test -s $(LOCALBIN)/kustomize || { curl -Ss $(KUSTOMIZE_INSTALL_SCRIPT) --output install_kustomize.sh && bash install_kustomize.sh $(subst v,,$(KUSTOMIZE_VERSION)) $(LOCALBIN); rm install_kustomize.sh; }

.PHONY: controller-gen
controller-gen: $(CONTROLLER_GEN) ## Download controller-gen locally if necessary. If wrong version is installed, it will be overwritten.
$(CONTROLLER_GEN): $(LOCALBIN)
	test -s $(LOCALBIN)/controller-gen && $(LOCALBIN)/controller-gen --version | grep -q $(CONTROLLER_TOOLS_VERSION) || \
	GOBIN=$(LOCALBIN) go install sigs.k8s.io/controller-tools/cmd/controller-gen@$(CONTROLLER_TOOLS_VERSION)

.PHONY: envtest
envtest: $(ENVTEST) ## Download envtest-setup locally if necessary.
$(ENVTEST): $(LOCALBIN)
	test -s $(LOCALBIN)/setup-envtest || GOBIN=$(LOCALBIN) go install sigs.k8s.io/controller-runtime/tools/setup-envtest@latest

.PHONY: ko
ko: $(KO)
$(KO): $(LOCALBIN)
	test -s $(LOCALBIN)/ko || GOBIN=$(LOCALBIN) go install github.com/google/ko@v0.11.2

.PHONY: mockgen
mockgen: $(MOCKGEN)
$(MOCKGEN): $(LOCALBIN)
	test -s $(MOCKGEN) || GOBIN=$(LOCALBIN) go install github.com/golang/mock/mockgen@v1.6.0

GO_IMAGE_TAG=$(shell cat .go-version)
BUILD_IMAGE=public.ecr.aws/docker/library/golang:$(GO_IMAGE_TAG)
BASE_IMAGE=public.ecr.aws/eks-distro-build-tooling/eks-distro-minimal-base-nonroot:latest.2
GO_RUNNER_IMAGE=public.ecr.aws/eks-distro/kubernetes/go-runner:v0.15.0-eks-1-27-3
.PHONY: docker-buildx
docker-buildx: test
	for platform in $(ARCHS); do \
		docker buildx build --platform=linux/$$platform -t $(IMG)-$$platform --build-arg BASE_IMAGE=$(BASE_IMAGE) --build-arg BUILD_IMAGE=$(BUILD_IMAGE) --build-arg $$platform --load .; \
	done

# Check formatting of source code files without modification.
check-format: FORMAT_FLAGS = -l
check-format: format

format:       ## Format all Go source code files.
	@command -v goimports >/dev/null || { echo "ERROR: goimports not installed"; exit 1; }
	@exit $(shell find ./* \
	  -type f \
	  -name '*.go' \
	  -print0 | sort -z | xargs -0 -- goimports $(or $(FORMAT_FLAGS),-w) | wc -l | bc)

run-cyclonus-test: ## Runs cyclonus tests on an existing cluster. Call with CLUSTER_NAME=<name of your cluster> to execute cyclonus test
ifdef CLUSTER_NAME
	CLUSTER_NAME=$(CLUSTER_NAME) ./scripts/run-cyclonus-tests.sh
else
	@echo 'Pass CLUSTER_NAME parameter'
endif

./PHONY: deploy-controller-on-dataplane
deploy-controller-on-dataplane: ## Deploys the Network Policy controller on an existing cluster. Optionally call with NP_CONTROLLER_IMAGE=<Image URI> to update the image
	./scripts/deploy-controller-on-dataplane.sh NP_CONTROLLER_IMAGE=$(NP_CONTROLLER_IMAGE)

./PHONY: deploy-and-test
deploy-and-test: ## Deploys the Network Policy controller on an existing cluster and runs cyclonus tests. Call with CLUSTER_NAME=<name of the cluster> and NP_CONTROLLER_IMAGE=<Image URI> 
	$(MAKE) deploy-controller-on-dataplane NP_CONTROLLER_IMAGE=$(NP_CONTROLLER_IMAGE)
	$(MAKE) run-cyclonus-test CLUSTER_NAME=$(CLUSTER_NAME)
