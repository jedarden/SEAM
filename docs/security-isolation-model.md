# SEAM Security Isolation Model

## Overview

This document describes the complete security isolation model for SEAM and the seam-retirement-evaluator service. It documents all authentication and authorization paths, OpenBao policies, and the security boundaries that enforce the hostile-fragment threat model.

**Last Updated:** 2026-09-24  
**Bead:** seam-022ee164

## Vault Base In Force

The prefix SEAM enforces is `rs-manager/rs-manager/seam/routes` — the estate
convention `secret/<installation>/<cluster>/...`, so
`rs-manager/rs-manager/seam/routes/<route>/token` is
`secret/data/rs-manager/rs-manager/seam/routes/<route>/token` in KV v2 terms.
It is the *default* base (`internal/spec/allowlist.go` `DefaultVaultBaseDir`,
inlined in `internal/server/server.go`); `SEAM_VAULT_BASE_DIR` overrides it,
so the base stays deployment configuration rather than part of the schema.

The earlier cluster-agnostic base `seam/routes` is **retired** (consolidated
2026-09-04): it kept the prefix portable across clusters, but its
backup/replication coverage rested on a legacy rs-manager OpenBao role already
scheduled for removal, so the prefix moved under the installation scope the
replicator already walks. With `SEAM_VAULT_BASE_DIR` unset the runtime
enforcer now rejects a path under the old base. It appears below only where a
deployed policy is quoted verbatim (policies carry the legacy grant/deny
alongside the consolidated one until the legacy paths retire) or where the
retirement itself is being described.

## Threat Model: Hostile Fragment

SEAM operates under the hostile-fragment threat model, which assumes:

1. **Fragment authors may be malicious** - Route fragment authors can attempt to exfiltrate credentials
2. **Lint can be bypassed** - Client-side validation is not sufficient
3. **Gateway token must be bounded** - SEAM's OpenBao token must have literally no access outside its designated paths
4. **Cross-tenant isolation is required** - Each service must have strictly bounded access to secrets

### Security Requirements

Under this threat model, the following requirements MUST be satisfied:

1. **SEAM's OpenBao token** can ONLY read `secret/data/rs-manager/rs-manager/seam/routes/*` and NOTHING else
2. **Evaluator's OpenBao token** can ONLY read:
   - `secret/data/rs-manager/seam-retirement-evaluator/victoriametrics-query` (the query-only VictoriaMetrics bearer token — the single grant; the GitHub-token grant was removed 2026-09-05 when the evaluator went detection-only)
3. **Mutual denial** - SEAM cannot read evaluator paths, evaluator cannot read SEAM paths
4. **Default-deny** - Both roles explicitly deny all other paths
5. **Read-only** - Neither role has write capabilities to any secret

## Architecture Overview

