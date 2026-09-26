# Infrastructure configuration moved

The `jedarden/declarative-config` repository is the single source of truth for SEAM
infrastructure. Use
`declarative-config/k8s/rs-manager/{seam,seam-retirement-evaluator}/`; the
associated OpenBao WorkflowTemplates live under
`declarative-config/k8s/iad-ci/argo-workflows/`.

Do not restore staging copies in this repository.

This pointer is enforced by `internal/pointerguard`: `go test
./internal/pointerguard/` fails if a manifest reappears in this tree or a
document treats the removed copies as deployment sources.
