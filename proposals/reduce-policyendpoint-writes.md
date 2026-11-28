# Reduce PolicyEndpoint writes

Implementation: aws/amazon-network-policy-controller-k8s#301, which depends on #304.

## Summary

The controller issues more PolicyEndpoint (PE) and ClusterPolicyEndpoint (CPE) writes than it needs
to. Each write is an API server and etcd write. It is also a watch event to the node agent on every
node, and the agent recomputes the whole parent policy for it. Unneeded writes add control-plane and
node load that grows with cluster size and churn. They also use up the controller's client rate
limit (20 QPS, burst 100, by default) ahead of the writes that matter.

This proposal removes two causes of unneeded writes:

1. **Port order.** Named ingress ports came back from the resolver in a varying order, and the key
   that matches a stored entry to a desired one depended on that order. On a reconcile where nothing
   changed, entries stopped matching and were repacked, often into another object. Keys now hash
   ports in a fixed order.
2. **Placement.** New endpoints went into the first object with room, in a random order, rather than
   into objects that already needed a write. The packer now fills the objects where a new endpoint
   costs the least.

A third cause, **re-merged entries**, is deliberately left alone; see "Not changed". A peer named by
more than one rule is stored as one merged entry. That merged entry matches neither rule's key, so it
is purged and repacked.

All three only matter for a policy spread over more than one object, which happens in either of two
cases:
- One of `Ingress`, `Egress` or `PodSelectorEndpoints` in a target group needs more than
  `--endpoint-chunk-size` (default 200) entries. These are counted as resolved, one per peer per
  rule, before any merge.
- Several objects are left over after the policy shrank.

Within a single object, a repacked entry lands back in the same place and nothing is written.

The A/B ran on a 700-pod kind cluster with three policies, each spread over 4 to 8 PEs, with two runs
per build. Each phase makes 60 pod changes, and each change reconciles all three policies. Writes for
the three policies together, `main` against this change:

- **No-op phase, right after the policies were created:** 64–118 against 13–17. All of the 13–17
  come from re-merged entries.
- **No-op phase, after the churn phases:** 120–124 against 0–4. On `main`, 80–100 of these come from
  re-merged entries, which change 2 lets settle. The rest come from port order, which change 1
  removes.
- **60 pod replacements:** 498–519 against 377–381.
- **30 cycles of adding and then removing 10 pods:** 736–768 against 580–588.

## Background

**Terms.**
- An *entry* is one rule entry in a PE's or CPE's `Ingress` or `Egress`. For a PE it is an
  `EndpointInfo`: an address or domain, with excepts and ports. For a CPE it is a
  `ClusterEndpointInfo`: an address or domain, with ports and an action.
- A *pod endpoint* is one item in `PodSelectorEndpoints`. *Endpoint* means either.
- A *peer* is the address or domain an entry names.
- A *selected pod* is one the policy applies to; it becomes a pod endpoint.
- NP, ANP and CNP are NetworkPolicy, ApplicationNetworkPolicy and ClusterNetworkPolicy.
- Since #286, a NetworkPolicy whose named ingress ports resolve differently across its selected pods
  is split into *target groups* (`Spec.TargetGroup`). Each group is packed over its own objects.
- Pod events for a policy are batched over `--pod-update-batch-period-duration` (default 1 s) into
  one reconcile.

**How a reconcile decides what to write.** A reconcile runs in three steps. `computePolicyEndpoints`
(and its ANP and CNP equivalents) does the first two, and `Reconcile` does the third.

1. **Purge.** For each existing object, keep the endpoints that are still desired and drop the rest.
   - An entry is matched by its key. For a PE, `getEndpointInfoKey` hashes the address or domain,
     the excepts of an address, and the ports. For a CPE, `getClusterEndpointInfoKey` hashes the
     address or domain, the action and the ports.
   - Pod endpoints are matched by value.
2. **Pack.** Place the desired endpoints that were not kept. This runs in three *passes*: ingress
   entries, egress entries, then pod endpoints. Each pass fills existing objects with room, then
   creates new ones. Finally, each object's entries for the same peer are merged.