```
┌────────────────────────────────────────────────────────────────────────────────┐
│                        OpenBao (rs-manager)                                    │
│          http://openbao-rs-manager.ardenone.com:8444                           │
├────────────────────────────────────────────────────────────────────────────────┤
│                                                                                │
│  Vault base SEAM enforces (DefaultVaultBaseDir):                               │
│    rs-manager/rs-manager/seam/routes                                           │
│  = secret/data/rs-manager/rs-manager/seam/routes/* in KV v2 terms.             │
│  The old cluster-agnostic base seam/routes is RETIRED (2026-09-04):            │
│  with SEAM_VAULT_BASE_DIR unset the runtime enforcer rejects it.               │
│                                                                                │
│  ┌────────────────────────────────────────────────────────────────────────────┐│
│  │ SEAM OpenBao Role                                                          ││
│  │ Policy: seam   |   Bound SA: seam (namespace: seam)                        ││
│  │ Token TTL: 24h   |   Token Max TTL: 72h                                    ││
│  │                                                                            ││
│  │ CAN READ:                                                                  ││
│  │   secret/data/rs-manager/rs-manager/seam/routes/*                          ││
│  │                                                                            ││
│  │ DENIED:                                                                    ││
│  │   seam-retirement-evaluator/* (the evaluator prefix), monitoring/*,        ││
│  │   and all other paths                                                      ││
│  │   (default-deny: secret/data/* is denied)                                  ││
│  └────────────────────────────────────────────────────────────────────────────┘│
│                                                                                │
│  ┌────────────────────────────────────────────────────────────────────────────┐│
│  │ Evaluator OpenBao Role                                                     ││
│  │ Policy: seam-retirement-evaluator-policy                                   ││
│  │ Bound SA: seam-retirement-evaluator (namespace: seam)                      ││
│  │ Token TTL: 24h   |   Token Max TTL: 72h                                    ││
│  │                                                                            ││
│  │ CAN READ:                                                                  ││
│  │   secret/data/rs-manager/seam-retirement-evaluator/victoriametrics-query   ││
│  │   (the query-only VM bearer token — the policy's single grant)             ││
│  │                                                                            ││
│  │ DENIED:                                                                    ││
│  │   secret/data/rs-manager/rs-manager/seam/routes/*  <- SEAM routes          ││
│  │   secret/data/seam/routes/*                        <- retired base         ││
│  │   and all other paths (OpenBao default-deny: no grant means no access)     ││
│  │                                                                            ││
│  │ The evaluator is detection-only: the binary makes no OpenBao calls.        ││
│  │ This role bounds whatever runs as its ServiceAccount — in practice the     ││
│  │ access canaries that prove the boundary every 5 minutes.                   ││
│  └────────────────────────────────────────────────────────────────────────────┘│
│                                                                                │
│  ┌────────────────────────────────────────────────────────────────────────────┐│
│  │ SEAM route secrets                                                         ││
│  │   secret/data/rs-manager/rs-manager/seam/routes/<route>/token              ││
│  │     • rs-manager/rs-manager/seam/routes/github-alerts/token                ││
│  │     • rs-manager/rs-manager/seam/routes/kalshi-tape/token                  ││
│  │     • rs-manager/rs-manager/seam/routes/mta-my-way/token                   ││
│  │     ... (one per route that needs external authentication)                 ││
│  └────────────────────────────────────────────────────────────────────────────┘│
└────────────────────────────────────────────────────────────────────────────────┘
```

## Authentication Paths

### Path 1: SEAM Gateway Authentication

**Service:** SEAM gateway  
**ServiceAccount:** `seam` (namespace: `seam`)  
**OpenBao Role:** `seam`  
**OpenBao Policy:** `seam`  

**Authentication Flow:**

1. **Kubernetes Authentication:**
   - SEAM pod runs with ServiceAccount `seam`
   - Pod has projected service account token volume mounted at `/var/run/secrets/kubernetes.io/serviceaccount/token`
   - SA token is automatically injected by Kubernetes

2. **OpenBao Login:**
   - SEAM reads SA token from mounted volume
   - SEAM authenticates to OpenBao via Kubernetes auth method:
     ```
     POST /v1/auth/kubernetes/login
     {
       "role": "seam",
       "jwt": "<service-account-token>"
     }
     ```
   - OpenBao validates JWT with Kubernetes API server
   - OpenBao returns OpenBao client token with `seam` policy attached

3. **Token Usage:**
   - SEAM uses OpenBao token to read route secrets on-demand
   - Token is cached in-memory for TTL duration (24h)
   - Token auto-renews before expiration

**Access Boundaries:**
- ✅ CAN read: `secret/data/rs-manager/rs-manager/seam/routes/*`
- ❌ CANNOT read: `secret/data/seam-retirement-evaluator/*` (evaluator prefix, explicit deny)
- ❌ CANNOT read: `secret/data/monitoring/*`
- ❌ CANNOT read: Any other paths
- ❌ CANNOT write: Any secrets

### Path 2: Evaluator Authentication

**Service:** seam-retirement-evaluator  
**ServiceAccount:** `seam-retirement-evaluator` (namespace: `seam`)  
**OpenBao Role:** `seam-retirement-evaluator`  
**OpenBao Policy:** `seam-retirement-evaluator-policy`  

**Authentication Flow:**

1. **Kubernetes Authentication:**
   - Evaluator pod runs with ServiceAccount `seam-retirement-evaluator`
   - Pod has projected service account token volume
   - SA token is automatically injected by Kubernetes

