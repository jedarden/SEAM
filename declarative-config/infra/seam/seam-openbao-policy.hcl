# OpenBao HCL policy for SEAM gateway
# Hostile-fragment threat model: grants read ONLY on seam/routes/* and NOTHING else
# This policy ensures SEAM can read route secrets but cannot access:
# - armor/ (Armor credentials)
# - kalshi/ (Kalshi credentials)
# - cluster kubeconfigs
# - any other tenant's material
# - evaluator's own GitHub token (seam-retirement-evaluator/*)

# Allow reading SEAM route secrets ONLY
path "secret/data/seam/routes/*" {
  capabilities = ["read"]
}

# Deny access to evaluator's secrets (explicit separation of concerns)
path "secret/data/evaluators/*" {
  capabilities = ["deny"]
}

# Deny access to all other secrets (default-deny)
path "secret/data/*" {
  capabilities = ["deny"]
}
