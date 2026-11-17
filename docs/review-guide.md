# Review guide for amazon-network-policy-controller-k8s

## 1. Purpose and how to use it

This guide lists the problems maintainers keep finding in review, so authors and reviewers can find them first. It covers four areas: the contracts the controller depends on but does not enforce (the informer cache, the PolicyEndpoint API the node agent reads, CRD rollout), the PolicyEndpoint write path, existing precedents to reuse, and recurring review themes.

An item is tagged `[blocking]` when, whenever its condition holds, the change causes one of these:

- **Wrong enforcement, even briefly.** A PolicyEndpoint (PE) or ClusterPolicyEndpoint (CPE) allows traffic the policy denies or denies traffic it allows. This includes a pod missing from, or wrongly listed in, `PodSelectorEndpoints`: the node agent isolates only the pods listed there. The delay between a policy, pod, namespace or Service event and the reconcile that applies it is how the controller works, not a defect. A change that writes a PE the policy doesn't mean, or makes that delay longer, is blocking even if a later reconcile corrects it.
- **A state that doesn't self-correct.** A PE, CPE or finalizer stays wrong after the next reconcile of its policy, until an unrelated event, a restart or manual action: for example a leaked PE, or a policy that can't be deleted.
- **Unbounded growth.** PE count, controller memory, or API writes that grow without bound, or with the number of events rather than the number of policies.
- **A controller that can't start or stay up.** A crash, or an informer sync that exceeds `CacheSyncTimeout` on large clusters.
- **A broken upgrade or rollback.** Enforcement is still wrong after the new or previous controller version has run, or the node agent on the nodes misreads what the controller writes.

It is `[advisory]` if the harm doesn't change enforcement and is transient (extra writes, latency or log volume that the next reconcile, requeue or event clears), needs an unusual precondition, or is about quality, noise, tests or docs. Wrong enforcement is never advisory because it is temporary. Each blocking item has a "Blocking when:" clause, and some advisory items a "Blocking only if:" clause, that names the condition that makes an instance blocking. Report blocking findings first and keep advisory ones brief. The tag is the item's worst case, so judge each finding's own severity by whether it changes enforcement, its reach, and whether it self-corrects, and don't copy the tag.

Run it against a diff. For each changed area, look up the matching checks:

| If the diff touches | Read |
|---|---|
| `pkg/resolvers/policies*.go`, `policy_tracker.go`, `internal/eventhandlers/` | [Event handlers and the resolver](#ck-events), [K1](#k1), [K4](#k4), [P1](#p-handler) |
| `pkg/config/runtime_config.go`, `pkg/k8s/*_utils.go` (transforms), any new `Get`/`List` | [Informer cache](#ck-cache), [K1](#k1), [K2](#k2), [K3](#k3) |
| `pkg/resolvers/endpoints.go`, `*_resolver.go` | [Resolving policies](#ck-resolve), [P3](#p-ports), [P4](#p-svc) |
| `pkg/policyendpoints/`, `pkg/equality/` | [The PE write path](#ck-write), [K5](#k5), [K6](#k6), [P2](#p-key) |
| `api/v1alpha1/`, `config/crd/`, `pkg/crd/`, `charts/*/crds/` | [API and CRDs](#ck-api), [K7](#k7), [K8](#k8) |
| `internal/controllers/` | [Reconciler](#ck-ctrl), [K6](#k6) |
| `cmd/main.go`, `pkg/config/controller_config.go`, `charts/`, `config/rbac/` | [Flags, RBAC and chart](#ck-chart), [P5](#p-flags) |
| Metrics, log lines | [Observability](#ck-obs), [P6](#p-logging) |
| `test/`, `scripts/`, `.github/workflows/` | [Tests and CI](#ck-tests), [P7](#p-tests) |
| Any change | [General review hygiene](#ck-general) |

**Citations.** Every citation is `path:line` at a named commit. If a line has moved, find it by the quoted symbol.

- NPC: `aws/amazon-network-policy-controller-k8s` at `0e7864a`.
- Node agent: `aws/aws-network-policy-agent` at `ccb42cd`.
- Libraries, as pinned in NPC `go.mod` at `0e7864a`: `sigs.k8s.io/controller-runtime` v0.25.2, `k8s.io/client-go` v0.37.2.

Items marked "(inferred)" rest on reasoning, or on Kubernetes or controller-runtime behavior that was not traced in source. Treat them as questions, not facts.

## 2. Quick checklist

<a id="ck-events"></a>
**Event handlers and the resolver**

- [blocking] Does a handler or resolver write to an object it got from the cache, including maps, slices and pointers inside it, or the policies the resolver returns? [K1](#k1) Blocking when: any write reaches a cached object. Every later reader sees the change, and the cache never repairs it until the object changes on the server.
- [advisory] Does new handler work run for every pod, namespace or Service event? What does it cost per event, with 10k pods and 10k policies? [P1](#p-handler) Blocking only if: the cost per event grows with the number of policies or pods in a way the initial list or a burst turns into a startup failure ([K4](#k4)).
- [blocking] If the resolver fails, is the event dropped? Does the policy have another path to be reconciled? [K4](#k4) Blocking when: a new error return drops an event that would change a PE, so the PE stays wrong until another event touches the policy.
- [blocking] For an update, does the handler compare the fields the resolver actually depends on (labels, IP, phase, deletion, Service spec)? A missing field means changes are ignored; an extra one means wasted reconciles. [P1](#p-handler) Blocking when: a field that changes what a PE contains is not compared, so the PE stays wrong until another event. An extra field is advisory.
- [blocking] Are both the old and the new object matched on label changes, so a pod that leaves a selector also updates its old policy? [P1](#p-handler) Blocking when: only the new object is matched, so the old policy's PE keeps the pod.

<a id="ck-cache"></a>
**Informer cache**

- [blocking] Does new code read a pod or Service field that the transform drops? [K2](#k2) Blocking when: the dropped field decides what goes into a PE; it reads as the zero value with no error.
- [blocking] Does new code `Get` or `List` a type that `BuildCacheOptions` doesn't list? [K3](#k3) Blocking when: the read is on the reconcile or event path; it fails every time because `ReaderFailOnMissingInformer` is set.
- [blocking] Does a change to the pod field selector keep every pod the controller needs, and still deliver a delete when a pod leaves the set? [K2](#k2) Blocking when: a pod a PE should list is filtered out, or a pod that leaves the set stays in a PE.
- [advisory] Does a new `client.UnsafeDisableDeepCopy` read return objects that anything later modifies, including in `pkg/policyendpoints`? [K1](#k1) Blocking only if: the result is modified.

<a id="ck-resolve"></a>
**Resolving policies**

- [blocking] Does a change to a resolver produce the same endpoints for every existing policy shape it doesn't mean to change? NP, ANP and CNP each have their own resolver; does the change belong in all three? Blocking when: an existing policy shape now resolves to a different set of addresses or ports.
- [blocking] For named ports, is the name resolved against the right pods (destination pods for ingress, each peer pod for egress), and can a Service `targetPort` name reach a container port the policy doesn't allow? [P3](#p-ports), [P4](#p-svc) Blocking when: the change lets a ClusterIP or pod IP through on a port the policy doesn't allow.
- [advisory] Are pods without an IP, terminal pods and hostNetwork pods handled the way the existing paths handle them? [P3](#p-ports) Blocking only if: a different handling changes what a PE allows.
- [advisory] Is the result deterministic: are maps and namespace lists sorted where the order reaches a PE? [Theme](#themes) Blocking only if: the order changes the PE on every reconcile, so every reconcile writes a PE ([K5](#k5)).
- [advisory] Is a slice shared between several endpoints (for example the resolved ports of a rule) left unmodified downstream? [K1](#k1)

<a id="ck-write"></a>
**The PE write path**

- [blocking] Does the rule identity key (`getEndpointInfoKey`, `getClusterEndpointInfoKey`, `getCombineKey`) include every field that makes two rules different, framed so two different rules can't produce the same bytes? [P2](#p-key) Blocking when: two different rules get the same key, so one of them is dropped or merged.
- [blocking] Is a PE written only when its spec changed, compared order-insensitively? [K5](#k5) Blocking when: an unchanged policy writes a PE on every reconcile; every write is watched by every node agent.
- [blocking] Can a failure, or the order of the create, update and delete loops, leave a PE set that enforces something the policy doesn't say? [K6](#k6) Blocking when: the change adds or widens such a window, even if the next reconcile repairs it.
- [blocking] Does new code list PEs or CPEs by label? [K6](#k6) Blocking when: the list decides what is deleted or reused; PEs carry no labels, so it returns nothing.
- [blocking] Does new code reuse an existing PE that may belong to an older policy with the same name (a different UID)? [K6](#k6), [#265](https://github.com/aws/amazon-network-policy-controller-k8s/issues/265) Blocking when: new code adds a path that adopts such a PE, so garbage collection can delete the new policy's only PE.
- [advisory] Does a change to bin-packing keep each PE at or under `--endpoint-chunk-size` entries per list, and avoid moving entries between PEs when nothing changed? [K5](#k5) Blocking only if: an entry is missing from every PE while it moves, or moves on every reconcile.

<a id="ck-api"></a>
**API and CRDs**

- [blocking] Does the change add, rename or change the meaning of a PE or CPE field? Is the node agent's copy of the types changed to match, and what does the agent version already on the nodes do with it? [K7](#k7) Blocking when: an agent that is currently supported reads the new PE differently from what the controller means.
- [blocking] Does the change need a CRD schema change to take effect? [K8](#k8) Blocking when: the controller writes a field the existing CRD doesn't have; the API server drops it on clusters where the CRD already exists (inferred: structural schema pruning).
- [advisory] Were `make manifests` and `make prepare-embed` run, and do `config/crd/bases`, `pkg/crd/crds.yaml` and `charts/*/crds/crds.yaml` agree? [K8](#k8)
- [advisory] Does a CRD change keep the `controller-gen.kubebuilder.io/version` annotation that `prepare-embed` pins for the PolicyEndpoint CRD? [K8](#k8)

<a id="ck-ctrl"></a>
**Reconciler**

- [blocking] Does every failure that leaves a PE wrong reach `Reconcile`'s return value? Blocking when: an error is logged and dropped on the reconcile path, so nothing requeues it.
- [advisory] A namespaced request reconciles both the NetworkPolicy and the ANP with that name. Does a change to one path handle the other's errors and latency? [K6](#k6) Blocking only if: an error in one stops the other from being written.
- [advisory] Does cleanup still run when the policy is deleted before its finalizer was added, or while the controller is down? [K6](#k6) Blocking only if: a deleted policy's PEs stay and keep being enforced.

<a id="ck-chart"></a>
**Flags, RBAC and chart**

- [advisory] Does the change add a flag? Could it ship without one? Is it in the chart's `deployment.yaml` and `values.yaml`? [P5](#p-flags)
- [blocking] Does a new API call have RBAC in both `config/rbac/role.yaml` (from markers) and the chart's `templates/rbac.yaml`? [P5](#p-flags) Blocking when: the chart's ClusterRole lacks the verb, so installs from the chart fail the call ([#226](https://github.com/aws/amazon-network-policy-controller-k8s/pull/226)).
- [advisory] Does a chart change still work with the image tag the chart defaults to? [P5](#p-flags)

<a id="ck-obs"></a>
**Observability**

- [advisory] Does a new log line fire per pod, endpoint or event at the default info level? Batch it into one line per reconcile, or use `V(1)`. Was an info line that exists for a past incident kept? [P6](#p-logging)
- [advisory] Does a skipped object (headless Service, hostNetwork pod, unresolved named port) still leave a trace at info, so a missing rule can be explained? [P6](#p-logging)
- [advisory] Does a new metric join an existing family, with bounded label values? [P6](#p-logging)

<a id="ck-tests"></a>
**Tests and CI**

- [advisory] Does each changed resolver path have a table case for the common shapes (mixed numeric and named ports, a selector matching several namespaces, a nil port, IPv4 and IPv6 CIDRs)? [P7](#p-tests)
- [advisory] Does a change to handler or resolver code that reads the cache keep `TestPolicyReferenceResolverDoesNotMutateCache` reaching the new path? [K1](#k1)
- [advisory] For scale or startup changes, was `make run-startup-scale-test` or `/test e2e` run, and are before and after numbers attached? [P7](#p-tests)
- [advisory] Does a CI script fail closed: a skipped or errored step reported as failed, not passed? [P7](#p-tests)

<a id="ck-general"></a>
**General review hygiene**

These apply to any change. Where a line links an entry, that entry makes it specific to NPC.

- [advisory] What happens on upgrade: the new controller reads PEs the old one wrote. On rollback: the old controller reads PEs the new one wrote. Does each reconcile them back to its own shape? [K6](#k6), [K7](#k7) Blocking only if: during the switch a PE enforces something neither version means, for example a PE half-converted to a new encoding.
- [advisory] Each new test fails on main, passes with the change, calls production code, and avoids sleeps and absolute counts.
- [advisory] No `time.Sleep` waits for a condition that can be checked ([#209](https://github.com/aws/amazon-network-policy-controller-k8s/pull/209#discussion_r2760512052)).
- [advisory] New logic is written once. NP, ANP and CNP share a helper where their semantics match ([#231](https://github.com/aws/amazon-network-policy-controller-k8s/pull/231#discussion_r3270150950), [#227](https://github.com/aws/amazon-network-policy-controller-k8s/pull/227#discussion_r3221549005)).
- [advisory] Comments describe current behavior and why, without change history or ticket IDs ([#260](https://github.com/aws/amazon-network-policy-controller-k8s/pull/260#discussion_r3961280288)).
- [advisory] The PR description matches the final code, names every fix in it, and lists test results ([#237](https://github.com/aws/amazon-network-policy-controller-k8s/pull/237#discussion_r3384226032)).
- [advisory] Hot-path or scale changes attach before and after numbers.
- [advisory] Concurrency is real (`--max-concurrent-reconciles` workers, handler goroutines) before a sync primitive is added.

<a id="contracts"></a>
## 3. Contracts

These are rules the code depends on that nothing enforces at compile time. "Silent" means no error reaches the caller and nothing is logged at error level.

<a id="k1"></a>
### [blocking] K1. Objects read with `UnsafeDisableDeepCopy` belong to the cache

Blocking when: code writes through any value reached from an object read with `client.UnsafeDisableDeepCopy`, including the policies the resolver returns.

**Rule.** Treat every object read with `client.UnsafeDisableDeepCopy`, and everything reachable from it, as read-only. `DeepCopy` before modifying.

**Why.** The option hands out the informer's own object instead of a copy. A write changes what every other reader of the cache sees, and the cache keeps the change until the object next changes on the server. Silent. The resolver uses it because it runs for every pod, namespace and Service event, so it avoids a deep copy of every policy it reads ([#273](https://github.com/aws/amazon-network-policy-controller-k8s/pull/273)).

**Cites.** NPC 0e7864a: `pkg/resolvers/policies.go:13-18` (the contract, on the interface), the reads at `policies_for_pod.go:20`, `48`, `86`, `149`, `184`, `218`, `268`, `policies_for_namespace.go:19`, `75`, `policies_for_service.go:33`, `67`, `116`, `169`, `policies.go:89`, `99`, `109`. The handlers send the returned policies on the channel as `event.GenericEvent` (`internal/eventhandlers/pod.go:103-137`), and the policy handler passes them to `policyTracker.UpdateGenericPolicy` (`internal/eventhandlers/policy.go:111-119`), which must not write to them either.

`pkg/policyendpoints` lists PEs and CPEs with the default deep copy and modifies the results in place (`processExistingPolicyEndpoints`, `pkg/policyendpoints/manager.go:516-560`). Adding `UnsafeDisableDeepCopy` there would corrupt the PE cache.

**Check.** `internal/eventhandlers/cache_readonly_test.go:57` (`TestPolicyReferenceResolverDoesNotMutateCache`) runs every handler and resolver method against an envtest-backed cache with the controller's own cache options and compares every cached object with a snapshot. A new resolver path needs an object in `cacheTestObjects` that reaches it.

<a id="k2"></a>
### [blocking] K2. The pod and Service caches are stripped and filtered

Blocking when: code reads a pod or Service field that the transform drops, and the value decides what goes into a PE.

**Rule.** Code that reads pods or Services from the cache reads only the fields `stripDownPodObject` and `stripDownServiceObject` keep. To read another field, add it to the transform and to its test.

**Why.** The transforms replace `ObjectMeta`, `Spec` and `Status` with copies that keep only what the controller reads, to cut memory on large clusters ([#233](https://github.com/aws/amazon-network-policy-controller-k8s/pull/233)). A dropped field reads as its zero value, with no error: a pod's `Spec.InitContainers` is empty, every annotation except `vpc.amazonaws.com/pod-ips` is gone, and a Service has no labels. The pod informer also uses a server-side field selector that drops pods in phase `Succeeded` or `Failed` and pods with no `spec.nodeName`. A pod that moves out of that set arrives as a delete event (inferred: the API server's watch filtering).

**Cites.** NPC 0e7864a: `pkg/k8s/pod_utils.go:66-106` (pod transform; kept: name, namespace, UID, deletion timestamp, labels, the pod-IP annotation, resource version, finalizers, container names and ports, `HostNetwork`, host and pod IPs, phase), `pkg/k8s/service_utils.go:66-88` (Service transform; kept: selector, cluster IPs, ports, type, load balancer status), `pkg/config/runtime_config.go:100-122` (field selector), `125-145` (cache options).

**Check.** For every new `pod.` or `svc.` field read, find it in the transform. `pkg/k8s/pod_utils_test.go` and `service_utils_test.go` cover the transforms.

<a id="k3"></a>
### [blocking] K3. Every type the controller reads needs an informer

Blocking when: the reconcile or event path reads a type that `BuildCacheOptions` doesn't list.

**Rule.** A new type read through `mgr.GetClient()` must be added to `ByObject` in `BuildCacheOptions`, with a transform if its objects are large.

**Why.** `ReaderFailOnMissingInformer: true` makes the cached client fail a read for a type with no informer, instead of starting one lazily. The read fails every time.

**Cites.** NPC 0e7864a: `pkg/config/runtime_config.go:125-145`.

**Check.** Every type in a new `Get`/`List` call is in the `ByObject` map. A unit test with the fake client does not catch this; an envtest test with `BuildCacheOptions()` does (`cache_readonly_test.go` builds its cache that way).

<a id="k4"></a>
### [advisory] K4. Event handlers run inline and do not retry

Blocking only if: new handler work per event grows with the number of pods or policies, so a burst of events or a failover stalls the queue or startup, or a new error return drops an event that would change a PE.

**Rule.** Pod, namespace and Service handlers must stay cheap, read only from the cache, and treat a resolver error as a dropped event.

**Why.** controller-runtime runs a handler synchronously for each informer event. The pod, namespace and Service handlers call the resolver and send each affected policy on an unbuffered channel, which a `source.Channel` hands to the policy handler. When the resolver returns an error, the handler logs it and returns: the policy is not enqueued, and it catches up only when another event touches it. The handlers skip the initial list (`IsInInitialList`), because every policy gets its own create event and is reconciled against the synced cache; resolving every initial pod against every policy took longer than `CacheSyncTimeout` on large clusters ([#268](https://github.com/aws/amazon-network-policy-controller-k8s/pull/268), `test/e2e/startup/README.md`). Relists after sync are not flagged and still go through.

**Cites.** NPC 0e7864a: `internal/controllers/policy_controller.go:113` (unbuffered channel), `136` (`WatchesRawSource(source.Channel(...))`), `internal/eventhandlers/pod.go:54-64` (initial-list skip), `91-138` (resolver errors logged and dropped), `internal/eventhandlers/namespace.go:43`, `service.go:58` (same skip), `internal/eventhandlers/policy.go:106-109` (generic events enqueued after `--pod-update-batch-period-duration`).

**Check.** Run `make run-startup-scale-test` for any change to handler or resolver cost, and attach the startup barrier time it reports.

<a id="k5"></a>
### [blocking] K5. A PE is written only when its spec changed

Blocking when: an unchanged policy or an unrelated event leads to a PE write.

**Rule.** Before an update, compare the desired spec with the cached one using `EqualPolicyEndpointSpec` / `EqualClusterPolicyEndpointSpec`, which compare `Ingress`, `Egress`, `PodSelectorEndpoints`, `Ports` and `Except` as unordered sets. Skip the write when they are equal.

**Why.** Every node agent in the cluster receives every PE write through its watch. A write per reconcile turns every pod event into cluster-wide work. The resolver builds lists from maps and sets, so order is not stable between reconciles; an ordered comparison wrote PEs that had not changed ([#219](https://github.com/aws/amazon-network-policy-controller-k8s/pull/219#discussion_r2921687893)). `setLastChangeTriggerTime` stamps the annotation only on PEs that are written ([#213](https://github.com/aws/amazon-network-policy-controller-k8s/pull/213#discussion_r2842641091)).

**Cites.** NPC 0e7864a: `pkg/equality/semantic.go:13-40`, `pkg/policyendpoints/manager.go:136-150` (NP), `217-231` (ANP), `788-802` (CPE).

**Check.** A table test that reconciles the same policy twice with the inputs reordered and expects no update.

<a id="k6"></a>
### [blocking] K6. The PE set is written in three steps, and cleanup relies on garbage collection

Blocking when: a change adds or widens a window in which a policy's PE set enforces something the policy doesn't say, even if the next reconcile repairs it, or new code reuses or deletes PEs found by a list that can return the wrong set.

**Rule.** Assume a reconcile can stop after any single create, update or delete, and that the node agent applies each write as it arrives. Don't add steps that leave the PE set wrong in between. Find a policy's PEs only through the field index. Don't rely on `Cleanup` to delete PEs.

**Why.**

- `Reconcile`, `ReconcileANP` and `ReconcileCNP` create, then update, then delete, and return on the first error (`manager.go:128-157`, `209-238`, `780-809`). In between, the node agent sees a mix of old and new PEs for the policy and enforces the union of their rules (node agent ccb42cd: `controllers/policyendpoints_controller.go:644-700`, `deriveIngressAndEgressFirewallRules` appends the rules of every PE that selects the pod). So until the update and delete land, the old rules are still allowed. This window exists on main today. The next reconcile recomputes from the cache, so the state repairs itself unless the cache is behind the writes (inferred: a create the cache hasn't seen yet can lead the next reconcile to create a second PE with the same rules).
- New PEs use `GenerateName` (`manager.go:401`), so a retried create makes a new name rather than failing with `AlreadyExists`.
- `Cleanup` and `CleanupANP` list PEs with `client.MatchingLabels{IndexKeyPolicyReferenceName: name}` (`manager.go:168-170`, `249-251`). PEs carry no labels, so the list is empty and the PEs are removed by garbage collection through their owner reference after the finalizer is removed. `CleanupCNP` uses the field index (`manager.go:819-821`).
- `Reconcile` finds PEs by policy name through the field index, not by UID. A policy deleted and re-created under the same name can adopt the old policy's PE while garbage collection is about to delete it ([#265](https://github.com/aws/amazon-network-policy-controller-k8s/issues/265)).
- A namespaced request reconciles both the NetworkPolicy and the ANP with that name; an error in either requeues both (`internal/controllers/policy_controller.go:142-208`).
- The policy delete handler only removes the policy from the tracker (`internal/eventhandlers/policy.go:90-104`). Cleanup runs from the update that sets the deletion timestamp, because the reconciler adds a finalizer first (`policy_controller.go:210-247`).

**Check.** For a change to the write loops, a test that injects an error at each step (the fake client's interceptor funcs, as in `pkg/resolvers/policies_for_pod_test.go:1754`) and checks that the next reconcile converges.

<a id="k7"></a>
### [blocking] K7. PE and CPE are an API with a second reader

Blocking when: a PE or CPE field is added, renamed, or given a new meaning, and a supported node agent version reads it differently.

**Rule.** A change to `api/v1alpha1/policyendpoint_types.go` or `clusterpolicyendpoint_types.go`, or to what the controller puts in those fields, needs a matching change to the node agent's copy of the types, and a plan for the agent versions already on the nodes.

**Why.** The node agent decodes PEs and CPEs with its own Go types and enforces exactly what they say. It ignores a field it doesn't know (inferred: JSON decoding into the agent's struct). So a new field that narrows what a rule allows is ignored by an older agent, which then allows more than the policy says. A new encoding of an existing field (for example a new meaning for an empty list) is read with the old meaning. The controller and agent are released separately, so for a while both directions of skew exist on real clusters.

**Cites.** NPC 0e7864a: `api/v1alpha1/policyendpoint_types.go`, `clusterpolicyendpoint_types.go`. Node agent ccb42cd: `api/v1alpha1/policyendpoints_types.go`, `clusterpolicyendpoints_types.go`.

**Check.** Diff the two repositories' types for every changed field. The PR says which agent versions read the new PE correctly, and what an older agent does with it.

<a id="k8"></a>
### [blocking] K8. CRDs are created at startup, never updated

Blocking when: the change depends on a CRD schema change reaching clusters where the CRD already exists.

**Rule.** Don't assume a changed CRD schema reaches existing clusters through the controller. Plan the rollout of any CRD change separately ([#198](https://github.com/aws/amazon-network-policy-controller-k8s/pull/198#discussion_r2536246731)).

**Why.** `ensureCRD` creates a CRD only when `Get` returns not found; an existing CRD is left as it is. The monitor cancels the manager's context if a CRD disappears; the manager stops, the process exits, and the container restarts and creates the CRD again. A field missing from the existing schema is dropped by the API server when the controller writes it (inferred: structural schema pruning). The chart ships only the PolicyEndpoint CRD; the ANP, CNP and CPE CRDs exist only because the controller creates them ([#266](https://github.com/aws/amazon-network-policy-controller-k8s/pull/266), [#267](https://github.com/aws/amazon-network-policy-controller-k8s/pull/267) propose shipping all four).

`make prepare-embed` concatenates `config/crd/bases/*.yaml` into `pkg/crd/crds.yaml` and rewrites line 1834, the PolicyEndpoint CRD's `controller-gen.kubebuilder.io/version` annotation, back to `v0.11.3`. The `sed` addresses a fixed line number, so a change to the CRDs above that line moves the annotation and the rewrite stops matching.

**Cites.** NPC 0e7864a: `pkg/crd/crd_manager.go:39` (embed), `107-131` (`Start`), `141-165` (monitor, `cancelFn()` at `156`), `223-248` (`ensureCRD`), `Makefile:109-111` (`prepare-embed`), `charts/amazon-network-policy-controller-k8s/crds/crds.yaml`.

**Check.** After `make manifests prepare-embed`, `grep -n 'controller-gen.kubebuilder.io/version' pkg/crd/crds.yaml` shows `v0.11.3` on the PolicyEndpoint CRD. `git diff` on `config/crd/bases`, `pkg/crd/crds.yaml` and the chart CRD agree.

<a id="precedents"></a>
## 4. Precedents

Use the existing mechanism before adding a new one. All citations are NPC 0e7864a.

<a id="p-handler"></a>
### [advisory] P1. Mapping an event to the policies it affects

- The pod update handler skips the event unless annotations, labels, deletion state, pod IP or phase changed (`internal/eventhandlers/pod.go:66-79`). The namespace handler compares labels and deletion state (`namespace.go:51-61`), the Service handler spec and deletion state (`service.go:66-76`).
- The resolver is called with both the new and the old object, so a pod whose labels changed is matched against the selectors it left and the ones it joined (`pod.go:98`, `pkg/resolvers/policies_for_pod.go`).
- Pods without an IP are skipped before the resolver runs (`pod.go:92-95`).
- Policies with namespace-selector peers are found through the policy tracker, which keeps their selectors parsed (`pkg/resolvers/policy_tracker.go:34-45`, [#289](https://github.com/aws/amazon-network-policy-controller-k8s/pull/289)). Add new cross-namespace lookups there, not as a scan of every policy per event.
- Affected policies go on the channel and are enqueued after `--pod-update-batch-period-duration` (default 1s, `pkg/config/controller_config.go:19-20`), so a burst of pod events for one policy becomes one reconcile.

<a id="p-key"></a>
### [blocking] P2. Rule identity keys

Blocking when: a key leaves out a field that distinguishes two rules, or two different rules can produce the same bytes.

- `getEndpointInfoKey` hashes `field=value|` pairs for the CIDR or domain name, each `Except`, and each port's protocol, port and end port, with `nil` written distinctly from zero (`pkg/policyendpoints/manager.go:438-481`). The framing was added after a port and end port could concatenate to the same bytes as a single port ([#260](https://github.com/aws/amazon-network-policy-controller-k8s/pull/260); tests in `hash_collision_test.go`). `getClusterEndpointInfoKey` does the same for CPEs and includes the action (`clusterpolicyendpoints_chunking.go:287-297`).
- The key is order-sensitive in `Ports` and `Except` (`manager.go:436-437`). A different order yields a different key, so the rule is treated as removed and re-added and can move to another PE. Keep resolver output ordered, or sort before hashing, and say which in the PR ([#263](https://github.com/aws/amazon-network-policy-controller-k8s/pull/263#discussion_r4074726803)).
- `getCombineKey` identifies a rule by CIDR or domain name plus sorted excepts, without ports, so rules for the same addresses merge their ports (`manager.go:336-380`, [#218](https://github.com/aws/amazon-network-policy-controller-k8s/pull/218), [#231](https://github.com/aws/amazon-network-policy-controller-k8s/pull/231)). An entry with no ports means all ports and absorbs the others.

<a id="p-ports"></a>
### [advisory] P3. Ports and pods in the endpoints resolver

- Ports are resolved once per rule and shared by every peer of that rule. Ingress named ports are resolved against the destination pods; egress named ports per peer pod, since pods may expose different container ports (`pkg/resolvers/endpoints.go:82-85`, `193-198`). The shared slice must not be modified downstream ([#227](https://github.com/aws/amazon-network-policy-controller-k8s/pull/227#discussion_r3221701684)).
- A nil protocol means TCP, and a nil port means all ports for that protocol (`endpoints.go:382-404`, [#237](https://github.com/aws/amazon-network-policy-controller-k8s/pull/237), [#243](https://github.com/aws/amazon-network-policy-controller-k8s/pull/243)).
- `IsPodNetworkReady` excludes pods without an IP and terminal pods (`pkg/k8s/pod_utils.go:25-29`).
- hostNetwork pods are left out of `PodSelectorEndpoints` only; they still count as peers in ingress and egress rules, and the skipped pods are logged once per reconcile (`endpoints.go:285-320`, [#206](https://github.com/aws/amazon-network-policy-controller-k8s/pull/206#discussion_r2700279907)).

<a id="p-svc"></a>
### [advisory] P4. Service ClusterIPs in egress rules

The node agent sees egress packets on the pod's interface before kube-proxy rewrites the ClusterIP, so the controller adds the ClusterIPs of Services whose selector matches an egress peer (`pkg/resolvers/endpoints.go:458-463`). Headless Services are skipped (`endpoints.go:506`). When a Service's named `targetPort` resolves to a container port the policy doesn't allow on some backing pod, the ClusterIP is left out (`hasTargetPortBypass`, `endpoints.go:576-628`; `firstNamedPort` at `664-681` matches how the EndpointSlice controller picks the first matching container port). This area has had several fixes ([#210](https://github.com/aws/amazon-network-policy-controller-k8s/pull/210), [#223](https://github.com/aws/amazon-network-policy-controller-k8s/pull/223), [#225](https://github.com/aws/amazon-network-policy-controller-k8s/pull/225)), and a change to it needs test cases for numeric and named ports, several backing pods, and pods that name the port differently.

<a id="p-flags"></a>
### [advisory] P5. Flags, RBAC and the chart

- Controller flags are bound in `pkg/config/controller_config.go:42-56`, runtime flags in `pkg/config/runtime_config.go:59-79`. The chart passes a subset in `templates/deployment.yaml` from `values.yaml`. A new flag goes in all three, with its default in one place ([#271](https://github.com/aws/amazon-network-policy-controller-k8s/pull/271) is the pattern).
- RBAC markers are on the reconciler (`internal/controllers/policy_controller.go:84-99`). `make manifests` writes `config/rbac/role.yaml`; the chart's `templates/rbac.yaml` is separate and maintained by hand. Don't rename generated roles or bindings without updating both sides ([#203](https://github.com/aws/amazon-network-policy-controller-k8s/pull/203#discussion_r2621378163)).
- `--enable-configmap-check` reads `kube-system/amazon-vpc-cni`. When the configmap disables the controller, leader election is turned off and no watches start (`cmd/main.go:111-124`, `pkg/utils/configmap/configmap.go:14-20`).

<a id="p-logging"></a>
### [advisory] P6. Logging and metrics

- Components take a `logr.Logger` named for them (`cmd/main.go:139-143`). The default level is info; `V(1)` is debug.
- Some info lines exist on purpose, for example the Service resolution logs restored after an incident ([#211](https://github.com/aws/amazon-network-policy-controller-k8s/pull/211#discussion_r2818527238), from [#62](https://github.com/aws/amazon-network-policy-controller-k8s/pull/62)). Lowering one needs a reason in the PR.
- A line that would fire per item is batched into one per reconcile, as for skipped hostNetwork pods ([#206](https://github.com/aws/amazon-network-policy-controller-k8s/pull/206#discussion_r2760659319)) and headless Services ([#211](https://github.com/aws/amazon-network-policy-controller-k8s/pull/211#discussion_r2842166991)).
- Each reconcile logs one line with its compute and write durations and the create, update and delete counts (`pkg/policyendpoints/manager.go:159-161`, [#230](https://github.com/aws/amazon-network-policy-controller-k8s/pull/230)).
- Metrics are in `pkg/metrics/policy_metrics.go:16-46` (`network_policy_reconciliations_total`, `network_policy_reconcile_duration_seconds`, `network_policy_objects_total`, `advanced_network_policy_enabled`). Prefer a new label value on an existing metric, and check whether controller-runtime already exports it ([#209](https://github.com/aws/amazon-network-policy-controller-k8s/pull/209#discussion_r2761306720)).

<a id="p-tests"></a>
### [advisory] P7. Where tests go and which CI runs them

- Unit tests sit next to the code and use table tests with the controller-runtime fake client (`fake.NewClientBuilder`), with interceptor funcs to inject errors. The mock client in `mocks/` is generated by `scripts/gen_mocks.sh`.
- `make test` runs `go test ./...` with envtest. Tests that depend on real cache behavior (transforms, field selectors, `UnsafeDisableDeepCopy`) use envtest, as `cache_readonly_test.go` does.
- `/test e2e <sha>` runs cyclonus on IPv4 and IPv6 EKS clusters and the startup scale test on IPv4 (`.github/workflows/pr-e2e-tests.yaml`). The YAML fixtures in `tests/anp` and `tests/cnp` are applied by `scripts/`.
- The startup scale test runs under ginkgo, whose suite timeout is separate from `go test -timeout` ([#270](https://github.com/aws/amazon-network-policy-controller-k8s/pull/270#discussion_r4406927331)).
- CI scripts treat a skipped or failed step as a failure, retry transient API errors, and wait for deletes to finish ([#269](https://github.com/aws/amazon-network-policy-controller-k8s/pull/269#discussion_r4319446855), [#278](https://github.com/aws/amazon-network-policy-controller-k8s/pull/278)).

<a id="themes"></a>
## 5. Recurring review themes

Roughly ordered by how often reviewers raised them on PRs #193 to #289 (approximate, hand-tagged).

| Severity | Theme | Reviewer's question | Examples |
|---|---|---|---|
| blocking | Ports and Service bypass | Is a named port resolved against the right pods, and can a Service `targetPort` reach a port the policy doesn't allow? Are all target ports checked, not only the first? Blocking when: traffic is allowed on a port the policy doesn't allow ([P4](#p-svc)). | [#210](https://github.com/aws/amazon-network-policy-controller-k8s/pull/210#discussion_r2820397555), [#223](https://github.com/aws/amazon-network-policy-controller-k8s/pull/223#discussion_r2948688862), [#225](https://github.com/aws/amazon-network-policy-controller-k8s/pull/225#discussion_r2976444834) |
| blocking | Rule identity and combining | Does the key include every distinguishing field, and do combined rules keep the union of ports? Blocking when: two different rules get one key ([P2](#p-key)). | [#218](https://github.com/aws/amazon-network-policy-controller-k8s/pull/218#discussion_r2896348679), [#231](https://github.com/aws/amazon-network-policy-controller-k8s/pull/231#discussion_r3270150950), [#261](https://github.com/aws/amazon-network-policy-controller-k8s/pull/261#discussion_r3929671511), [#263](https://github.com/aws/amazon-network-policy-controller-k8s/pull/263#discussion_r4074726803) |
| advisory | Log level and volume | Is the line needed at info? Does it fire per item? Does it explain why something was skipped? Was an incident-driven line kept? ([P6](#p-logging)) | [#211](https://github.com/aws/amazon-network-policy-controller-k8s/pull/211#discussion_r2818659636), [#206](https://github.com/aws/amazon-network-policy-controller-k8s/pull/206#discussion_r2760387703), [#221](https://github.com/aws/amazon-network-policy-controller-k8s/pull/221#discussion_r2942301603) |
| advisory | Cost on the event path | What does this cost per event at scale, and does it list pods or policies it could skip? Blocking only if: startup or the queue stalls at scale ([K4](#k4)). | [#219](https://github.com/aws/amazon-network-policy-controller-k8s/pull/219#discussion_r2921661272), [#210](https://github.com/aws/amazon-network-policy-controller-k8s/pull/210#discussion_r2820649262), [#289](https://github.com/aws/amazon-network-policy-controller-k8s/pull/289#discussion_r4589290409) |
| advisory | Test cases for common shapes | Are mixed ports, several matching namespaces, nil ports and hostNetwork pods covered? ([P7](#p-tests)) | [#227](https://github.com/aws/amazon-network-policy-controller-k8s/pull/227#discussion_r3221575747), [#232](https://github.com/aws/amazon-network-policy-controller-k8s/pull/232#discussion_r3251188965), [#237](https://github.com/aws/amazon-network-policy-controller-k8s/pull/237#discussion_r3384187963) |
| blocking | Unneeded PE writes | Is the write skipped when the spec is unchanged, for PE and CPE alike? Blocking when: unchanged policies write PEs ([K5](#k5)). | [#213](https://github.com/aws/amazon-network-policy-controller-k8s/pull/213#discussion_r2842425382), [#219](https://github.com/aws/amazon-network-policy-controller-k8s/pull/219#discussion_r2921687893) |
| advisory | Errors reach the caller | Is an error returned and handled, not logged in two places with a misleading message? Blocking only if: a dropped error leaves a PE wrong ([K4](#k4)). | [#223](https://github.com/aws/amazon-network-policy-controller-k8s/pull/223#discussion_r2948732332), [#221](https://github.com/aws/amazon-network-policy-controller-k8s/pull/221#discussion_r2942657871), [#227](https://github.com/aws/amazon-network-policy-controller-k8s/pull/227#discussion_r3221687714) |
| advisory | Deterministic output | Are namespaces and map iterations sorted where the order reaches a PE? | [#232](https://github.com/aws/amazon-network-policy-controller-k8s/pull/232#discussion_r3251167158) |
| advisory | Shared data stays read-only | Is a cached object, or a slice shared between endpoints, modified downstream? ([K1](#k1)) | [#227](https://github.com/aws/amazon-network-policy-controller-k8s/pull/227#discussion_r3222900780), [#218](https://github.com/aws/amazon-network-policy-controller-k8s/pull/218#discussion_r2896869754), [#273](https://github.com/aws/amazon-network-policy-controller-k8s/pull/273) |
| advisory | CI fails closed | Does a skipped, cancelled or errored step report failure? Does cleanup wait for deletion? ([P7](#p-tests)) | [#269](https://github.com/aws/amazon-network-policy-controller-k8s/pull/269#discussion_r4319446855), [#269](https://github.com/aws/amazon-network-policy-controller-k8s/pull/269#discussion_r4319446878), [#270](https://github.com/aws/amazon-network-policy-controller-k8s/pull/270#discussion_r4406927331) |
| advisory | Chart, RBAC and CRDs in step | Do the chart, generated RBAC and CRDs agree, and does the chart still work with its default image? ([P5](#p-flags), [K8](#k8)) | [#226](https://github.com/aws/amazon-network-policy-controller-k8s/pull/226#pullrequestreview-3989049243), [#203](https://github.com/aws/amazon-network-policy-controller-k8s/pull/203#discussion_r2621378163), [#198](https://github.com/aws/amazon-network-policy-controller-k8s/pull/198#discussion_r2536246731) |
| advisory | Sleeps and PR descriptions | Can a sleep be a check? Does the description name every fix and the reason for each? | [#209](https://github.com/aws/amazon-network-policy-controller-k8s/pull/209#discussion_r2761323859), [#237](https://github.com/aws/amazon-network-policy-controller-k8s/pull/237#discussion_r3384226032), [#211](https://github.com/aws/amazon-network-policy-controller-k8s/pull/211#pullrequestreview-3815734717) |
