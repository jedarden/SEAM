#!/bin/bash
# Workflow monitoring and log capture script for SEAM CI
# Usage: ./monitor-workflow.sh <workflow-name>

set -e

WORKFLOW_NAME="${1:-}"
KUBECONFIG="/home/coding/.kube/iad-ci.kubeconfig"
NAMESPACE="argo-workflows"

if [ -z "$WORKFLOW_NAME" ]; then
    echo "Usage: $0 <workflow-name>"
    echo "Example: $0 seam-ci-abc123"
    exit 1
fi

echo "=== Monitoring workflow: $WORKFLOW_NAME ==="
echo "Started at: $(date -u +"%Y-%m-%dT%H:%M:%SZ")"

# Function to get workflow status
get_workflow_status() {
    kubectl --kubeconfig="$KUBECONFIG" get workflow "$WORKFLOW_NAME" -n "$NAMESPACE" -o jsonpath='{.status.phase}'
}

# Function to get workflow message
get_workflow_message() {
    kubectl --kubeconfig="$KUBECONFIG" get workflow "$WORKFLOW_NAME" -n "$NAMESPACE" -o jsonpath='{.status.message}'
}

# Function to check if workflow is complete
is_workflow_complete() {
    local phase
    phase=$(get_workflow_status)
    [[ "$phase" == "Succeeded" || "$phase" == "Failed" || "$phase" == "Error" ]]
}

# Monitor workflow status
echo "Waiting for workflow to complete..."
while true; do
    CURRENT_STATUS=$(get_workflow_status)
    echo "[$(date -u +"%Y-%m-%dT%H:%M:%SZ")] Current status: $CURRENT_STATUS"

    if is_workflow_complete; then
        echo "Workflow completed with status: $CURRENT_STATUS"
        break
    fi

    sleep 10
done

# Get final workflow details
echo ""
echo "=== Final Workflow Details ==="
FINAL_PHASE=$(get_workflow_status)
FINAL_MESSAGE=$(get_workflow_message)
FINISHED_AT=$(kubectl --kubeconfig="$KUBECONFIG" get workflow "$WORKFLOW_NAME" -n "$NAMESPACE" -o jsonpath='{.status.finishedAt}')

echo "Phase: $FINAL_PHASE"
echo "Message: $FINAL_MESSAGE"
echo "Finished at: $FINISHED_AT"

# Try to get logs from workflow pods
echo ""
echo "=== Attempting to capture logs ==="

# Get pod names associated with this workflow
POD_NAMES=$(kubectl --kubeconfig="$KUBECONFIG" get pods -n "$NAMESPACE" -l "workflows.argoproj.io/workflow=$WORKFLOW_NAME" -o jsonpath='{.items[*].metadata.name}')

if [ -n "$POD_NAMES" ]; then
    echo "Found pods: $POD_NAMES"

    for pod in $POD_NAMES; do
        echo ""
        echo "=== Logs from pod: $pod ==="
        kubectl --kubeconfig="$KUBECONFIG" logs "$pod" -n "$NAMESPACE" --all-containers=true || echo "Failed to get logs from pod: $pod"
    done
else
    echo "No pods found (may have been deleted by podGC)"
    echo "Logs are only available while the workflow is running"
fi

# Save full workflow details to JSON
OUTPUT_FILE="workflow-${WORKFLOW_NAME}-$(date -u +"%Y%m%dT%H%M%SZ").json"
echo ""
echo "Saving full workflow details to $OUTPUT_FILE"
kubectl --kubeconfig="$KUBECONFIG" get workflow "$WORKFLOW_NAME" -n "$NAMESPACE" -o json > "$OUTPUT_FILE"

echo ""
echo "=== Monitoring Complete ==="
echo "Workflow: $WORKFLOW_NAME"
echo "Final Status: $FINAL_PHASE"
echo "Output saved to: $OUTPUT_FILE"

exit $([ "$FINAL_PHASE" == "Succeeded" ] && echo 0 || echo 1)