2. **OpenBao Login:**
   - Evaluator reads SA token from mounted volume
   - Evaluator authenticates to OpenBao via Kubernetes auth method:
     ```
     POST /v1/auth/kubernetes/login
     {
       "role": "seam-retirement-evaluator",
       "jwt": "<service-account-token>"
     }
     ```
   - OpenBao validates JWT with Kubernetes API server
   - OpenBao returns OpenBao client token with `seam-retirement-evaluator-policy` attached

3. **Token Usage:**
   - The evaluator is **detection-only** (since 2026-09-05): it emits
     deprecation candidates and never writes to a git host. There is no PR,
     no branch, and no GitHub token to read — the proposed `x-seam-deprecated`
     edit is landed by a human as an ordinary commit to `main`.
   - The binary itself makes no OpenBao calls; its only network dependency is
     the VictoriaMetrics query. The role's single grant covers the query-only
     VM bearer token (`victoriametrics-query`), read by the access canary —
     not by the evaluator binary.
   - Token is cached in-memory for TTL duration (24h)
   - Token auto-renews before expiration

**Access Boundaries:**
- ✅ CAN read: `secret/data/rs-manager/seam-retirement-evaluator/victoriametrics-query` (query-only VM bearer token)
- ❌ CANNOT read: `secret/data/rs-manager/rs-manager/seam/routes/*`
- ❌ CANNOT read: The retired GitHub-token path (`seam-retirement-evaluator/github/token` — now a deny-probe target)
- ❌ CANNOT read: Any other paths
- ❌ CANNOT write: Any secrets

## OpenBao Policies

Both policies are written every cycle by the rs-manager OpenBao
hardening-reconciler (`k8s/rs-manager/openbao/hardening-reconciler.yml` in
declarative-config), so that reconciler — not the reference copies — is the
source of truth. Both carry a **legacy** rule for the retired base
`seam/routes` next to the consolidated one: a glob is prefix-exact, so the
legacy rule stops matching the moment the data sits only under the new base,
and it is kept only until the legacy paths retire. Do not copy a legacy rule
into a new policy without also carrying the consolidated one.

### SEAM Policy

**File:** `declarative-config/k8s/rs-manager/seam/seam-openbao-policy.hcl`

```hcl
# Allow reading SEAM route secrets ONLY — the consolidated, enforced prefix
path "secret/data/rs-manager/rs-manager/seam/routes/*" {
  capabilities = ["read"]
}

# LEGACY grant — retired base, kept until cutover is verified and the old
# paths are retired
path "secret/data/seam/routes/*" {
  capabilities = ["read"]
}

# Deny access to evaluator's secrets (explicit separation)
path "secret/data/seam-retirement-evaluator/*" {
  capabilities = ["deny"]
}

# Deny access to all other secrets (default-deny)
path "secret/data/*" {
  capabilities = ["deny"]
}
```

**Policy Properties:**
- **Read-only:** SEAM can only read, never write secrets
- **Path-scoped:** Only `rs-manager/rs-manager/seam/routes/*` is accessible
- **Explicit deny:** All other paths are explicitly denied
- **Isolation enforced:** Evaluator paths explicitly denied

### Evaluator Policy

**File:** `declarative-config/k8s/rs-manager/seam-retirement-evaluator/openbao-policy.hcl`
(reference copy — the rs-manager hardening-reconciler writes this policy every
cycle and is the source of truth)

```hcl
# Query-only bearer token. vmauth accepts this token only on Prometheus read
# endpoints; the separate vmagent write token is deliberately inaccessible.
path "secret/data/rs-manager/seam-retirement-evaluator/victoriametrics-query" {
  capabilities = ["read"]
}

path "secret/metadata/rs-manager/seam-retirement-evaluator/victoriametrics-query" {
  capabilities = ["read"]
}

# Explicitly deny access to SEAM's route secrets, at the enforced prefix and
# at the retired one
path "secret/data/rs-manager/rs-manager/seam/routes/*" {
  capabilities = ["deny"]
}

path "secret/data/seam/routes/*" {
  capabilities = ["deny"]  # LEGACY — retired base
}
```

