# OpenBao Kubernetes Authentication Role Configuration
# Role: seam-retirement-evaluator
#
# This role is created via the Argo WorkflowTemplate: seam-retirement-evaluator-openbao-setup
# To recreate manually:
#   bao write auth/kubernetes/role/seam-retirement-evaluator \
#     bound_service_account_names=seam-retirement-evaluator \
#     bound_service_account_namespaces=seam \
#     policies=seam-retirement-evaluator-policy \
#     token_ttl=24h \
#     token_max_ttl=72h \
#     token_default_policies=seam-retirement-evaluator-policy

# Role Binding
bound_service_account_names = ["seam-retirement-evaluator"]
bound_service_account_namespaces = ["seam"]

# Policies
policies = ["seam-retirement-evaluator-policy"]
token_default_policies = ["seam-retirement-evaluator-policy"]

# Token TTL
token_ttl = "24h"
token_max_ttl = "72h"

# This role is distinct from SEAM's role which uses:
# - Role name: seam (or seam-openbao-role)
# - Policy: seam-openbao-policy
# - Different service account binding
