## Controller startup scale test

Reproduces the controller failing to start on large clusters. controller-runtime waits for every initial
informer event to go through the event handlers before starting reconcile workers, and gives up after
`CacheSyncTimeout` (2m by default). On large clusters the pod event handler's initial pass takes longer than
that, so the manager exits and the pod ends up in CrashLoopBackOff.

The test:

1. Installs [kwok](https://kwok.sigs.k8s.io) (`kwok.yaml` + `stage-fast.yaml`) if it isn't already installed.
2. Scales the existing controller deployment to 0.
3. Creates kwok nodes and the workload of the selected scenario (10k kwok pods and 10k policies by default, spread
   over namespaces, with services, ApplicationNetworkPolicies and ClusterNetworkPolicies depending on the scenario),
   and waits for every pod to be Running with an IP.
4. Takes the controller's leader lease under a test identity, scales the controller back up, and waits
   `NPC_CACHE_WARMUP` so the caches are fully synced. A leader that wins the lease on a cold start usually
   replays pod events before the NetworkPolicy cache fills, which hides the cost. The warm takeover is the
   failover case, and also what happens on clusters where the policy list syncs before the pod list.
5. Releases the lease, then polls the controller pods, the lease, PolicyEndpoints and ClusterPolicyEndpoints, and the
   leader's metrics (reconciles, queue depth, memory).

It passes only if the controller never restarts, creates its first PolicyEndpoint within `NPC_FIRST_RECONCILE_TIMEOUT`
of becoming leader, and creates endpoints for every policy within `NPC_STARTUP_TIMEOUT`. On affected builds it fails
and reports the restart reason, the previous container logs, and how long the leader held the lease before the first
PolicyEndpoint appeared.

### Scenarios

`NPC_SCALE_SCENARIO` picks the shape of the workload; each one exercises a different controller code path (see
`scenarios.go`). The individual `NPC_SCALE_*` variables below override the preset.

| Scenario | Shape |
|---|---|
| `baseline` | one namespace, policies select pods by app label, 10% use a namespaceSelector peer |
| `multi-namespace` | the same policies spread over 50 namespaces in 5 groups |
| `namespace-selector` | every policy references the pods of another namespace group |
| `broad` | adds 50 policies that select every pod and allow egress to every test pod |
| `services` | adds 10 services per namespace behind the egress targets |
| `anp` | every policy is an ApplicationNetworkPolicy |
| `cnp` | adds 50 ClusterNetworkPolicies |
| `ipblock` | every policy has 50 ipBlock egress peers |
| `mixed` (default) | all of the above at lower ratios |

The test uses the controller deployment that is already in the cluster. It must be able to win the leader lease,
so disable the EKS control plane controller (`enable-network-policy-controller: "false"` in the `amazon-vpc-cni`
ConfigMap) and run without `--enable-configmap-check=true`. `deploy-controller-on-dataplane.sh` does both; the
test fails fast if either is missing.

```bash
NP_CONTROLLER_IMAGE=<repo> NP_CONTROLLER_TAG=<tag> ./scripts/deploy-controller-on-dataplane.sh
make run-startup-scale-test
```

| Variable | Default | Description |
|---|---|---|
| `NPC_SCALE_SCENARIO` | `mixed` | workload preset, see above |
| `NPC_SCALE_PODS` | `10000` | kwok pods to create |
| `NPC_SCALE_POLICIES` | `10000` | NetworkPolicies and ApplicationNetworkPolicies to create |
| `NPC_SCALE_APP_LABELS` | `50` | distinct `app` labels per namespace shared by pods and policy selectors |
| `NPC_SCALE_KWOK_NODES` | `20` | kwok nodes (each gets a /22 pod CIDR and holds up to 1000 pods) |
| `NPC_SCALE_NAMESPACE` | `npc-startup-scale` | test namespace, or the prefix of the test namespaces when there are several |
| `NPC_SCALE_NAMESPACES` / `NPC_SCALE_NAMESPACE_GROUPS` | preset | namespaces, and the groups namespace selectors pick from |
| `NPC_SCALE_NS_SELECTOR_PERCENT` | preset | percent of policies with a namespaceSelector peer |
| `NPC_SCALE_BROAD_POLICIES` | preset | extra select-everything policies |
| `NPC_SCALE_SERVICES_PER_NAMESPACE` | preset | services per namespace |
| `NPC_SCALE_ANP_PERCENT` | preset | percent of policies created as ApplicationNetworkPolicies |
| `NPC_SCALE_CNPS` | preset | ClusterNetworkPolicies |
| `NPC_SCALE_IPBLOCKS_PER_POLICY` | preset | ipBlock egress peers per policy |
| `NPC_SCALE_KEEP_RESOURCES` | `false` | skip deleting the namespace and kwok nodes afterwards |
| `NPC_SCALE_CREATE_CONCURRENCY` | `64` | parallel create requests |
| `NPC_STARTUP_TIMEOUT` | `20m` | how long the controller has to reconcile every policy after the lease is released |
| `NPC_FIRST_RECONCILE_TIMEOUT` | `3m` | how long the leader may hold the lease before creating its first PolicyEndpoint |
| `NPC_CACHE_WARMUP` | `1m` | how long the controller pods sync caches before the lease is released |
| `NPC_NAMESPACE` / `NPC_DEPLOYMENT` | `kube-system` / `amazon-network-policy-controller-k8s` | controller deployment |
| `KWOK_VERSION` | `v0.8.0` | kwok release to install |
| `KWOK_SKIP_INSTALL` | `false` | don't install kwok |
| `KWOK_UNINSTALL` | `false` | uninstall kwok afterwards if the test installed it |

In CI this test runs on the IPv4 leg of the `/test e2e <sha>` PR workflow (`.github/workflows/pr-e2e-tests.yaml`), after the cyclonus suite.
