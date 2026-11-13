#!/usr/bin/env bash
# Hold the kind cluster from repro.sh in the #288 problem state for a live demo, and probe it.
#
#   ./hack/repro-288/demo.sh start    # background pod churn (CHURN pods created + deleted per second)
#   ./hack/repro-288/demo.sh probe    # new pod matched by p7; watch it NOT reach p7's PolicyEndpoint
#   ./hack/repro-288/demo.sh control  # spec change on p7; PE rewritten in ~0.1 s (policy path is fine)
#   ./hack/repro-288/demo.sh status   # controller health signals: restarts, lease, queue, errors, CPU
#   ./hack/repro-288/demo.sh fix      # swap to the fixed image (FIX_IMAGE), churn keeps running
#   ./hack/repro-288/demo.sh stop     # stop the churn
set -uo pipefail
HERE="$(cd "$(dirname "$0")" && pwd)"
CLUSTER=${CLUSTER:-npc-288}; CHURN=${CHURN:-5}; N_NS=${N_NS:-500}; N_NODES=${N_NODES:-20}
FIX_IMAGE=${FIX_IMAGE:-716123634426.dkr.ecr.us-east-1.amazonaws.com/npa-bench/network-policy-controller:main-79b0365-peerindex}
STATE=/tmp/repro-288/demo; mkdir -p "$STATE"
k(){ kubectl --context "kind-$CLUSTER" "$@"; }
npc_pod(){ k -n kube-system get pod -l app.kubernetes.io/name=amazon-network-policy-controller-k8s --field-selector=status.phase=Running --sort-by=.metadata.creationTimestamp -o jsonpath='{.items[-1:].metadata.name}'; }
metric(){ k get --raw "/api/v1/namespaces/kube-system/pods/$(npc_pod):8080/proxy/metrics" 2>/dev/null | awk -v m="$1" 'index($0,m)==1{print $2; exit}'; }
P=p7; PNS=$(k get netpol -A --field-selector metadata.name=$P -o jsonpath='{.items[0].metadata.namespace}')
pe_ips(){ k -n "$PNS" get policyendpoints -o json | python3 -c "
import json,sys
for i in json.load(sys.stdin)['items']:
    if i['spec'].get('policyRef',{}).get('name')=='$P':
        print(' '.join(e['podIP'] for e in i['spec'].get('podSelectorEndpoints',[])))"; }

case "${1:-}" in
start)
  [ -f "$STATE/churn.pid" ] && kill -0 "$(cat "$STATE/churn.pid")" 2>/dev/null && { echo "churn already running (pid $(cat "$STATE/churn.pid"))"; exit 0; }
  ( i=0
    while :; do
      python3 "$HERE/gen.py" pods "$CHURN" "$N_NS" "$N_NODES" "churn-$i" | k create -f - >/dev/null 2>&1
      [ "$i" -ge 30 ] && python3 "$HERE/gen.py" pods "$CHURN" "$N_NS" "$N_NODES" "churn-$((i-30))" | k delete -f - --wait=false >/dev/null 2>&1
      i=$((i+1)); sleep 1
    done ) >/dev/null 2>&1 &
  echo $! > "$STATE/churn.pid"
  echo "churn started (pid $!): $CHURN pods created and $CHURN deleted per second, ~$((CHURN*4)) pod events/s"
  echo "the unfixed handler does ~2 events/s, so give it 1-2 minutes to build a backlog, then run: $0 probe" ;;
probe)
  name=demo-target-$(date +%H%M%S)
  k apply -f - >/dev/null <<EOF
apiVersion: v1
kind: Pod
metadata: {name: $name, namespace: $PNS, labels: {app: a7, repro-288: "true"}}
spec:
  nodeName: kwok-288-0
  tolerations: [{key: kwok.x-k8s.io/node, operator: Exists}]
  containers: [{name: pause, image: registry.k8s.io/pause:3.9}]
EOF
  for _ in $(seq 30); do ip=$(k -n "$PNS" get pod "$name" -o jsonpath='{.status.podIP}'); [ -n "$ip" ] && break; sleep 0.5; done
  echo "created $PNS/$name with IP $ip, selected by policy $PNS/$P (podSelector app=a7)"
  t0=$(date +%s)
  while :; do
    el=$(( $(date +%s) - t0 ))
    if pe_ips | tr ' ' '\n' | grep -qx "$ip"; then echo "  ${el}s: $ip is in $P's PolicyEndpoint"; break; fi
    echo "  ${el}s: $ip NOT in $P's PolicyEndpoint yet (pod is Running, API server knows its IP)"
    [ "$el" -ge "${PROBE_MAX:-600}" ] && break; sleep 10
  done ;;
control)
  # Pure policy-path change: add an ipBlock peer with a unique /32. The controller writes it to the PE on the
  # next reconcile of p7, which a policy event triggers directly (no pod handler involved).
  cidr="192.0.2.$((RANDOM % 250 + 1))/32"; t0=$(date +%s.%N)
  k -n "$PNS" patch netpol $P --type=json -p="[{\"op\":\"add\",\"path\":\"/spec/ingress/-\",\"value\":{\"from\":[{\"ipBlock\":{\"cidr\":\"$cidr\"}}]}}]" >/dev/null
  for _ in $(seq 300); do
    k -n "$PNS" get policyendpoints -o json | grep -q "\"${cidr%/32}" && break; sleep 0.1
  done
  python3 -c "print(f'spec change on $P (new ipBlock $cidr) reached its PolicyEndpoint in {$(date +%s.%N)-$t0:.1f}s')" ;;
status)
  pod=$(npc_pod)
  echo "controller pod: $pod"
  echo "  image:    $(k -n kube-system get pod "$pod" -o jsonpath='{.spec.containers[0].image}' | sed 's#.*/##')"
  echo "  restarts: $(k -n kube-system get pod "$pod" -o jsonpath='{.status.containerStatuses[0].restartCount}')"
  echo "  lease:    $(k -n kube-system get lease amazon-network-policy-controller-k8s -o jsonpath='{.spec.holderIdentity} renewed {.spec.renewTime}')"
  echo "  workqueue_depth: $(metric 'workqueue_depth{controller="policy"')   reconcile errors: $(metric 'controller_runtime_reconcile_errors_total{controller="policy"}')"
  c0=$(metric process_cpu_seconds_total); sleep 10; c1=$(metric process_cpu_seconds_total)
  python3 -c "print(f'  CPU:      {($c1-$c0)/10:.2f} cores (10 s sample)')"
  [ -f "$STATE/churn.pid" ] && kill -0 "$(cat "$STATE/churn.pid")" 2>/dev/null && echo "  churn:    running" || echo "  churn:    stopped" ;;
fix)
  k -n kube-system set image deployment/amazon-network-policy-controller-k8s amazon-network-policy-controller-k8s="$FIX_IMAGE" >/dev/null
  k -n kube-system rollout status deployment/amazon-network-policy-controller-k8s --timeout=3m >/dev/null
  echo "controller now on ${FIX_IMAGE##*/}; wait ~30 s for it to take the lease, then run: $0 probe" ;;
stop)
  [ -f "$STATE/churn.pid" ] && kill "$(cat "$STATE/churn.pid")" 2>/dev/null; rm -f "$STATE/churn.pid"
  echo "churn stopped (pods left in place; repro.sh deletes them on its next run)" ;;
*) sed -n '2,10p' "$0"; exit 1 ;;
esac
