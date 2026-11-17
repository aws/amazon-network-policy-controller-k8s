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
#   E2E_TIMEOUT    positive whole-second timeout, e.g. 180s (default: 180s)

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
readonly SCRIPT_DIR
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
readonly REPO_ROOT
readonly FIXTURES_DIR="${REPO_ROOT}/tests/e2e"
readonly E2E_NAMESPACE="${E2E_NAMESPACE:-npc-regression-e2e}"
E2E_TIMEOUT="${E2E_TIMEOUT:-180s}"

fail() {
    echo "ERROR: $*" >&2
    exit 1
}

if [[ ! "${E2E_TIMEOUT}" =~ ^[1-9][0-9]*s$ ]]; then
    fail "E2E_TIMEOUT must be a positive whole-second duration such as 180s"
fi
readonly E2E_TIMEOUT
readonly E2E_TIMEOUT_SECONDS="${E2E_TIMEOUT%s}"

cleanup() {
    # Delete the finalizer-bearing ANP while the candidate controller is still
    # running. Waiting here prevents a caller that restores the controller
    # immediately after this script exits from leaving the test namespace in
    # Terminating state.
    kubectl -n "${E2E_NAMESPACE}" delete applicationnetworkpolicy ports-only-egress \
        --ignore-not-found --wait=true --timeout="${E2E_TIMEOUT}" >/dev/null 2>&1 || true
    kubectl delete namespace "${E2E_NAMESPACE}" \
        --ignore-not-found --wait=true --timeout="${E2E_TIMEOUT}" >/dev/null 2>&1 || true
}

require_command() {
    command -v "$1" >/dev/null 2>&1 || fail "required command not found: $1"
}

wait_for_policy_endpoint() {
    local deadline=$((SECONDS + E2E_TIMEOUT_SECONDS))

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

wait_for_network_policy_agent() {
    local deadline=$((SECONDS + E2E_TIMEOUT_SECONDS))

    while (( SECONDS < deadline )); do
        if kubectl -n "${E2E_NAMESPACE}" exec client -- nslookup example.com >/dev/null 2>&1 &&
            kubectl -n "${E2E_NAMESPACE}" exec client -- nc -z -w 2 example.com 443 >/dev/null 2>&1 &&
            ! kubectl -n "${E2E_NAMESPACE}" exec client -- nc -z -w 2 example.com 80 >/dev/null 2>&1; then
            return
        fi
        sleep 2
    done

    fail "timed out waiting for Network Policy Agent enforcement: expected DNS and TCP/443 allowed, TCP/80 denied"
}

require_command kubectl
require_command jq

[[ -n "${KUBECONFIG:-}" ]] || fail "set KUBECONFIG to the target cluster before running this test"
kubectl auth can-i create namespaces 2>/dev/null | grep -qx yes || fail "current identity cannot create namespaces"
kubectl get crd applicationnetworkpolicies.networking.k8s.aws >/dev/null ||
    fail "ApplicationNetworkPolicy CRD is not installed"

if kubectl get namespace "${E2E_NAMESPACE}" >/dev/null 2>&1; then
    fail "namespace ${E2E_NAMESPACE} already exists; choose E2E_NAMESPACE or remove the stale test namespace"
fi

echo "Creating isolated test namespace ${E2E_NAMESPACE}"
kubectl create namespace "${E2E_NAMESPACE}"
trap cleanup EXIT

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
wait_for_network_policy_agent

echo "PASS: NPC ports-only egress and domain-name CRD regressions"