3. **Write.** For each object to update, compare it with the cached copy using an order-insensitive
   spec comparison. Write it only if it differs: a Patch for PEs, an Update for CPEs.

Step 3 already suppresses writes for objects whose content is unchanged. So an unnecessary write
happens only when steps 1 and 2 change an object that did not need to change. There are two ways:
- moving an entry to another object, as with port order and re-merged entries;
- placing a new endpoint in an object nothing else touched, as with placement.

**No-op reconciles are common.** Pod creates and deletes enqueue every policy that selects or
references the pod. So do updates that change a pod's labels, IP, phase, deletion timestamp or
`vpc.amazonaws.com/pod-ips` annotation; the pod cache strips other annotations. Events for a pod
without an IP enqueue nothing. Every controller restart or leader failover also reconciles every
policy, and so does the informer resync, which falls back to controller-runtime's 10-hour default.

## Success criteria

1. **No-op reconciles.** A reconcile in which nothing changed writes nothing, except for re-merged
   entries. Those can still write for a few reconciles after a policy changes.
2. **Changes.** A reconcile that removes or adds endpoints writes the objects that lost an endpoint.
   In each pass, it writes others only when the additions do not fit in those, and then as few
   existing objects as can hold them. There are two exceptions:
   - Objects are created only when every existing one is full. So a large batch after removals in an
     earlier reconcile can write more objects than needed; see "Alternatives considered".
   - Re-merged entries cost writes beyond this under steady churn.
3. **Nothing else gets worse than on `main` with #304 applied.**
   - What nodes enforce is the same.
   - Objects are no larger.
   - Objects are created only when every existing one is full, as on `main`. A policy can keep
     objects that `main` would have deleted; see "Risks".
   - Compute cost is unchanged for single-port lists. Computing an entry's key costs more for longer
     lists: about 5% for a sorted multi-port list, from the order check, and 13–17% for a list out of
     canonical order, from the sort. That is about 5% of a no-op reconcile whose lists are all out of
     order.

Enforcement latency was not measured under the A/B's churn. The only latency data is the startup
test's churn probes, all within 1 s on both builds.

## Changes

### 1. Hash rule ports in a canonical order

`dedupPorts` (`pkg/resolvers/endpoints.go`) returns `maps.Values(...)` over a fresh map, so its order
varies. For a small map, Go starts iteration at a random slot, so the order is a random rotation
rather than a shuffle. Two ports come back reversed from insertion order about 1 time in 8; three
come back out of insertion order about 1 time in 4. One slice is shared by every entry of the rule,
so a reordering hits all of that rule's entries at once.

`dedupPorts` runs when named ingress ports are resolved:
- in `planIngressPorts` (`ingress_groups.go`), for NetworkPolicy;
- in `getIngressRulesPorts` (`endpoints.go`), which ANP and CNP ingress reach through
  `baseResolver.Resolve`.

`getEndpointInfoKey` hashed ports in slice order, as a TODO above it noted. So the stored entry
`{10.0.0.1, [80, 443]}` and the desired entry `{10.0.0.1, [443, 80]}` got different keys. The stored
entry was purged and the desired one repacked, often into another object.

This affects an NP, ANP or CNP ingress rule with at least one named port and two or more ports in
total, such as `[http, metrics]` or `[http, 443]`. Egress rules already produce their ports in a
stable order.

**Change:**

- `getEndpointInfoKey` and `getClusterEndpointInfoKey` hash ports in a canonical order: protocol,
  then port, then end port, with an absent value sorting before any present one.
  - A list that is already sorted is hashed as is.
  - An unsorted list of up to 8 ports is sorted in a stack buffer. A longer one is copied to the heap
    and sorted.
  - The caller's slice is never reordered, because the resolver shares it across every entry of a
    rule.
- `dedupPorts` returns its ports sorted, so named-port rules skip the sort.

The keys are in-memory map keys and are never persisted. The spec comparison ignores port order. So
changing the keys causes no writes on upgrade. Excepts are still hashed in the order given: a policy
spec sets that order, so it changes only when the spec does.

### 2. Pack new endpoints where they cost the least

Each pass filled *candidate objects* first-fit:
1. existing objects that kept something, in the cache's list order (random);
2. emptied objects;
3. objects created by an earlier pass.

