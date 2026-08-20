# Infrastructure configuration moved

The `declarative-config` repository is the single source of truth for SEAM
infrastructure. Use
`declarative-config/k8s/rs-manager/{seam,seam-retirement-evaluator}/`; the
associated OpenBao WorkflowTemplates live under
`declarative-config/k8s/iad-ci/argo-workflows/`.

Do not restore staging copies in this repository.
