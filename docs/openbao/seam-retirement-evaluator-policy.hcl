# OpenBao policy for the seam-retirement-evaluator Kubernetes-auth role.
# This file contains no credential values.
#
# STATUS: PREPARED / HISTORICAL ARTIFACT -- NOT THE POLICY IN FORCE.
# This is the pre-provisioning draft that went with
# seam-retirement-evaluator-role-parameters.json. The policy actually deployed
# for this role lives in declarative-config:
#
#   ~/declarative-config/k8s/rs-manager/seam-retirement-evaluator/openbao-policy.hcl
#   (commit eec9f2f3, 2026-09-04, "deny the evaluator on the consolidated seam
#    route path")
#
# Two things differ between this draft and what is deployed:
#   * the credential paths -- the deployed policy grants read on the exact
#     paths secret/data/seam-retirement-evaluator/github/token and
#     secret/data/rs-manager/seam-retirement-evaluator/victoriametrics-query
#     (+ their secret/metadata/ counterparts), not on broad prefixes;
#   * the deny set -- the deployed policy denies the CONSOLIDATED route prefix
#     secret/data/rs-manager/rs-manager/seam/routes/* as well as the legacy
#     secret/data/seam/routes/*. Only the legacy deny appears below, because
#     that was the only route location when this draft was written.

path "secret/data/evaluators/seam-retirement-evaluator/*" {
  capabilities = ["read"]
}

path "secret/data/monitoring/victoriametrics/*" {
  capabilities = ["read"]
}

# LEGACY deny -- retired base. SEAM's enforced vault base dir is now
# rs-manager/rs-manager/seam/routes (internal/spec/allowlist.go
# DefaultVaultBaseDir); a path under the bare seam/routes base fails SEAM-side
# validation and the data has consolidated to
# secret/rs-manager/rs-manager/seam/*. Kept here only because the deployed
# policy carries both denies until the legacy paths retire. Do not copy this
# deny into a new policy without also carrying the consolidated one: a glob is
# prefix-exact, so this rule alone stops matching the moment the data moved.
path "secret/data/seam/routes/*" {
  capabilities = ["deny"]
}

path "secret/data/*" {
  capabilities = ["deny"]
}
