#!/usr/bin/env bash

# Runs focused regressions on an existing cluster that has both the Network
# Policy Controller and Network Policy Agent installed. This script creates
# only E2E_NAMESPACE and removes it on exit.
#
# Required: KUBECONFIG must identify a cluster where the candidate CRDs and
# controller image have been installed.
#
# Optional:
#   E2E_NAMESPACE  namespace to create (default: npc-regression-e2e)
#   E2E_TIMEOUT    timeout passed to kubectl wait (default: 180s)

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
readonly SCRIPT_DIR
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
readonly REPO_ROOT
readonly FIXTURES_DIR="${REPO_ROOT}/tests/e2e"
readonly E2E_NAMESPACE="${E2E_NAMESPACE:-npc-regression-e2e}"
readonly E2E_TIMEOUT="${E2E_TIMEOUT:-180s}"

fail() {
    echo "ERROR: $*" >&2
    exit 1
}

cleanup() {
    kubectl delete namespace "${E2E_NAMESPACE}" --ignore-not-found --wait=false >/dev/null 2>&1 || true
}

require_command() {
    command -v "$1" >/dev/null 2>&1 || fail "required command not found: $1"
}

wait_for_policy_endpoint() {
    local deadline=$((SECONDS + ${E2E_TIMEOUT%s}))

    while (( SECONDS < deadline )); do
        if kubectl get policyendpoint -n "${E2E_NAMESPACE}" -o json |
            jq -e '
                [.items[]
                 | select(.spec.policyRef.name == "ports-only-egress")
                 | .spec.egress[]?] as $egress
                | ([ $egress[]
                     | select(.cidr == "0.0.0.0/0")
                     | .ports[]?
                     | select(.protocol == "UDP" and .port == 53) ] | length == 1)
                  and
                  ([ $egress[]
                     | select(.cidr == "0.0.0.0/0")
                     | .ports[]?
                     | select(.protocol == "TCP" and .port == 53) ] | length == 1)
                  and
                  ([ $egress[]
                     | select(.cidr == "::/0")
                     | .ports[]?
                     | select(.protocol == "UDP" and .port == 53) ] | length == 1)
                  and
                  ([ $egress[]
                     | select(.cidr == "::/0")
                     | .ports[]?
                     | select(.protocol == "TCP" and .port == 53) ] | length == 1)
                  and
                  ([ $egress[]
                     | select(.domainName == "example.com")
                     | .ports[]?
                     | select(.protocol == "TCP" and .port == 443) ] | length == 1)
            ' >/dev/null; then
            return
        fi
        sleep 2
    done

    kubectl get policyendpoint -n "${E2E_NAMESPACE}" -o yaml >&2 || true
    fail "timed out waiting for the expected PolicyEndpoint egress rules"
}

require_command kubectl
require_command jq

[[ -n "${KUBECONFIG:-}" ]] || fail "set KUBECONFIG to the target cluster before running this test"
kubectl auth can-i create namespaces | grep -qx yes || fail "current identity cannot create namespaces"
kubectl get crd applicationnetworkpolicies.networking.k8s.aws >/dev/null ||
    fail "ApplicationNetworkPolicy CRD is not installed"

if kubectl get namespace "${E2E_NAMESPACE}" >/dev/null 2>&1; then
    fail "namespace ${E2E_NAMESPACE} already exists; choose E2E_NAMESPACE or remove the stale test namespace"
fi

trap cleanup EXIT

echo "Creating isolated test namespace ${E2E_NAMESPACE}"
kubectl create namespace "${E2E_NAMESPACE}"

echo "Checking CRD domain-name validation"
kubectl -n "${E2E_NAMESPACE}" apply --dry-run=server -f "${FIXTURES_DIR}/valid-domain.yaml" >/dev/null
if kubectl -n "${E2E_NAMESPACE}" apply --dry-run=server -f "${FIXTURES_DIR}/invalid-domain.yaml" >/dev/null 2>&1; then
    fail "CRD accepted ^x.example.com; expected the domain-name validation to reject it"
fi

echo "Applying ports-only egress policy"
kubectl -n "${E2E_NAMESPACE}" apply -f "${FIXTURES_DIR}/anp-ports-only-egress.yaml"
kubectl -n "${E2E_NAMESPACE}" wait --for=condition=Ready pod/client --timeout="${E2E_TIMEOUT}"

echo "Waiting for the controller's PolicyEndpoint output"
wait_for_policy_endpoint

echo "Validating DNS, allowed TCP/443, and denied TCP/80"
kubectl -n "${E2E_NAMESPACE}" exec client -- nslookup example.com >/dev/null
kubectl -n "${E2E_NAMESPACE}" exec client -- nc -z -w 10 example.com 443
if kubectl -n "${E2E_NAMESPACE}" exec client -- nc -z -w 10 example.com 80; then
    fail "TCP/80 unexpectedly succeeded; only TCP/443 is allowed for example.com"
fi

echo "PASS: NPC ports-only egress and domain-name CRD regressions"
