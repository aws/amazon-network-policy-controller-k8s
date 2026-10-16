## Controller startup scale test

Reproduces the controller failing to start on large clusters. controller-runtime waits for every initial
informer event to go through the event handlers before starting reconcile workers, and gives up after
`CacheSyncTimeout` (2m by default). On large clusters the pod event handler's initial pass takes longer than
that, so the manager exits and the pod ends up in CrashLoopBackOff.

The test:

1. Installs [kwok](https://kwok.sigs.k8s.io) (`kwok.yaml` + `stage-fast.yaml`) if it isn't already installed.
2. Scales the existing controller deployment to 0.
3. Creates kwok nodes, a namespace, 5000 NetworkPolicies and 5000 kwok pods, and waits for every pod to be Running with an IP.
4. Takes the controller's leader lease under a test identity, scales the controller back up, and waits
   `NPC_CACHE_WARMUP` so the caches are fully synced. A leader that wins the lease on a cold start usually
   replays pod events before the NetworkPolicy cache fills, which hides the cost. The warm takeover is the
   failover case, and also what happens on clusters where the policy list syncs before the pod list.
5. Releases the lease, then polls the controller pods, the lease, and PolicyEndpoints.

It passes only if the controller creates PolicyEndpoints for every policy within `NPC_STARTUP_TIMEOUT` and never
restarts. On affected builds it fails and reports the restart reason, the previous container logs, and how long
the leader held the lease before the first PolicyEndpoint appeared.

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
| `NPC_SCALE_PODS` | `5000` | kwok pods to create |
| `NPC_SCALE_POLICIES` | `5000` | NetworkPolicies to create |
| `NPC_SCALE_APP_LABELS` | `50` | distinct `app` labels shared by pods and policy selectors |
| `NPC_SCALE_KWOK_NODES` | `10` | kwok nodes (each gets a /22 pod CIDR) |
| `NPC_SCALE_NAMESPACE` | `npc-startup-scale` | namespace for the test pods and policies |
| `NPC_SCALE_KEEP_RESOURCES` | `false` | skip deleting the namespace and kwok nodes afterwards |
| `NPC_SCALE_CREATE_CONCURRENCY` | `64` | parallel create requests |
| `NPC_STARTUP_TIMEOUT` | `10m` | how long the controller has to reconcile every policy after the lease is released |
| `NPC_CACHE_WARMUP` | `1m` | how long the controller pods sync caches before the lease is released |
| `NPC_NAMESPACE` / `NPC_DEPLOYMENT` | `kube-system` / `amazon-network-policy-controller-k8s` | controller deployment |
| `KWOK_VERSION` | `v0.8.0` | kwok release to install |
| `KWOK_SKIP_INSTALL` | `false` | don't install kwok |
| `KWOK_UNINSTALL` | `false` | uninstall kwok afterwards if the test installed it |

In CI this test runs on the IPv4 leg of the `/test e2e <sha>` PR workflow (`.github/workflows/pr-e2e-tests.yaml`), after the cyclonus suite.