When a pod was replaced, the new pod often landed in an object nothing else touched. One replacement
then cost two writes: the object the old pod left, and the one the new pod joined.

**Change:** an object costs one API call whether it gets one new endpoint or a full chunk. So each
pass now fills candidates in four classes, cheapest first:

1. **Objects already being written.** These are objects that:
   - lost some, but not all, of their endpoints in the purge step;
   - received endpoints in an earlier pass; or
   - are being restamped with a new `Spec.TargetGroup`, because the policy's groups changed. A restamp
     is a write.
2. **Emptied objects.** Each costs a write either way, as an update or a delete.
3. **Objects this reconcile creates.**
4. **Untouched objects.** The first endpoint placed in one costs a write. These go most room first
   (fewest endpoints already in the field being filled), then by name, so a batch lands in as few of
   them as possible.

Some rules go with this order:
- The set of objects being written carries across the three passes. A new pod that the policy both
  selects and allows as a peer gets a pod endpoint and an ingress entry. Each goes first into objects
  already being written for the other.
- Classes 1 and 2 go by name and class 3 in creation order, so the order of candidates does not
  depend on list order. Which entry goes into which candidate still follows Go map iteration over the
  desired entries.
- An emptied object is kept if packing put anything into it, and deleted otherwise.
- New objects are created only when every candidate is full in that field, as on `main`.
- A pass with nothing to place returns before ordering the candidates.

The same order applies to PEs and CPEs. ApplicationNetworkPolicy now packs through the NetworkPolicy
code instead of a copy of it. The merge after packing is unchanged.

## Not changed: re-merged entries

The pack step merges each object's entries for the same peer. A peer allowed by two rules resolves to
two entries, `{IP, [80]}` and `{IP, [443]}`. When both land in one object, it stores
`{IP, [80, 443]}`. That merged entry matches neither resolved key, so the next purge drops it, and
both entries are placed again.

- **On `main`.** Objects are tried in random order, so the two entries usually land together in
  another object with room, and merge there. The merged entry moves again on the next reconcile. In
  the simulation, `main` often keeps writing about once per no-op reconcile for as long as some object
  has room. In some runs it settles.
- **With change 2.** The object that lost a merged entry is already being written, so it is tried
  first. Within a few reconciles, each peer's two entries settle in one of two ways. Either they merge
  again in the object they came from, which writes nothing, or they split across objects, where each
  matches its own key. After that, a no-op reconcile writes nothing.
- **Under steady churn.** Each batch of additions places new peers' two entries together, where they
  merge, so the next reconcile repacks them. This recurs on both builds and stays well above the
  lower bound. See the simulation's last row and the A/B's two-rule policy.

While an entry moves, objects are written one at a time. A node that applies the object losing the
entry before the object gaining it briefly has neither. So new connections from that peer on that
port can be refused for a moment. This exists on `main`. Change 2 makes such moves rarer. The
remaining sources are re-merged entries and regrouping.

## Risks and compatibility

- **NetworkPolicy and ApplicationNetworkPolicy enforcement is unchanged.** The agent unions every PE
  of a policy, so where an entry lands does not matter.
- **ClusterNetworkPolicy enforcement depends on #304.**
  - The bug: on `main`, `combineClusterRulesEndpoints` narrows an all-ports entry merged with a
    specific-port one for the same peer and action. So a peer covered by both an all-ports and a
    port-80 Deny rule is denied only on port 80 while both entries share a CPE.
  - On `main`, random placement often separates the two entries again, so the narrowing comes and
    goes. This change returns purged entries to the object they came from, where they merge again, so
    the narrowing could persist.
  - #304 fixes the merge, so placement no longer matters. #301 depends on it.
- **Brief gaps.** Any move of an entry between objects can briefly drop it on a node; see "Not
  changed". This change makes fewer moves.
- **Upgrade and rollback.**
  - The changed keys are in memory only and there is no CRD change.
  - The spec comparison ignores port order, so an upgrade writes nothing for unchanged state.
  - A rollback brings back `main`'s behavior.
  - During a rolling update only the leader writes. A new leader of either version reconciles what
    the other stored as it would its own.
