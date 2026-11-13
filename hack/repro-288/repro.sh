#!/usr/bin/env bash
# Reproducer for issue #288 on a local kind cluster with kwok nodes. No cloud resources.
#
# What it does:
#   1. kind cluster, PolicyEndpoint CRDs, kwok (fake nodes/pods), the controller from IMAGE (1 replica).
#   2. N_NS namespaces, N_POLICIES NetworkPolicies with PEERS namespaceSelector peers each; waits until every
#      policy has a PolicyEndpoint, so startup work is out of the way.
#   3. Creates N_PODS kwok pods at once (the pod event wave), then immediately:
#        probe A: a pod matched by one policy's podSelector -> seconds until its IP is in that policy's PE
#        probe B: a pod matched by one policy's ingress peer -> seconds until its IP is in the PE's ingress list
#        control: a spec change on the same policy -> seconds until the PE is rewritten
#   4. Prints the controller's CPU and the handler lag; leaves the cluster up unless KEEP=0.
#
# Usage:
#   IMAGE=<repo>:<tag> ./hack/repro-288/repro.sh            # e.g. the image built from main
#   IMAGE=<repo>:<fixed-tag> REUSE=1 ./hack/repro-288/repro.sh   # swap the controller image on the same cluster
#
# Knobs: N_NS (500) N_POLICIES (5000) PEERS (10) N_NODES (20) N_PODS (1500) PROBE_TIMEOUT (900 s) CLUSTER (npc-288)
set -euo pipefail
HERE="$(cd "$(dirname "$0")" && pwd)"
REPO="$(cd "$HERE/../.." && pwd)"
: "${IMAGE:?set IMAGE=<repo>:<tag> (must be present in the local docker daemon)}"
CLUSTER=${CLUSTER:-npc-288}; N_NS=${N_NS:-500}; N_POLICIES=${N_POLICIES:-5000}; PEERS=${PEERS:-10}
N_NODES=${N_NODES:-20}; N_PODS=${N_PODS:-1500}; PROBE_TIMEOUT=${PROBE_TIMEOUT:-1500}; KWOK_VERSION=${KWOK_VERSION:-v0.8.0}
REUSE=${REUSE:-0}; KEEP=${KEEP:-1}
OUT=${OUT:-/tmp/repro-288}; mkdir -p "$OUT"
log(){ echo "[$(date -u +%H:%M:%S)] $*"; }
k(){ kubectl --context "kind-$CLUSTER" "$@"; }
now(){ date +%s.%N; }

if [ "$REUSE" != 1 ]; then
  kind delete cluster --name "$CLUSTER" >/dev/null 2>&1 || true
  log "creating kind cluster $CLUSTER"
  cat > "$OUT/kind.yaml" <<EOF
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
nodes:
  - role: control-plane
    kubeadmConfigPatches:
      - |
        kind: ClusterConfiguration
        apiServer:
          extraArgs:
            max-requests-inflight: "3000"
            max-mutating-requests-inflight: "1500"
EOF
  kind create cluster --name "$CLUSTER" --config "$OUT/kind.yaml" --wait 120s >/dev/null
  log "installing CRDs and kwok $KWOK_VERSION"
  k apply -f "$REPO/config/crd/bases" >/dev/null
  for m in kwok.yaml stage-fast.yaml; do
    k apply -f "https://github.com/kubernetes-sigs/kwok/releases/download/$KWOK_VERSION/$m" >/dev/null
  done
  k -n kube-system rollout status deployment/kwok-controller --timeout=3m >/dev/null
  log "creating $N_NODES kwok nodes"
  python3 "$HERE/gen.py" nodes "$N_NODES" | k apply -f - >/dev/null
  log "creating $N_NS namespaces"
  python3 "$HERE/gen.py" namespaces "$N_NS" | k apply -f - >/dev/null
fi

log "loading $IMAGE into kind and deploying the controller (1 replica)"
kind load docker-image --name "$CLUSTER" "$IMAGE" >/dev/null
helm template amazon-network-policy-controller-k8s "$REPO/charts/amazon-network-policy-controller-k8s" \
  --namespace kube-system --set enableConfigMapCheck=false --set replicaCount=1 \
  --set image.repository="${IMAGE%:*}" --set image.tag="${IMAGE##*:}" --set image.pullPolicy=Never \
  --set resources.limits.memory=4Gi --set resources.limits.cpu=4 --set resources.requests.cpu=1 \
  --set kubeAPIQPS="${NPC_QPS:-200}" --set kubeAPIBurst="${NPC_BURST:-400}" | k apply -f - >/dev/null