**Policy Properties:**
- **Read-only:** Evaluator can only read, never write secrets
- **Narrowly scoped:** The query-only VM credential is the only grant (the
  evaluator is detection-only and holds no third-party credential; the
  GitHub-token grant was removed 2026-09-05, `declarat-b818338b`)
- **Explicit deny:** SEAM route paths explicitly denied at both prefixes;
  everything else is denied by absence of grant (OpenBao default-deny) —
  the policy carries no explicit `secret/data/*` rule
- **Isolation enforced:** Mutual denial with SEAM policy; the retired
  GitHub-token path stays denied and is asserted as a 403 by both the
  verify script and the `seam` policy's explicit deny

## Secret Paths

### SEAM Route Secrets

**Path Pattern:** `secret/data/rs-manager/rs-manager/seam/routes/<route-name>/token`

**Examples:**
- `secret/data/rs-manager/rs-manager/seam/routes/github-alerts/token`
- `secret/data/rs-manager/rs-manager/seam/routes/kalshi-tape/token`
- `secret/data/rs-manager/rs-manager/seam/routes/mta-my-way/token`

**Access:**
- ✅ SEAM CAN read
- ❌ Evaluator CANNOT read

**Contents:**
Each path contains a JSON object with authentication credentials for the external service:
```json
{
  "token": "external-service-token-or-api-key",
  "type": "bearer-token-or-api-key",
  "updated_by": "manual-setup-or-automation"
}
```

### Evaluator Credentials

**There is no evaluator GitHub token.** The evaluator is detection-only
(since 2026-09-05, `declarat-b818338b`): it emits one structured log record
and one Prometheus counter per deprecation candidate, and the proposed
`x-seam-deprecated` edit is landed by a human as an ordinary commit to `main`
in declarative-config. There is no PR, no branch, no token, and no write
path of any kind. The GitHub-token grant was removed from
`seam-retirement-evaluator-policy`; it had no caller (the sole OpenPR call
site passed nil files, so the PR path errored every time it ran).

The legacy path name `seam-retirement-evaluator/github/token` survives only
as a **deny-probe target**: the verify script requires the evaluator itself
to get 403 on it, and `seam-evaluator-credential-boundary-canary` requires
the `seam` identity to get 403 on it, every 5 minutes. Removing a grant is
not the same as retiring the prefix — the deny must outlive it.

### VictoriaMetrics Query Credential

**Path:** `secret/data/rs-manager/seam-retirement-evaluator/victoriametrics-query`

**Access:**
- ✅ Evaluator policy CAN read (its single grant)
- ❌ SEAM CANNOT read (explicit deny + default-deny)
- ❌ Nobody can read the vmagent write token (`rs-manager/monitoring/seam-vmagent`) with it

**Contents:**
```json
{
  "endpoint": "<query-only vmauth service URL>",
  "token": "<vmauth-scoped bearer token>"
}
```

**Usage:**
- vmauth maps this token to Prometheus **read** endpoints only; the write
  (`/api/v1/write`) and delete (`/api/v1/admin/tsdb/delete_series`) URLs are
  a disjoint set granted only to the vmagent write token
- `seam-retirement-evaluator-access-canary` proves this every 5 minutes:
  read succeeds, write and delete must not return 2xx

**Settled — credentialled or not?** Both historical claims were half right:
- The **evaluator binary** performs **unauthenticated** VictoriaMetrics
  queries: `loadConfig` reads only `VICTORIAMETRICS_ENDPOINT` and
  `DECLARATIVE_CONFIG_PATH`, and `VictoriaMetricsClient`
  (`tools/seam-retirement-evaluator/victoriametrics.go`) is a plain
  `http.Client` that sends no Authorization header.
- The **estate** nonetheless provisions the query-only bearer token above —
  that credential is the VM-side boundary (vmauth URL scoping), exercised by
  the access canary. The binary does not consume it today.