- **Object count.** Refilling an emptied object ahead of an untouched one saves writes, but can keep
  more objects than `main` would.
  - Example: 8 objects of 50 pods each, chunk size 100, with every pod of 4 of them replaced in one
    reconcile. This change writes 4 objects and keeps 6; `main` writes 8 and keeps 4.
  - The extra objects already existed. Neither build consolidates objects.
- **No feature flag.** The new behavior is always on.

## Alternatives considered

- **Merge before the purge step.** An earlier revision did this, so that stored and desired forms
  match. But then an entry carried the ports of every rule that names its peer. The chunk size, which
  counts entries, no longer bounded object size. It needed a per-object byte budget, and it changed
  ClusterNetworkPolicy enforcement. It was dropped as far more risk than re-merged entries warrant.
- **Order candidates by name only.** This removes the randomness. But additions in a reconcile of
  their own then top up small holes in the first objects, and each batch spills into the next
  objects. An earlier revision did this and wrote about 2–3 times as much as `main` in that case.
- **Fill the fullest object that fits, as the EndpointSlice reconciler does.**
  - What it does: Kubernetes' EndpointSlice reconciler fills objects already being updated, creates
    new objects for full chunks, and puts only the remainder into one unchanged object, the one that
    ends fullest.
  - An example: five objects each lost 40 pods in an earlier reconcile, and then 200 new pods arrive.
    This change writes 5 objects. The EndpointSlice rule writes 1 new object, at the cost of a sixth
    object.
  - The choice: this proposal keeps `main`'s rule that objects are created only when every existing
    one is full, and accepts the extra writes in that case. The case needs a batch larger than the
    largest hole, arriving after removals in an earlier reconcile, such as a scale-down followed by a
    scale-up. No simulated scenario has one.
- **Lengthen `--pod-update-batch-period-duration`.** Batching more pods per reconcile lowers the write
  count per pod, but delays enforcement for new pods. It remains an operator knob.

## Results

### Simulation

`TestWriteSimulation`, in `pkg/policyendpoints/write_simulation_test.go`, produces this table.

```
NPC_WRITE_SIMULATION=1 go test ./pkg/policyendpoints/ -run TestWriteSimulation -v
```

Copy it and `write_amplification_test.go` onto `main` to get `main`'s columns.

- **What it runs.** It drives the real compute functions, including the merge after packing, against
  an in-memory store. Each reconcile applies the same order-insensitive comparison as `Reconcile`,
  and counts a write only for a create, a delete, or an update whose spec differs.
- **Ordering.** Both builds get ports in the order `dedupPorts` gives them on `main`: the values of a
  fresh map. So this change's zeros come from the key change alone. The store lists objects in Go map
  order, as the informer's indexed List does.
- **Runs.** Each row is 10 runs at chunk size 200. A run's seed fixes which pods are removed or
  replaced. Go map order cannot be seeded, so rerunning a seed can give a different count. A cell is
  the range of total writes across the runs; a single number means every run agreed.
- **Lower bound.** It is computed per reconcile, on each build's own state: one write per object that
  held an endpoint that went away, plus one if there were additions and no such object. It is a floor
  for each reconcile, not a minimum over a whole run. It depends on how a build has spread pods over
  its objects, so compare each build with its own bound.

Unless a row says otherwise, the policy selects the pods and allows 10 peers on a numeric port. 900
pods fill 5 PEs: 4 full, and 1 with room for 100.