# QPS 200 (#271) so the reconcile side is not the limiter: at the default 20 QPS the work queue drains at exactly
# 20 reconciles/s during a pod wave, whichever handler is in front of it.
k -n kube-system rollout restart deployment/amazon-network-policy-controller-k8s >/dev/null
k -n kube-system rollout status deployment/amazon-network-policy-controller-k8s --timeout=3m >/dev/null
NPC_POD=$(k -n kube-system get pod -l app.kubernetes.io/name=amazon-network-policy-controller-k8s --field-selector=status.phase=Running --sort-by=.metadata.creationTimestamp -o jsonpath='{.items[-1:].metadata.name}')
# Wait until this pod holds the leader lease (the replaced pod may hold it for up to the lease duration).
for _ in $(seq 120); do
  holder=$(k -n kube-system get lease amazon-network-policy-controller-k8s -o jsonpath='{.spec.holderIdentity}' 2>/dev/null || true)
  case "$holder" in "$NPC_POD"*) break;; esac; sleep 2
done
log "controller pod $NPC_POD is leader, running $(k -n kube-system get pod "$NPC_POD" -o jsonpath='{.spec.containers[0].image}')"

if [ "$REUSE" != 1 ] || [ "$(k get netpol -A -l repro-288=true --no-headers 2>/dev/null | wc -l)" -lt "$N_POLICIES" ]; then
  log "creating $N_POLICIES policies with $PEERS namespaceSelector peers each"
  python3 "$HERE/gen.py" policies "$N_POLICIES" "$PEERS" "$N_NS" > "$OUT/policies.yaml"
  k apply -f "$OUT/policies.yaml" --server-side >/dev/null
fi
log "waiting for a PolicyEndpoint per policy"
for _ in $(seq 180); do
  pe=$(k get policyendpoints -A --no-headers 2>/dev/null | wc -l)
  [ "$pe" -ge "$N_POLICIES" ] && break; sleep 5
done
log "PolicyEndpoints: $pe"
# Delete any pods from an earlier run so the event wave is a wave.
k delete pods -A -l repro-288=true --wait=false >/dev/null 2>&1 || true
sleep 20

# Probe targets. Policy p7 selects app=a7 (podSelector) and its first ingress peer is a fixed
# {team=tX, app=aY} pair; read it from the live object so the probe pod is a real match.
P=p7; PNS=$(k get netpol -A -l repro-288=true --field-selector metadata.name=$P -o jsonpath='{.items[0].metadata.namespace}')
PEER_TEAM=$(k -n "$PNS" get netpol $P -o jsonpath='{.spec.ingress[0].from[0].namespaceSelector.matchLabels.team}')
PEER_APP=$(k -n "$PNS" get netpol $P -o jsonpath='{.spec.ingress[0].from[0].podSelector.matchLabels.app}')
PEER_NS=$(k get ns -l team="$PEER_TEAM" -o jsonpath='{.items[0].metadata.name}')
pe_of(){ k -n "$PNS" get policyendpoints -o json | python3 -c "import json,sys;d=json.load(sys.stdin);d['items']=[i for i in d['items'] if i['spec'].get('policyRef',{}).get('name')=='$P'];print(json.dumps(d))"; }
wait_in_pe(){ # $1 = ip, $2 = python expression over PE json items -> list of ips
  local ip=$1 expr=$2 t0; t0=$(now)
  while :; do
    if pe_of | python3 -c "import json,sys;items=json.load(sys.stdin)['items'];ips=$expr;sys.exit(0 if '$ip' in ips else 1)"; then
      python3 -c "print(f'{$(now)-$t0:.1f}')"; return 0; fi
    if python3 -c "import sys;sys.exit(0 if $(now)-$t0 > $PROBE_TIMEOUT else 1)"; then echo "TIMEOUT(>${PROBE_TIMEOUT}s)"; return 0; fi
    sleep 1
  done
}
mkpod(){ # name ns app
  k apply -f - >/dev/null <<EOF
apiVersion: v1
kind: Pod
metadata: {name: $1, namespace: $2, labels: {app: $3, repro-288: "true"}}
spec:
  nodeName: kwok-288-0
  tolerations: [{key: kwok.x-k8s.io/node, operator: Exists}]
  containers: [{name: pause, image: registry.k8s.io/pause:3.9}]
EOF
  for _ in $(seq 60); do ip=$(k -n "$2" get pod "$1" -o jsonpath='{.status.podIP}' 2>/dev/null); [ -n "$ip" ] && { echo "$ip"; return; }; sleep 0.5; done
  echo "NOIP"; return 1
}

