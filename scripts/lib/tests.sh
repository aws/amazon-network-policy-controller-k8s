function generate_manifest_and_apply(){

    # Use upstream images by default
    IMAGE_REPOSITORY_PARAMETER=""
    CYCLONUS_IMAGE_REPOSITORY="mfenwick100"

    if [[ -n $TEST_IMAGE_REGISTRY && $TEST_IMAGE_REGISTRY != "registry.k8s.io" ]]; then
        IMAGE_REPOSITORY_PARAMETER="- --image-repository=$TEST_IMAGE_REGISTRY"
        CYCLONUS_IMAGE_REPOSITORY=${TEST_IMAGE_REGISTRY}/networking-e2e-test-images
    fi

cat <<EOF | kubectl apply -n netpol -f -
apiVersion: batch/v1
kind: Job
metadata:
  name: cyclonus
spec:
  backoffLimit: 0
  template:
    spec:
      restartPolicy: Never
      serviceAccount: cyclonus
      containers:
        - name: cyclonus
          imagePullPolicy: Always
          image: ${CYCLONUS_IMAGE_REPOSITORY}/cyclonus:v0.5.4
          command:
            - ./cyclonus
            - generate
            - --retries=2
            ${IMAGE_REPOSITORY_PARAMETER}
EOF
}

function run_cyclonus_tests(){

    TIMEOUT=$((4 * 60 * 60))  # 4 hours timeout in seconds
    START_TIME=$(date +%s)

    kubectl create ns netpol
    kubectl create clusterrolebinding cyclonus --clusterrole=cluster-admin --serviceaccount=netpol:cyclonus
    kubectl create sa cyclonus -n netpol
    generate_manifest_and_apply

    echo "Executing cyclonus suite"

    # Waiting only for Complete would sit out the whole timeout when the job fails
    while true; do
      # Callers run under set -e; a transient API error here is retried on the next iteration.
      STATUS=$(kubectl get job.batch/cyclonus -n netpol -o jsonpath='{.status.conditions[?(@.type=="Failed")].status}') || STATUS=""
      if [[ $STATUS == "True" ]]; then
        echo "Job cyclonus has failed"
        TEST_FAILED=true
        break
      fi

      ELAPSED_TIME=$(( $(date +%s) - START_TIME ))
      if [[ $ELAPSED_TIME -ge $TIMEOUT ]]; then
        echo "Timed out waiting for job cyclonus to complete"
        TEST_FAILED=true
        break
      fi

      kubectl wait --for=condition=complete job.batch/cyclonus -n netpol --timeout=60s > /dev/null 2>&1 && break
    done

    kubectl logs -n netpol job/cyclonus > ${DIR}/results.log || TEST_FAILED=true

    # Cleanup after test finishes. A failure here must not skip verifying the results.
    kubectl delete clusterrolebinding cyclonus || echo "Unable to delete clusterrolebinding cyclonus"
    kubectl delete ns netpol x y z --ignore-not-found || echo "Unable to delete the cyclonus namespaces"

    cat ${DIR}/results.log

    echo "Verify results against expected"
    python3 ${DIR}/lib/verify_test_results.py -f ${DIR}/results.log -ip $IP_FAMILY || TEST_FAILED=true
}