| Scenario | `main`: writes | `main`: lower bound | This change: writes | This change: lower bound |
| --- | --- | --- | --- | --- |
| 50 no-op reconciles; 1000 pods, 900 peers; named ports resolving to 2 ports | 30–95 | 0 | 0 | 0 |
| The same, resolving to 3 ports | 80–140 | 0 | 0 | 0 |
| 100 reconciles; 1 pod replaced per reconcile, in one batch | 146–167 | 100 | 100 | 100 |
| 100 reconciles; 10 pods replaced per reconcile, in one batch | 449–468 | 434–448 | 430–447 | 430–447 |
| 100 rounds (200 reconciles): remove 10 pods, then add 10 in a separate reconcile | 605–685 | 532–547 | 540–555 | 540–555 |
| 50 rounds (100 reconciles): scale up by 20, then down by 20 | 343–381 | 293–297 | 295–300 | 295–300 |
| Control: 1000 pods (5 full PEs); 100 reconciles, 1 replaced per reconcile | 100 | 100 | 100 | 100 |
| 1000 pods, 500 peers each in both rules (PEs full); no-op reconciles 1–50 after creation | 10–16 | 0 | 10–14 | 0 |
| The same, reconciles 51–300 | 0 | 0 | 0 | 0 |
| 1000 pods, 450 peers each in both rules (PEs with room); no-op reconciles 1–50 | 13–79 | 0 | 11–16 | 0 |
| The same, reconciles 51–300 | 0–346 (median 278) | 0 | 0 | 0 |
| 900 pods that are also the peers of two rules; 100 rounds of removing 10, then adding 10 separately | 1684–1717 | 958–979 | 1643–1673 | 950–972 |

The control row shows no gain: with every object full, `main` can only put the new pod into the
object the old one left. The last row is re-merged entries under steady churn. There, both builds stay
about 70% above their lower bound.

### Real API server

The A/B ran on kind with kwok, one fresh cluster per run, at `567ed7a` against `main` at `37a2470`.
Each build had two runs, interleaved. The cluster had 700 pods. Three policies select all of them,
and allow ingress from the same pods:

- **named:** ports `[http, metrics]`, which resolve to 8080 and 9090;
- **two rules:** the same peers on port 80 in one rule and port 443 in another;
- **plain:** port 80. It has no named ports and no peer in two rules, so only placement (change 2)
  affects it.

Each policy spans 4 to 8 PEs. The phases ran in this order:

1. **No-op 1.** Right after the policies were created: 60 changes, 2 s apart, to a pod label that no
   selector uses.
2. **Replace.** 60 replacements, 3 s apart, each creating a new pod and deleting an old one together.
3. **Split.** 30 cycles: add 10 pods, wait until they have IPs plus 3 s, then delete 10 pods and wait
   3 s.
4. **No-op 2.** 60 more label changes, after the churn phases.

How writes were counted:
- **Per policy:** from each PE's `metadata.generation`, and its creation and deletion, between
  snapshots. The PE CRD has a status subresource, so the generation moves only on spec changes.
- **Batching:** that replacement pairs and split batches each fell in one reconcile was inferred from
  timing and agreement with the simulation, not measured.

| Phase | Policy | `main` (run 1, run 2) | This change (run 1, run 2) |
| --- | --- | --- | --- |
| No-op 1 | named | 44, 104 | 0, 0 |
| | two rules | 20, 14 | 13, 17 |
| | plain | 0, 0 | 0, 0 |
| | all three | 64, 118 | 13, 17 |
| Replace | named | 192, 168 | 102, 105 |
| | two rules | 202, 201 | 166, 169 |
| | plain | 125, 129 | 109, 107 |
| | all three | 519, 498 | 377, 381 |
| Split | named | 208, 207 | 150, 150 |
| | two rules | 338, 364 | 288, 281 |
| | plain | 186, 193 | 150, 149 |
| | all three | 736, 768 | 588, 580 |
| No-op 2 | named | 24, 40 | 0, 0 |
| | two rules | 100, 80 | 0, 4 |
| | plain | 0, 0 | 0, 0 |
| | all three | 124, 120 | 0, 4 |

The "all three" rows come from the API server's `apiserver_request_total`, counting successful POST,
PATCH and DELETE on `policyendpoints`. They equal the sum of the per-policy rows except in Split on
`main`: 736 and 768 against 732 and 764. In each of those runs, two objects were created and deleted
between snapshots, which the generation counts cannot see.

What the table shows:
- **Placement alone.** The plain policy went from 125–129 writes to 107–109 in Replace, and from
  186–193 to 149–150 in Split.
- **The named policy** gains from both changes: from 168–192 to 102–105 in Replace.
- **The two-rule policy** improves too, but keeps the cost of re-merged entries under churn.
- **Creates and deletes.** Nearly every write was a patch. In Split, this change made 1 create and no
  deletes per run, against 3 creates and 2 deletes per run on `main`.
