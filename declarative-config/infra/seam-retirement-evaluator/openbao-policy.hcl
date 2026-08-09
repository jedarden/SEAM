# OpenBao HCL policy for seam-retirement-evaluator
# Allows read access to evaluator's own GitHub token path and VictoriaMetrics credentials
# Explicitly denies access to seam/routes/* to ensure SEAM cannot read evaluator's token

# Allow reading evaluator's own GitHub token
path "secret/data/seam-retirement-evaluator/*" {
  capabilities = ["read"]
}

# Allow reading VictoriaMetrics credentials (for metrics query access)
path "secret/data/monitoring/victoriametrics/*" {
  capabilities = ["read"]
}

# Explicitly deny access to SEAM's route secrets
path "secret/data/seam/routes/*" {
  capabilities = ["deny"]
}

# Deny access to all other secrets
path "secret/data/*" {
  capabilities = ["deny"]
}
