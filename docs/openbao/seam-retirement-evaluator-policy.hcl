# OpenBao policy for the seam-retirement-evaluator Kubernetes-auth role.
# This file contains no credential values.

path "secret/data/evaluators/seam-retirement-evaluator/*" {
  capabilities = ["read"]
}

path "secret/data/monitoring/victoriametrics/*" {
  capabilities = ["read"]
}

path "secret/data/seam/routes/*" {
  capabilities = ["deny"]
}

path "secret/data/*" {
  capabilities = ["deny"]
}