- **Object counts.** Both builds ended with 4, 8 and 4 PEs for the named, two-rule and plain policies.
- **Spread and failover.** The ranges are the spread of two runs, enough to show direction rather than
  a distribution. No phase is a leader failover; a failover reconciles each policy once, like one
  label change in a no-op phase.

### CPU

The benchmarks run `computePolicyEndpoints` and the key function only, steps 1 and 2 of a reconcile.
They were compared with benchstat over 10 interleaved runs at `f85ec2e`, on an Apple M3 Pro with one
CPU. `main` was measured with the benchmark and `write_amplification_test.go` copied onto it.

- **`BenchmarkComputePolicyEndpoints_NoOp`** runs a no-op reconcile of a 25-object policy, with 5,000
  peers and 5,000 pods.
- **`BenchmarkEndpointInfoKey`** computes one entry's key.

| Benchmark | `main` | This change | Difference |
| --- | --- | --- | --- |
| No-op reconcile, one port | 4.90 ms ± 8% | 4.83 ms ± 12% | not significant (p = 0.48) |
| No-op reconcile, two ports sorted | 5.39 ms ± 10% | 5.64 ms ± 5% | not significant (p = 0.35) |
| No-op reconcile, two ports out of order | 5.32 ms ± 10% | 5.57 ms ± 8% | +5%, borderline (p = 0.05) |
| Key, one port | 136 ns ± 14% | 142 ns ± 4% | not significant (p = 0.28) |
| Key, two ports sorted | 189 ns ± 10% | 197 ns ± 2% | +4%, not significant (p = 0.14) |
| Key, two ports out of order (`[443, 80]`) | 190 ns ± 7% | 215 ns ± 5% | +13% (p < 0.001) |
| Key, DNS rule (`[UDP 53, TCP 53]`) | 174 ns ± 6% | 199 ns ± 13% | +15% (p = 0.001) |

An independent run on the same model of machine measured these as significant, at p < 0.001:
- +5% for the out-of-order no-op reconcile;
- +5% for the sorted two-port key;
- 16–17% for the out-of-order keys.

So the costs above are real, if small:
- A port list out of canonical order costs about 25 ns per key, for the sort. DNS egress rules are
  usually written UDP first, which is out of order.
- A sorted multi-port list costs about 8 ns per key, for the order check.

Bytes per op are equal to `main` in every case. A no-op reconcile makes one allocation fewer than on
`main` (40,532 against 40,533), and a key makes the same number.

### Startup scale test

This is a smoke test, not a measure of this change. With the controller stopped, the test creates
10,000 kwok pods and 10,000 policies. It then starts a leader with warm caches, which must create every
PE.
- In `baseline`, every policy fits in one object, the leader does only creates, and the time to create
  all PEs is set by the 20 QPS client rate limit.
- `mixed` is a smaller workload covering the namespace, service, ANP and CNP paths, with some
  multi-object policies.
- The startup barrier is the time from taking the leader lease to the controller's workers starting.
- A churn probe is a new pod, timed until it appears in its policy's PEs.

`make run-startup-scale-test` passed all four specs, startup and pod churn for each scenario. It ran
once on this change at `567ed7a` and twice on `main`. Ranges in a cell are across the two scenarios.

| | This change | `main`, run 1 | `main`, run 2 |
| --- | --- | --- | --- |
| Startup barrier | 615 ms | 616–620 ms | 613–615 ms |
| All PEs created, `baseline` / `mixed` | 8m19s / 8m50s | 8m19s / 8m49s | 8m20s / 8m50s |
| Churn probes | all ≤ 1 s | all ≤ 1 s | all ≤ 1 s |
| Peak RSS, `baseline` / `mixed` | 2,261 / 438 MiB | 2,266 / 439 MiB | 2,284 / 439 MiB |
| Peak heap in use, `baseline` / `mixed` | 1,927 / 394 MiB | 1,913 / 384 MiB | 1,992 / 381 MiB |

