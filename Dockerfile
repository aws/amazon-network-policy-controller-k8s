ARG BASE_IMAGE
ARG BUILD_IMAGE
ARG ARCH=amd64
# Build the controller binary
FROM $BUILD_IMAGE as builder

WORKDIR /workspace
ENV GOPROXY direct

# Copy the Go Modules manifests
COPY go.mod go.mod
COPY go.sum go.sum
# cache deps before building and copying source so that we don't need to re-download as much
# and so that source changes don't invalidate our downloaded layer
RUN go mod download

# Copy the go source
COPY .git/ .git/
COPY cmd/main.go cmd/main.go
COPY api/ api/
COPY pkg/ pkg/
COPY internal/ internal/

# Version package for passing the ldflags
# TODO: change this to network controller's version
ENV VERSION_PKG=github.com/aws/amazon-network-policy-controller-k8s/pkg/version
# Build
RUN GIT_VERSION=$(git describe --tags --always) && \
        GIT_COMMIT=$(git rev-parse HEAD) && \
        BUILD_DATE=$(date +%Y-%m-%dT%H:%M:%S%z) && \
        CGO_ENABLED=0 GOOS=linux GOARCH=${ARCH} GO111MODULE=on go build \
        -ldflags="-X ${VERSION_PKG}.GitVersion=${GIT_VERSION} -X ${VERSION_PKG}.GitCommit=${GIT_COMMIT} -X ${VERSION_PKG}.BuildDate=${BUILD_DATE}" -a -o controller cmd/main.go

FROM $BASE_IMAGE

WORKDIR /
COPY --from=public.ecr.aws/eks-distro/kubernetes/go-runner:v0.18.0-eks-1-32-11 /go-runner /usr/local/bin/go-runner
COPY --from=builder /workspace/controller .
USER 65532:65532

ENTRYPOINT ["/controller"]
