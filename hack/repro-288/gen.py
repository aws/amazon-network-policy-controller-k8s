#!/usr/bin/env python3
"""Generate the workload for the issue #288 reproducer: namespaces, NetworkPolicies with namespaceSelector peers,
kwok nodes and pods. Same shape as pkg/resolvers' customerShapeFixture, as YAML for kubectl.

  gen.py namespaces  N_NS                       -> namespaces ns-0..N with label team=t<i%20>
  gen.py policies    N_POLICIES PEERS N_NS      -> policy p<i> in a random namespace, podSelector app=a<i>,
                                                  one ingress rule with PEERS {namespaceSelector team, podSelector app}
  gen.py nodes       N_NODES                    -> kwok nodes with a /22 pod CIDR each
  gen.py pods        N_PODS N_NS N_NODES PREFIX -> pods PREFIX-<i> in ns-<i%N_NS>, label app=a<i>, on kwok nodes
"""
import random
import sys

rng = random.Random(1)


def ns_name(i):
    return f"ns-{i}"


def namespaces(n):
    for i in range(n):
        print(f"""---
apiVersion: v1
kind: Namespace
metadata:
  name: {ns_name(i)}
  labels:
    team: t{i % 20}
    repro-288: "true"
""")


def policies(n, peers, n_ns):
    for i in range(n):
        ns = ns_name(rng.randrange(n_ns))
        froms = "".join(f"""        - namespaceSelector:
            matchLabels:
              team: t{rng.randrange(20)}
          podSelector:
            matchLabels:
              app: a{rng.randrange(n)}
""" for _ in range(peers))
        print(f"""---
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: p{i}
  namespace: {ns}
  labels:
    repro-288: "true"
spec:
  podSelector:
    matchLabels:
      app: a{i}
  policyTypes: [Ingress]
  ingress:
    - from:
{froms}""")


def nodes(n):
    for i in range(n):
        cidr = f"10.{200 + i // 64}.{(i % 64) * 4}.0/22"
        print(f"""---
apiVersion: v1
kind: Node
metadata:
  name: kwok-288-{i}
  annotations:
    kwok.x-k8s.io/node: fake
  labels:
    kubernetes.io/hostname: kwok-288-{i}
    kubernetes.io/os: linux
    kubernetes.io/arch: amd64
    kubernetes.io/role: agent
    type: kwok
    repro-288: "true"
spec:
  podCIDR: {cidr}
  podCIDRs: [{cidr}]
  taints:
    - key: kwok.x-k8s.io/node
      value: fake
      effect: NoSchedule
""")


def pods(n, n_ns, n_nodes, prefix):
    for i in range(n):
        print(f"""---
apiVersion: v1
kind: Pod
metadata:
  name: {prefix}-{i}
  namespace: {ns_name(i % n_ns)}
  labels:
    app: a{i}
    repro-288: "true"
spec:
  nodeName: kwok-288-{i % n_nodes}
  tolerations:
    - key: kwok.x-k8s.io/node
      operator: Exists
  containers:
    - name: pause
      image: registry.k8s.io/pause:3.9
""")


if __name__ == "__main__":
    kind, args = sys.argv[1], [int(a) if a.isdigit() else a for a in sys.argv[2:]]
    {"namespaces": namespaces, "policies": policies, "nodes": nodes, "pods": pods}[kind](*args)