npc_cpu(){ k get --raw "/api/v1/namespaces/kube-system/pods/$NPC_POD:8080/proxy/metrics" 2>/dev/null | awk '/^process_cpu_seconds_total/{print $2}' || true; }
cpu0=$(npc_cpu)
t_wave=$(now)
log "creating $N_PODS pods at once (the pod event wave), 8 kubectl clients in parallel"
python3 "$HERE/gen.py" pods "$N_PODS" "$N_NS" "$N_NODES" wave > "$OUT/wave.yaml"
python3 - "$OUT/wave.yaml" <<'PY'
import sys; docs=open(sys.argv[1]).read().split('---\n'); docs=[d for d in docs if d.strip()]
for i in range(8): open(f"{sys.argv[1]}.{i}","w").write('---\n'.join(docs[i::8]))
PY
for i in $(seq 0 7); do k create -f "$OUT/wave.yaml.$i" >/dev/null 2>&1 & done
# Let the wave arrive first: a real "new pod during churn" lands behind whatever is already queued. Wait until
# half the wave has an IP (kwok sets it right after creation), then create the probes.
for _ in $(seq 600); do
  n=$(k get pods -A -l repro-288=true --field-selector status.phase=Running --no-headers 2>/dev/null | wc -l)
  [ "$n" -ge $((N_PODS / 2)) ] && break; sleep 1
done
log "$n wave pods Running with an IP $(python3 -c "print(f'{$(now)-$t_wave:.0f}')")s after the wave started; creating the probe pods now"
log "probe A: target pod (podSelector app=a7) in $PNS;  probe B: peer pod (team=$PEER_TEAM/app=$PEER_APP) in $PEER_NS"
ipA=$(mkpod probe-target "$PNS" a7); ipB=$(mkpod probe-peer "$PEER_NS" "$PEER_APP")
wait_in_pe "$ipA" "[e['podIP'] for i in items for e in i['spec'].get('podSelectorEndpoints',[])]" > "$OUT/tA" &
wait_in_pe "$ipB" "[r['cidr'] for i in items for r in i['spec'].get('ingress',[])]" > "$OUT/tB" &
wait
tA=$(cat "$OUT/tA"); tB=$(cat "$OUT/tB")
log "  wave of $N_PODS pods fully submitted; target pod $ipA in $P's PE after: ${tA}s; peer pod $ipB in $P's PE ingress after: ${tB}s"
log "control: spec change on $P (add a port)"
t0=$(now); k -n "$PNS" patch netpol $P --type=json -p='[{"op":"add","path":"/spec/ingress/0/ports","value":[{"port":8080}]}]' >/dev/null
tC=$(wait_in_pe "$ipB" "[r['cidr'] for i in items for r in i['spec'].get('ingress',[]) if any(p.get('port')==8080 for p in r.get('ports',[]))]")
log "  PE rewritten with the new port after: ${tC}s"

cpu1=$(npc_cpu)
wall=$(python3 -c "print(f'{$(now)-$t_wave:.0f}')")
log "controller CPU over the run: $(python3 -c "print(f'{(${cpu1:-0}-${cpu0:-0})/max($wall,1):.2f} cores avg over {$wall}s')")"
running=$(k get pods -A -l repro-288=true --field-selector status.phase=Running --no-headers | wc -l)
summary="$IMAGE | qps=${NPC_QPS:-200} pods=$N_PODS policies=$N_POLICIES peers=$PEERS | target->PE ${tA}s | peer->PE ${tB}s | spec-change->PE ${tC}s | running pods $running"
log "RESULT: $summary"; echo "$(date -u +%FT%TZ) $summary" >> "$OUT/results.txt"
k -n kube-system logs "$NPC_POD" --tail=2000 > "$OUT/npc-$(echo "$IMAGE" | tr '/:' '__').log" 2>/dev/null || true
[ "$KEEP" = 1 ] || kind delete cluster --name "$CLUSTER"