- The older claim that the evaluator reads
  `secret/data/monitoring/victoriametrics/readonly-credentials` is simply
  stale: that path is not in the live policy (default-deny covers it) and
  survives only as a synthetic fixture path inside
  `internal/server/e2e_isolation_test.go`.

## VictoriaMetrics Access Pattern

### Query Endpoint

**VictoriaMetrics Endpoint:** `http://victorialogs-single-ardenone-manager-vector-headless.monitoring.svc.cluster.local:8428` (the binary's in-cluster default; `VICTORIAMETRICS_ENDPOINT` overrides)

**Authentication:** Two layers, do not conflate them:
- The **binary** sends no credential — plain HTTP against the endpoint it is
  configured with (the in-cluster default needs none).
- The **credentialled path** goes through the query-only vmauth Service
  (`victoriametrics-query` in namespace `seam`, a Tailscale-annotated
  Service fronting ardenone-cluster's vmauth) with the
  `victoriametrics-query` bearer token, which vmauth scopes to read URLs
  only. This is the path the access canary exercises.

### Query Pattern

The evaluator queries metrics to determine route health and usage:

```bash
# Example: Query request count for a route over the last 30 days
curl -g 'http://victoriametrics:8428/api/v1/query?query=sum(rate(seam_request_count{route="github-alerts"}[30d]))'

# Example: Query error rate for a route
curl -g 'http://victoriametrics:8428/api/v1/query?query=sum(rate(seam_request_errors{route="github-alerts"}[30d]))'
```

**Metrics Used for Retirement Decisions:**
- `seam_request_count` - Total requests per route
- `seam_request_errors` - Error rate per route
- `seam_latency_p99` - 99th percentile latency per route
- `seam_last_request_timestamp` - Timestamp of last request per route

**Access Boundaries:**
- Evaluator has read-only access to metrics
- Evaluator cannot write or delete metrics
- SEAM has no access to VictoriaMetrics

## Kubernetes Role Bindings

### SEAM Role Binding

**File:** `declarative-config/k8s/rs-manager/seam/setup-openbao.sh` (live repo; SEAM's in-repo `declarative-config/` tree is a stale snapshot)

```yaml
OpenBao Role: seam
Bound ServiceAccount: seam
Namespace: seam
Policies: ["seam"]
Token TTL: 24h
Token Max TTL: 72h
```

### Evaluator Role Binding

**File:** `declarative-config/k8s/rs-manager/seam-retirement-evaluator/setup-openbao-resources.yml` (live repo; documented role binding)

```yaml
OpenBao Role: seam-retirement-evaluator
Bound ServiceAccount: seam-retirement-evaluator
Namespace: seam
Policies: ["seam-retirement-evaluator-policy"]
Token TTL: 24h
Token Max TTL: 72h
```

**Binding Validation:**
- ServiceAccount names MUST match exactly
- Namespace MUST be `seam`
- Policies MUST be correct for each role
- TTL settings enforce regular token renewal

## Security Properties Verified

### 1. Path Separation

✅ **VERIFIED:**
- Evaluator credential: `secret/data/rs-manager/seam-retirement-evaluator/victoriametrics-query`
- SEAM routes: `secret/data/rs-manager/rs-manager/seam/routes/*`
- No overlap between paths

### 2. Policy Isolation

✅ **VERIFIED:**
- Evaluator cannot read SEAM routes (explicit deny or default-deny)
- SEAM cannot read evaluator token (explicit deny or default-deny)
- Each policy allows only its designated paths

### 3. Service Account Binding

✅ **VERIFIED:**
- Evaluator role only accessible to `seam-retirement-evaluator` SA
- SEAM role only accessible to `seam` SA
- No cross-binding between roles

### 4. Bounded Capabilities

✅ **VERIFIED:**
- Evaluator: read-only access to the single query-only VM credential; no
  third-party credential of any kind (detection-only)
- SEAM: read-only access to route secrets only
- No write capabilities granted to either

## Test Coverage

### Unit Tests

**File:** `internal/server/openbao_token_access_denial_test.go`

Tests (against a throwaway OpenBao fixture — the property under test is the
denial, not any production path):
- SEAM CANNOT read the evaluator credential path
- SEAM CAN read its own route secrets
- Permission denied errors are correctly returned

### End-to-End Tests

**File:** `internal/server/e2e_isolation_test.go`

Exercises the full isolation shape in one test. Note: its fixture paths
(`evaluators/seam-retirement-evaluator/github-token`,
`monitoring/victoriametrics/readonly-credentials`) are synthetic and still
mirror the withdrawn GitHub-token model — they exist only inside the test's
own throwaway OpenBao. The assertions that matter are the denials:
- Evaluator CAN read its own fixture paths
- Evaluator CANNOT access SEAM routes
- Evaluator CANNOT access other secrets
- SEAM CAN read own route secrets
- SEAM CANNOT read the evaluator's credential path

### Live Verification (Canary Deployments)

The standing proof is two low-resource canary Deployments in namespace
`seam` on rs-manager, refreshed every 5 minutes:

- `seam-retirement-evaluator-access-canary` — logs in as the evaluator,
  is denied on both SEAM route prefixes and on the vmagent write token,
  reads the `victoriametrics-query` credential, queries VM through vmauth,
  and requires the write and delete APIs to reject its token
- `seam-evaluator-credential-boundary-canary` — logs in as `seam` and
  requires 403 on the retired evaluator credential path
  (`seam-retirement-evaluator/github/token`)

There is no GitHub assertion any more: the evaluator is detection-only and
has no write capability to verify.

## Verification Methods

### Method 1: Go Unit Tests

```bash
# Run SEAM access denial test
go test -v ./internal/server -run TestOpenBaoTokenAccessDenial

# Run end-to-end isolation test
go test -v ./internal/server -run TestE2EIsolation
```

**Note:** These tests require OpenBao in PATH or they skip. Use integration tests for cluster validation.

### Method 2: Canary Status (standing verification)

The two canary Deployments expose their verdict as a readiness gate — a pod
that is not Ready means the last probe round failed:

```bash
kubectl --server=http://traefik-rs-manager:8001 get deploy -n seam
# seam-retirement-evaluator-access-canary        READY 1/1
# seam-evaluator-credential-boundary-canary      READY 1/1

# On a failure, the reason is in the canary log:
kubectl --server=http://traefik-rs-manager:8001 logs -n seam \
  deploy/seam-retirement-evaluator-access-canary | tail
```

`scripts/verify-openbao-setup.sh` in declarative-config runs the same
assertions on demand — including the inverted one: the evaluator itself must
now get **403** on the retired GitHub credential path (inverted
2026-09-05, `declarat-b818338b`).

### Method 3: Manual OpenBao CLI

```bash
# Set environment variables
export BAO_ADDR="http://openbao-rs-manager.openbao.svc.cluster.local:8200"
export BAO_TOKEN="<your-admin-token>"

# Verify evaluator policy
bao policy read seam-retirement-evaluator-policy

# Verify SEAM policy
bao policy read seam

# Check for deny rules
bao policy read seam | grep 'evaluators'
bao policy read seam-retirement-evaluator-policy | grep 'rs-manager/rs-manager/seam/routes'
```

## Comparison: SEAM vs Evaluator

| Aspect | SEAM | seam-retirement-evaluator |
|--------|------|---------------------------|
| **ServiceAccount** | `seam` | `seam-retirement-evaluator` |
| **Namespace** | `seam` | `seam` |
| **OpenBao Role** | `seam` | `seam-retirement-evaluator` |
| **OpenBao Policy** | `seam` | `seam-retirement-evaluator-policy` |
| **Token TTL** | 24h | 24h |
| **Token Max TTL** | 72h | 72h |
| **Primary Secret Access** | `rs-manager/rs-manager/seam/routes/*` | `rs-manager/seam-retirement-evaluator/victoriametrics-query` (query-only VM credential; no GitHub token) |
| **Can Read Own Secrets** | ✅ Yes | ✅ Yes |
| **Can Read Other's Secrets** | ❌ No | ❌ No |
| **Can Write Secrets** | ❌ No | ❌ No |
| **Setup Method** | Shell script | Argo WorkflowTemplate |
| **Explicit Deny Rules** | `seam-retirement-evaluator/*`, `*` (default) | `rs-manager/rs-manager/seam/routes/*` (plus the retired `seam/routes/*`) |

## Security Checklist

### Setup Verification

- [ ] OpenBao server accessible at `http://openbao-rs-manager.openbao.svc.cluster.local:8200`
- [ ] SEAM policy `seam` exists in OpenBao
- [ ] Evaluator policy `seam-retirement-evaluator-policy` exists in OpenBao
- [ ] SEAM Kubernetes auth role `seam` exists
- [ ] Evaluator Kubernetes auth role `seam-retirement-evaluator` exists
- [ ] SEAM ServiceAccount `seam` exists in namespace `seam`
- [ ] Evaluator ServiceAccount `seam-retirement-evaluator` exists in namespace `seam`

### Secret Verification

- [ ] At least one SEAM route secret exists at `rs-manager/rs-manager/seam/routes/*/token`
- [ ] The query-only VM credential exists at `rs-manager/seam-retirement-evaluator/victoriametrics-query` (with `endpoint` and `token` fields)
- [ ] No GitHub token exists anywhere for the evaluator (detection-only; the retired path must stay absent and denied)

### Isolation Verification

- [ ] SEAM can read `rs-manager/rs-manager/seam/routes/*` secrets
- [ ] SEAM cannot read evaluator paths, including the retired `seam-retirement-evaluator/github/token` (permission denied — asserted by `seam-evaluator-credential-boundary-canary`)
- [ ] Evaluator can read `rs-manager/seam-retirement-evaluator/victoriametrics-query`
- [ ] Evaluator cannot read `rs-manager/rs-manager/seam/routes/*` secrets (permission denied)
- [ ] Evaluator's VM token cannot reach the metrics write or delete APIs (asserted by `seam-retirement-evaluator-access-canary`)
- [ ] Both roles cannot read other paths (armor/, kalshi/, etc.)

### Test Verification

- [ ] Unit tests pass: `go test -v ./internal/server -run TestOpenBaoTokenAccessDenial`
- [ ] E2E tests pass: `go test -v ./internal/server -run TestE2EIsolation`
- [ ] Both canary Deployments show READY 1/1 in namespace `seam` on rs-manager

## Related Documentation

- **SEAM OpenBao Setup:** `docs/notes/openbao-seam-setup.md`
- **Evaluator OpenBao Setup:** `docs/notes/openbao-evaluator-setup.md`
- **Isolation Verification Plan:** `docs/notes/evaluator-isolation-verification-plan.md`
- **OpenBao Research:** `docs/research/openbao-kubernetes-auth-seam-research.md`
- **Test Runbook:** `docs/testing-isolation-runbook.md`

The `declarative-config/` paths below live in the **live** declarative-config
repository; SEAM's in-repo `declarative-config/` tree is a stale snapshot and
may not contain them (see the caveat in `docs/notes/openbao-evaluator-setup.md`).

## References

- **Bead:** bf-4oa45 (verification and documentation task)
- **Bead:** bf-38lwm (evaluator documentation task)
- **Bead:** bf-5rx9 (SEAM documentation task)
- **Bead:** seam-022ee164 (detection-only doc reconciliation; GitHub-token grant removed 2026-09-05, `declarat-b818338b`)
- **SEAM Policy:** `declarative-config/k8s/rs-manager/seam/openbao-policy.hcl` (written every cycle by `k8s/rs-manager/openbao/hardening-reconciler.yml`)
- **Evaluator Policy:** `declarative-config/k8s/rs-manager/seam-retirement-evaluator/openbao-policy.hcl` (same reconciler)
- **SEAM Setup:** `declarative-config/k8s/rs-manager/seam/setup-openbao.sh`
- **Access Canaries:** `declarative-config/k8s/rs-manager/seam-retirement-evaluator/access-canaries.yml`
- **VM Query Path:** `declarative-config/k8s/rs-manager/seam-retirement-evaluator/victoriametrics-access.yml`
- **Verify Script:** `declarative-config/k8s/rs-manager/seam-retirement-evaluator/verify-openbao-setup.sh`
