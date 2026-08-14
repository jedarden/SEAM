#!/bin/bash
# Submit the workflow to create OpenBao role for seam-retirement-evaluator

set -e

log() { echo "[$(date -u '+%Y-%m-%dT%H:%M:%SZ')] $1"; }
error() { echo "[$(date -u '+%Y-%m-%dT%H:%M:%SZ')] ERROR: $1" >&2; exit 1; }

log "Submitting workflow to create OpenBao role for seam-retirement-evaluator..."

# Submit the workflow template
kubectl --kubeconfig=/home/coding/.kube/iad-ci.kubeconfig create -f - <<EOF
apiVersion: argoproj.io/v1alpha1
kind: Workflow
metadata:
  generateName: seam-retirement-evaluator-openbao-setup-
  namespace: argo-workflows
  labels:
    app: seam-retirement-evaluator
    component: openbao-provisioning
spec:
  workflowTemplateRef:
    name: seam-retirement-evaluator-openbao-setup
EOF

log "✅ Workflow submitted successfully!"
log ""
log "To monitor the workflow:"
log "  kubectl --kubeconfig=/home/coding/.kube/iad-ci.kubeconfig get workflows -n argo-workflows -l app=seam-retirement-evaluator,component=openbao-provisioning --watch"
log ""
log "To view logs:"
log "  kubectl --kubeconfig=/home/coding/.kube/iad-ci.kubeconfig logs -n argo-workflows -c main <workflow-pod-name>"