- **RSS.** Peak RSS is at or just below `main` in both scenarios.
- **Heap.** Peak heap is within `main`'s spread in `baseline`. In `mixed` it is about 3% above both
  `main` runs, 394 against 381 and 384 MiB.
- **Caveats.** This change had one run, sampled every 5 s. `567ed7a` still had a per-object
  allocation that `2761633` removes.

## Test plan

The write-count tests in `pkg/policyendpoints/write_amplification_test.go` run repeated reconciles
against an in-memory store.
- The store returns objects in a new, seeded order each time, and named ports come in a fully shuffled
  order.
- Ten of the tests check that the stored objects, taken together, carry exactly the desired peers and
  pods, reading an entry with no ports as every port. The other two check the objects they expect
  directly.
- Ten of the twelve fail on `main`. The other two guard against regressions in the new code.

They cover:
- no-op reconciles with named ports in another order (NP, ANP, CNP);
- one pod replacement writing exactly one object (NP, ANP, CNP);
- additions in a reconcile of their own writing one object per batch;
- an emptied object refilled before an untouched one;
- a pod that the policy both selects and allows as a peer, writing only the objects it left;
- the carry across passes; removing the carry makes its test fail;
- a regrouped object taking new endpoints first;
- an emptied object kept or deleted by what it holds after packing, even with no name.

Unit tests cover the following:
- the port-order-insensitive keys;
- an unsorted port list allocating no more than a sorted one;
- adding an existing object to the candidates without allocating;
- the sorted `dedupPorts`;
- a packing pass with nothing to place leaving the candidates alone.

`FuzzGetEndpointInfoKeyMatchesFmt` checks the fmt-free key encoder from #283, byte for byte, against a
plain `fmt` version that hashes ports in canonical order. `TestWriteSimulation` and the two benchmarks
reproduce the simulation and CPU results.

## Out of scope

- **Pod event filter** (`internal/eventhandlers/pod.go`). Phase changes other than to a terminal phase
  do not change what the resolver produces, so filtering them would save reconciles, not writes.
  Label, IP and `vpc.amazonaws.com/pod-ips` changes are real inputs.
- **One write per referencing policy.** A pod that is a peer in P policies costs at least P writes per
  change, because each policy carries its own copy of the address. Removing that needs a shared
  address-set object and node agent support.
- **Regrouping.** A pod whose named port resolves differently can split a policy into target groups,
  which repacks the whole policy. That costs the same on both builds.

## Follow-ups

- **Match merged entries when purging.** This removes re-merged entries.
  - When a stored entry's key misses, look up the desired entries with the same merge key
    (`getCombineKey`).
  - If merging them gives exactly the stored entry, keep it and mark those desired entries placed.
  - What is stored, object sizes and enforcement all stay as on `main`.
  - A prototype for NP ingress, about 30 lines, took the two-rule case to zero writes. It left no
    moment where a node had neither copy of an entry. Egress and CNP need the same treatment.
  - This must wait until every merge bug is fixed, starting with #304. It makes whatever the merge
    produces stable, including a wrong merge.
- **Stop merging.** Store entries as the resolver produces them.
  - The node agent unions entries for the same address across every PE that applies to a pod, and has
    since at least v1.0.8.
  - A single-object policy would grow, because the peer repeats once per rule.
  - It also needs an answer on how domain-name entries are consumed.
- **Write objects that gain entries before objects that only lose them.** Watch events are delivered
  in order, so this would close the brief gap described in "Not changed". Creates already go first.

## Bugs found, to fix separately

- `combineClusterRulesEndpoints` narrows an all-ports entry merged with a specific-port one: #304, a
  prerequisite for #301.
- A CNP egress rule to a domain that lists only named ports resolves to an entry with no ports, which
  matches every port.
- `portsToString` ignores `EndPort`. A port and a range that starts at it, for the same peer and
  action, deduplicate to whichever comes first.
- `Cleanup` and `CleanupANP` list PEs with `MatchingLabels` on a field-index key. PEs carry no such
  label, so these list nothing, and owner-reference garbage collection deletes the objects instead.
- `RuntimeConfig.SyncPeriod` has no flag and is never passed to the manager, so the resync interval
  cannot be configured.
