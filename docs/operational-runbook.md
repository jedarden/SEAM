# SEAM Operational Runbook

**Version:** 1.0  
**Last Updated:** 2026-08-15  
**Purpose:** Production deployment patterns, failure modes, debugging procedures, and operational guidance for SEAM gateway operators.

---

## Table of Contents

1. [Architecture Overview](#architecture-overview)
2. [Production Deployment Patterns](#production-deployment-patterns)
3. [Health Check Interpretation](#health-check-interpretation)
4. [Common Failure Modes](#common-failure-modes)
5. [OpenBao Connectivity Debugging](#openbao-connectivity-debugging)
6. [Log Analysis Procedures](#log-analysis-procedures)
7. [Rollback Strategies](#rollback-strategies)
8. [Performance Tuning](#performance-tuning)
9. [Emergency Procedures](#emergency-procedures)

---

## Architecture Overview

### Component Overview

SEAM is a dual-port HTTP gateway that mediates access to backend services by injecting secrets from OpenBao:

```
┌─────────────────────────────────────────────────────────────────┐
│                        SEAM Gateway                              │
├─────────────────────────────────────────────────────────────────┤
│                                                                  │
│  Caller Port (8080)          Operator Port (8081)               │
│  ├─ API endpoints            ├─ Health endpoints                │
│  ├─ Proxy routes             ├─ Metrics                         │
│  ├─ Docs UI                  ├─ Cache status                    │
│  └─ OpenAPI spec             └─ Config status                   │
│                                                                  │
│  Middleware Stack:                                              │
│  1. Capture Middleware (corpus collection)                      │
│  2. Validation Middleware (OpenAPI schema)                      │
│  3. Cache Middleware (TTL-based caching)                        │
│  4. Quota Middleware (cost enforcement)                          │
│  5. Header Stripping (removes internal headers)                 │
│                                                                  │
└─────────────────────────────────────────────────────────────────┘
         │                                    │
         ▼                                    ▼
    Backend Services                   OpenBao (Secrets)
```

### Key Services

- **OpenBao:** Secret management backend (hosted on rs-manager cluster)
- **Kubernetes:** Container orchestration and service discovery
- **Argo Workflows:** CI/CD pipeline for builds and deployments
- **VictoriaMetrics:** Metrics storage and querying (optional)

---

## Production Deployment Patterns

### Kubernetes Deployment

#### Namespace Configuration

```yaml
apiVersion: v1
kind: Namespace
metadata:
  name: seam
  labels:
    name: seam
```

#### ServiceAccount Setup

```yaml
apiVersion: v1
kind: ServiceAccount
metadata:
  name: seam
  namespace: seam
```

#### RBAC Configuration

SEAM requires minimal Kubernetes permissions. Create a Role and RoleBinding:

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: seam-pod-reader
  namespace: seam
rules:
- apiGroups: [""]
  resources: ["pods"]
  verbs: ["get", "list"]

---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: seam-pod-reader-binding
  namespace: seam
subjects:
- kind: ServiceAccount
  name: seam
  namespace: seam
roleRef:
  kind: Role
  name: seam-pod-reader
  apiGroup: rbac.authorization.k8s.io
```

### Deployment Manifest

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: seam
  namespace: seam
  labels:
    app: seam
spec:
  replicas: 3
  selector:
    matchLabels:
      app: seam
  template:
    metadata:
      labels:
        app: seam
      annotations:
        prometheus.io/scrape: "true"
        prometheus.io/port: "8081"
        prometheus.io/path: "/_seam/metrics"
    spec:
      serviceAccountName: seam
      containers:
      - name: seam
        image: ronaldraygun/seam:latest  # Pin specific version in production
        ports:
        - name: caller
          containerPort: 8080
          protocol: TCP
        - name: operator
          containerPort: 8081
          protocol: TCP
        env:
        - name: SEAM_CALLER_PORT
          value: "8080"
        - name: SEAM_OPERATOR_PORT
          value: "8081"
        - name: SEAM_BASE_URL
          value: "https://seam.example.com"  # External URL
        - name: SEAM_SPEC_DIR
          value: "/app/spec"
        - name: SEAM_FRAGMENTS_DIR
          value: "/app/fragments"
        - name: SEAM_CAPTURE_ENABLED
          value: "false"  # Enable only for debugging
        volumeMounts:
        - name: spec
          mountPath: /app/spec
          readOnly: true
        - name: fragments
          mountPath: /app/fragments
          readOnly: true
        livenessProbe:
          httpGet:
            path: /_seam/healthz
            port: caller
          initialDelaySeconds: 10
          periodSeconds: 10
          timeoutSeconds: 5
          failureThreshold: 3
        readinessProbe:
          httpGet:
            path: /_seam/readyz
            port: caller
          initialDelaySeconds: 5
          periodSeconds: 5
          timeoutSeconds: 3
          failureThreshold: 3
        resources:
          requests:
            memory: "128Mi"
            cpu: "100m"
          limits:
            memory: "256Mi"
            cpu: "500m"
      volumes:
      - name: spec
        configMap:
          name: seam-spec
      - name: fragments
        projected:
          sources:
          - configMap:
              name: seam-fragments-test-service
```

### Service Configuration

#### Caller-Facing Service

```yaml
apiVersion: v1
kind: Service
metadata:
  name: seam-caller
  namespace: seam
  labels:
    app: seam
    component: caller
spec:
  type: ClusterIP
  ports:
  - port: 8080
    targetPort: caller
    protocol: TCP
    name: http
  selector:
    app: seam
```

#### Operator-Only Service

```yaml
apiVersion: v1
kind: Service
metadata:
  name: seam-operator
  namespace: seam
  labels:
    app: seam
    component: operator
spec:
  type: ClusterIP
  ports:
  - port: 8081
    targetPort: operator
    protocol: TCP
    name: http
  selector:
    app: seam
```

### Environment-Specific Configuration

#### Development

```bash
SEAM_CALLER_PORT=8080
SEAM_OPERATOR_PORT=8081
SEAM_BASE_URL=http://localhost:8080
SEAM_CAPTURE_ENABLED=true
SEAM_CORPUS_DIR=./corpus
```

#### Production

```bash
SEAM_CALLER_PORT=8080
SEAM_OPERATOR_PORT=8081
SEAM_BASE_URL=https://seam.example.com
SEAM_CAPTURE_ENABLED=false  # Disable in production
```

---

## Health Check Interpretation

### Health Endpoints Overview

SEAM provides multiple health endpoints for different monitoring scenarios:

| Endpoint | Purpose | Access | Response Format |
|----------|---------|--------|-----------------|
| `/_seam/healthz` | Liveness probe | Caller port | Plain text "OK" |
| `/_seam/readyz` | Readiness probe | Caller port | JSON `{"ready": true}` |
| `/health/credentials` | Credential status | Operator port | JSON with circuit breaker state |
| `/health/upstreams` | Upstream connectivity | Caller port | JSON with upstream health |
| `/_seam/metrics` | Prometheus metrics | Operator port | Prometheus text format |

### Liveness Probe (`/_seam/healthz`)

**Purpose:** Verify the process is running and responsive.

**Expected Response:** HTTP 200 with body `OK`

**Failure Diagnosis:**

```bash
# Test liveness
curl http://localhost:8080/_seam/healthz

# Expected: OK
# If timeout or connection refused:
# 1. Check if SEAM process is running
# 2. Check port binding: ss -tlnp | grep 8080
# 3. Review startup logs for initialization errors
```

**Common Issues:**
- Port already in use (change `SEAM_CALLER_PORT`)
- Spec directory missing or invalid
- Fragment validation errors blocking startup

### Readiness Probe (`/_seam/readyz`)

**Purpose:** Verify SEAM is ready to accept traffic.

**Expected Response:** HTTP 200 with JSON `{"ready": true}`

**Current Behavior:** Always returns `ready: true` in Phase 1a. Future phases will add dependency gating (OpenBao login, spec loading).

**Failure Diagnosis:**

```bash
# Test readiness
curl http://localhost:8080/_seam/readyz

# Expected: {"ready":true}
# If missing or false:
# 1. Check spec loading completed successfully
# 2. Verify OpenBao authentication (future phase)
# 3. Review initialization logs
```

### Credential Health (`/health/credentials`)

**Purpose:** Monitor OpenBao credential health and circuit breaker state.

This is a read-only operator endpoint. It is a reserved path, bypasses the
cache and quota middleware, and sends `Cache-Control: no-store`, so an open or
half-open breaker is visible without waiting for a cached health response.
The aggregate `circuit_breaker` field is accompanied by per-origin
`circuit_breakers` entries when breaker state has been published.

**Expected Response:**

```json
{
  "status": "healthy",
  "timestamp": "2026-08-15T10:30:00Z",
  "credentials": {
    "available": true,
    "last_refresh": "2026-08-15T00:00:00Z"
  },
  "circuit_breaker": {
    "enabled": false,
    "state": "closed",
    "note": "Per-origin circuit breaker implementation pending (bead seam-b8d97cbb)"
  }
}
```

**Field Meanings:**

- `status`: Overall credential health (`healthy`, `degraded`, `unhealthy`)
- `credentials.available`: Whether credentials are currently accessible
- `credentials.last_refresh`: Last successful credential refresh timestamp
- `circuit_breaker.state`: Circuit breaker state (`closed`, `open`, `half_open`)
  - `closed`: Normal operation, requests flow through
  - `open`: Circuit tripped, requests fail fast
  - `half_open`: Testing if upstream has recovered

**Failure Diagnosis:**

```bash
# Check credential health
curl http://localhost:8081/health/credentials

# If status != "healthy":
# 1. Check OpenBao connectivity (see OpenBao Debugging section)
# 2. Verify ServiceAccount token is valid
# 3. Review circuit breaker trip logs
# 4. Check OpenBao role/policy configuration
```

### Upstream Health (`/health/upstreams`)

**Purpose:** Monitor backend service connectivity.

**Expected Response:**

```json
{
  "status": "healthy",
  "timestamp": "2026-08-15T10:30:00Z",
  "upstreams": {
    "total": 5,
    "healthy": 5,
    "degraded": 0,
    "down": 0
  },
  "circuit_breakers": [
    {
      "upstream": "example-service",
      "state": "closed",
      "consecutive_failures": 0,
      "enabled": false,
      "note": "Per-origin circuit breaker implementation pending (bead seam-b8d97cbb)"
    }
  ]
}
```

**Field Meanings:**

- `upstreams.total`: Total number of configured upstreams
- `upstreams.healthy`: Upstreams responding normally
- `upstreams.degraded`: Upstreams responding slowly or with errors
- `upstreams.down`: Upstreams completely unreachable

**Failure Diagnosis:**

```bash
# Check upstream health
curl http://localhost:8080/health/upstreams

# If any upstream is down/degraded:
# 1. Test upstream directly: curl http://upstream-service/health
# 2. Check network connectivity from SEAM pod
# 3. Verify DNS resolution: nslookup upstream-service
# 4. Review upstream service logs
# 5. Check circuit breaker state for repeated failures
```

### Metrics Endpoint (`/_seam/metrics`)

**Purpose:** Expose Prometheus metrics for monitoring.

**Key Metrics:**

```prometheus
# Build info
seam_build_info{version="...",commit="...",go_version="..."} 1

# Cache metrics
seam_cache_hits_total
seam_cache_misses_total
seam_cache_evictions_total
seam_cache_size

# Quota metrics
seam_quota_cost_total{route="..."}
seam_quota_bypassed_total{reason="cache-hit"}
seam_quota_remaining

# HTTP metrics (if instrumented)
http_requests_total{method="",path="",status=""}
http_request_duration_seconds{path=""}
```

**Monitoring Queries:**

```promql
# Cache hit rate
rate(seam_cache_hits_total[5m]) / (rate(seam_cache_hits_total[5m]) + rate(seam_cache_misses_total[5m]))

# Quota utilization
(1 - seam_quota_remaining / seam_quota_limit) * 100

# Error rate
rate(http_requests_total{status=~"5.."}[5m]) / rate(http_requests_total[5m])
```

---

## Common Failure Modes

### 1. Startup Failures

#### Spec Directory Missing

**Symptoms:**
- SEAM fails to start
- Log: `Failed to initialize spec loader: no such file or directory`

**Diagnosis:**
```bash
# Check spec directory exists
ls -la /app/spec/

# Verify ConfigMap is mounted
kubectl describe configmap seam-spec -n seam
kubectl get pod -n seam -l app=seam -o json | jq '.items[0].spec.volumes'
```

**Resolution:**
```bash
# Create ConfigMap from spec files
kubectl create configmap seam-spec -n seam \
  --from-file=spec/ \
  --dry-run=client -o yaml | kubectl apply -f -

# Restart pods
kubectl rollout restart deployment seam -n seam
```

#### Fragment Validation Failures

**Symptoms:**
- SEAM starts but reports quarantined fragments
- Log: `Fragment quarantined: ... - schema validation failed`

**Diagnosis:**
```bash
# Check operator endpoint for fragment status
curl http://localhost:8081/config/status

# Response shows:
# {
#   "fragments_loaded": true,
#   "valid_count": 2,
#   "quarantined_count": 3,
#   "conditions": [...]
# }
```

**Resolution:**

```bash
# Validate fragment locally against schema
validate_fragment() {
  local fragment=$1
  local schema="/path/to/route-fragment-schema.json"
  
  ajv validate \
    --spec=$schema \
    --data=$fragment \
    --errors=json
}

# Fix fragment and reapply
kubectl apply -f fragments/test-service/fixed-route.json
```

#### Port Already in Use

**Symptoms:**
- SEAM fails to bind to port
- Log: `failed to bind caller port 8080: bind: address already in use`

**Diagnosis:**
```bash
# Check what's using the port
ss -tlnp | grep 8080

# Or using lsof
lsof -i :8080
```

**Resolution:**
```bash
# Option 1: Kill conflicting process
kill -9 <PID>

# Option 2: Change SEAM port
export SEAM_CALLER_PORT=8888
```

### 2. Runtime Failures

#### Cache Errors

**Symptoms:**
- High cache miss rate
- Error logs about cache expiration
- Metrics show `seam_cache_evictions_total` increasing rapidly

**Diagnosis:**
```bash
# Check cache status
curl http://localhost:8081/_seam/cache/status

# Response:
# {
#   "enabled": true,
#   "size": 1000,
#   "hits": 5000,
#   "misses": 15000,
#   "evictions": 500,
#   "hit_rate": 0.25
# }
```

**Resolution:**

```bash
# Manual cache cleanup
curl -X POST http://localhost:8081/_seam/cache/cleanup

# Adjust TTL configuration in fragments
# Add to route fragment:
# "x-cache-ttl": 300  # 5 minutes instead of default
```

#### Quota Exhaustion

**Symptoms:**
- HTTP 429 Too Many Requests responses
- Log: `quota exceeded for caller`
- Metrics show `seam_quota_remaining = 0`

**Diagnosis:**
```bash
# Check quota metrics
curl http://localhost:8081/_seam/metrics | grep quota

# Look for:
# seam_quota_cost_total
# seam_quota_bypassed_total
# seam_quota_remaining
```

**Resolution:**

```bash
# Option 1: Increase quota limit
# Update fragment configuration:
# "x-quota": {
#   "limit": 10000,
#   "cost_per_call": 1
# }

# Option 2: Reduce quota cost
# "x-quota": {
#   "cost_per_call": 0.5
# }

# Option 3: Enable caching to bypass quota
# "x-cache-ttl": 300
```

#### Fragment Hot-Reload Failures

**Symptoms:**
- New routes not appearing
- Spec hash not updating
- Log: `Failed to reload fragment: ...`

**Diagnosis:**
```bash
# Check current spec version
curl -I http://localhost:8080/openapi.json

# Look for X-SEAM-Spec-Version header
# Compare with expected hash from fragment files
```

**Resolution:**

```bash
# Trigger reload (if hot-reload is implemented)
curl -X POST http://localhost:8081/_seam/config/reload

# Or restart pods
kubectl rollout restart deployment seam -n seam
```

### 3. Network Failures

#### DNS Resolution Failures

**Symptoms:**
- Upstream connection timeouts
- Log: `no such host` or `lookup upstream-service on 10.96.0.10:53: no such host`

**Diagnosis:**
```bash
# Test DNS from within pod
kubectl exec -n seam deployment/seam -- nslookup upstream-service

# Check CoreDNS is functioning
kubectl exec -n kube-system deployment/coredns -- nslookup kubernetes.default
```

**Resolution:**

```bash
# Verify Service exists
kubectl get svc upstream-service

# Check Service DNS name format:
# <service-name>.<namespace>.svc.cluster.local

# Update fragment with correct upstream URL
# "x-seam-upstream": "http://upstream-service.default.svc.cluster.local"
```

#### Connection Timeouts

**Symptoms:**
- Slow responses
- Log: `dial tcp 10.0.0.1:8080: i/o timeout`

**Diagnosis:**

```bash
# Test connectivity from pod
kubectl exec -n seam deployment/seam -- \
  curl -v http://upstream-service:8080/health

# Check network policies
kubectl get networkpolicies -n seam

# Check Service Endpoints
kubectl get endpoints upstream-service
```

**Resolution:**

```bash
# Create NetworkPolicy to allow traffic
kubectl apply -f - <<EOF
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: seam-to-upstream
  namespace: seam
spec:
  podSelector:
    matchLabels:
      app: seam
  policyTypes:
  - Egress
  egress:
  - to:
    - podSelector:
        matchLabels:
          app: upstream-service
    ports:
    - protocol: TCP
      port: 8080
EOF
```

### 4. Resource Exhaustion

#### Memory Leaks

**Symptoms:**
- Pod OOMKilled
- Memory usage steadily increasing
- Metrics show rising `go_memstats_alloc_bytes`

**Diagnosis:**

```bash
# Check memory metrics
curl http://localhost:8081/_seam/metrics | grep memstats

# Look for:
# go_memstats_alloc_bytes
# go_memstats_heap_alloc_bytes
# go_memstats_stack_inuse_bytes

# Check pod memory usage
kubectl top pod -n seam -l app=seam
```

**Resolution:**

```bash
# Increase memory limits in deployment
resources:
  limits:
    memory: "512Mi"  # Increase from 256Mi

# Enable Go memory profiling (development only)
export GODEBUG=gctrace=1
```

#### Goroutine Leaks

**Symptoms:**
- High goroutine count
- Slow response times
- CPU usage elevated

**Diagnosis:**

```bash
# Check goroutine count
curl http://localhost:8081/_seam/metrics | grep go_goroutines

# If > 1000 goroutines, investigate leak
```

**Resolution:**

```bash
# Enable goroutine profiling (development)
curl http://localhost:8081/_seam/debug/pprof/goroutine?debug=1

# Restart pods to clear leak
kubectl rollout restart deployment seam -n seam
```

---

## OpenBao Connectivity Debugging

### Prerequisites

Before debugging OpenBao connectivity, ensure you have:

1. **Cluster Access:** kubectl configured for target cluster
2. **OpenBao CLI:** `bao` binary installed and accessible
3. **ServiceAccount Token:** Valid JWT token for authentication

### Architecture

```
┌─────────────┐              ┌──────────────┐              ┌─────────────┐
│   SEAM Pod  │ ──JWT Token──▶│  OpenBao     │◀────────────▶│   Secrets   │
│             │              │  (rs-manager)│              │   (routes)  │
└─────────────┘              └──────────────┘              └─────────────┘
       │                              │
       │ Kubernetes Auth Method       │
       └──────────────────────────────┘
```

### Authentication Flow

1. SEAM pod starts with ServiceAccount `seam`
2. Pod's JWT token is mounted at `/var/run/secrets/kubernetes.io/serviceaccount/token`
3. SEAM authenticates to OpenBao using Kubernetes auth method
4. OpenBao validates JWT against Kubernetes API
5. OpenBao returns a client token with policy-based access
6. SEAM uses client token to access secrets at `secret/data/seam/routes/*`

### Verification Steps

#### 1. Verify OpenBao Infrastructure

```bash
# Check OpenBao pod is running
kubectl get pods -n openbao -l app=openbao

# Check OpenBao service
kubectl get svc -n openbao openbao

# Test OpenBao API from bastion host
curl -k https://openbao.openbao.svc.cluster.local:8200/v1/sys/health
```

#### 2. Verify Kubernetes Auth Method

```bash
# Set OpenBao address
export BAO_ADDR=https://openbao.ardenone.com:8200

# Login with admin token (stored securely!)
export BAO_TOKEN=$(bao login -method=token -token=$YOUR_ADMIN_TOKEN)

# Check if Kubernetes auth method is enabled
bao auth list | grep kubernetes

# Verify auth method configuration
bao read auth/kubernetes/role/seam
```

**Expected Output:**

```json
{
  "data": {
    "bound_service_account_names": ["seam"],
    "bound_service_account_namespaces": ["seam"],
    "token_ttl": "24h",
    "token_max_ttl": "72h",
    "policies": ["seam"]
  }
}
```

#### 3. Verify ServiceAccount Exists

```bash
# Check ServiceAccount
kubectl get sa seam -n seam

# Describe ServiceAccount
kubectl describe sa seam -n seam
```

#### 4. Verify Policy Exists

```bash
# Read policy
bao read policy/seam

# Expected policy HCL:
# path "secret/data/seam/routes/*" {
#   capabilities = ["read"]
# }
#
# path "secret/data/seam-retirement-evaluator/*" {
#   capabilities = ["deny"]
# }
#
# path "secret/data/*" {
#   capabilities = ["deny"]
# }
```

#### 5. Test JWT Login

```bash
# Get JWT token from a running SEAM pod
SEAM_POD=$(kubectl get pod -n seam -l app=seam -o jsonpath='{.items[0].metadata.name}')
JWT_TOKEN=$(kubectl exec -n seam $SEAM_POD -- cat /var/run/secrets/kubernetes.io/serviceaccount/token)

# Test login
bao write auth/kubernetes/login role=seam jwt=$JWT_TOKEN

# Expected response includes:
# {
#   "auth": {
#     "client_token": "hvs.xxx",
#     "policies": ["seam", "default"],
#     "metadata": {
#       "role": "seam",
#       "service_account_name": "seam",
#       "service_account_namespace": "seam"
#     }
#   }
# }
```

#### 6. Test Secret Access

```bash
# Using the client token from step 5
export BAO_CLIENT_TOKEN="hvs.xxx"

# Try to read a route secret
bao read secret/data/seam/routes/test-route

# If this fails with "permission denied", policy is misconfigured
# If this fails with "invalid path", secret doesn't exist
```

### Common OpenBao Issues

#### Issue 1: ServiceAccount Not Found

**Symptoms:**
```
Error: serviceaccount "seam" not found
```

**Resolution:**

```bash
# Create ServiceAccount
kubectl apply -f - <<EOF
apiVersion: v1
kind: ServiceAccount
metadata:
  name: seam
  namespace: seam
EOF
```

#### Issue 2: Role Not Configured

**Symptoms:**
```
Error: *errors.statusError: 400 Bad Request
role "seam" does not exist
```

**Resolution:**

```bash
# Create role in OpenBao
bao write auth/kubernetes/role/seam \
  bound_service_account_names=seam \
  bound_service_account_namespaces=seam \
  token_ttl=24h \
  token_max_ttl=72h \
  policies=seam
```

#### Issue 3: Policy Not Configured

**Symptoms:**
```
Error: *errors.statusError: 403 Forbidden
permission denied
```

**Resolution:**

```bash
# Create policy file
cat > seam-policy.hcl <<'EOF'
path "secret/data/seam/routes/*" {
  capabilities = ["read"]
}

path "secret/data/seam-retirement-evaluator/*" {
  capabilities = ["deny"]
}

path "secret/data/*" {
  capabilities = ["deny"]
}
EOF

# Write policy to OpenBao
bao policy write seam seam-policy.hcl
```

#### Issue 4: JWT Token Validation Failed

**Symptoms:**
```
Error: *errors.statusError: 401 Unauthorized
lookup failed: lookup failed: No matching tenant
```

**Resolution:**

```bash
# Check Kubernetes auth method configuration
bao read auth/kubernetes/config

# Verify kubernetes_host and kubernetes_port are correct
# Should point to Kubernetes API server

# Common issue: Wrong certificate
# Verify the CA certificate is correct
bao read auth/kubernetes/cert/ca
```

#### Issue 5: Network Connectivity

**Symptoms:**
```
Error: dial tcp: lookup openbao on 10.96.0.10:53: no such host
```

**Resolution:**

```bash
# Test DNS from SEAM pod
kubectl exec -n seam deployment/seam -- nslookup openbao.openbao.svc.cluster.local

# Test connectivity
kubectl exec -n seam deployment/seam -- \
  curl -k https://openbao.openbao.svc.cluster.local:8200/v1/sys/health

# If failing, check NetworkPolicies allow egress to OpenBao namespace
kubectl get networkpolicies -n seam
```

### Debugging Workflow

When OpenBao connectivity fails, follow this workflow:

```bash
# 1. Verify OpenBao is running
kubectl get pods -n openbao -l app=openbao

# 2. Verify ServiceAccount exists
kubectl get sa seam -n seam

# 3. Verify role exists
bao read auth/kubernetes/role/seam

# 4. Verify policy exists
bao read policy/seam

# 5. Test JWT login manually
JWT_TOKEN=$(kubectl exec -n seam deployment/seam -- cat /var/run/secrets/kubernetes.io/serviceaccount/token)
bao write auth/kubernetes/login role=seam jwt=$JWT_TOKEN

# 6. Test secret access with client token
# (using token from step 5)
bao read secret/data/seam/routes/test-route

# 7. Check SEAM logs for authentication errors
kubectl logs -n seam deployment/seam --tail=100 | grep -i openbao
```

---

## Log Analysis Procedures

### Log Locations

| Component | Log Location | Access Method |
|-----------|--------------|---------------|
| SEAM Server | stdout/stderr | `kubectl logs -n seam deployment/seam` |
| OpenBao | stdout/stderr | `kubectl logs -n openbao deployment/openbao` |
| Argo Workflows | Archive S3 | Viaargo CLI |

### Log Formats

#### SEAM Server Logs

**Startup Logs:**
```
2026/08/15 10:30:00.123456 Starting SEAM gateway server:
2026/08/15 10:30:00.123500   Caller-facing port: 8080
2026/08/15 10:30:00.123505   Operator-only port: 8081
2026/08/15 10:30:00.123510   Base URL: http://localhost:8080
2026/08/15 10:30:00.123515   Spec directory: ./spec
2026/08/15 10:30:00.123520   Fragment mode: true
2026/08/15 10:30:00.123525   Capture enabled: false
2026/08/15 10:30:00.123530 [Loader] Creating new fragment-mode loader
2026/08/15 10:30:00.124000 [Fragment] Loading fragments from directory: ./fragments
2026/08/15 10:30:00.124100 [Fragment] Successfully loaded fragment: fragments/test-service/test-route.json
2026/08/15 10:30:00.124200 [Fragment] Fragment loading complete: 5 loaded, 2 errors
2026/08/15 10:30:00.124300 [Loader] Generated spec hash: 7afe06f4...
2026/08/15 10:30:00.124400 Caller-facing server listening on :8080
2026/08/15 10:30:00.124500 Operator-only server listening on :8081
```

**Request Logs:**
```
2026/08/15 10:35:00.123456 [/docs] Successfully fetched and validated merged spec (5036 bytes)
2026/08/15 10:35:01.234567 [Cache] Cache hit for /test/get (TTL: 300s)
2026/08/15 10:35:02.345678 [Quota] Cost deducted: 1.0, remaining: 999.0
2026/08/15 10:35:03.456789 [OpenBao] Secret retrieved: secret/data/seam/routes/test-route
```

**Error Logs:**
```
2026/08/15 10:40:00.123456 [Fragment] Warning: failed to load fragment fragments/argocd-ro/1-argocd-read-only-proxy.yaml: failed to parse JSON: invalid character '#' looking for beginning of value
2026/08/15 10:40:01.234567 [OpenBao] Error authenticating to OpenBao: *errors.statusError: 401 Unauthorized
2026/08/15 10:40:02.345678 [Cache] Error: cache entry expired, eviction count increased
```

### Log Analysis Patterns

#### Searching for Errors

```bash
# Get all error logs
kubectl logs -n seam deployment/seam | grep -i error

# Get OpenBao-related errors
kubectl logs -n seam deployment/seam | grep -i openbao

# Get cache-related errors
kubectl logs -n seam deployment/seam | grep -i cache

# Get quota-related errors
kubectl logs -n seam deployment/seam | grep -i quota
```

#### Analyzing Request Patterns

```bash
# Count requests by path
kubectl logs -n seam deployment/seam | grep -oP '\[\K[^\]]+' | sort | uniq -c

# Find slow requests (if timing logs enabled)
kubectl logs -n seam deployment/seam | grep -P 'duration=\K\d+' | awk '$1 > 1000'

# Track cache hit/miss ratio
kubectl logs -n seam deployment/seam | grep -c 'Cache hit'
kubectl logs -n seam deployment/seam | grep -c 'Cache miss'
```

#### Monitoring Health Checks

```bash
# Track health probe failures
kubectl logs -n seam deployment/seam | grep '/_seam/healthz'

# Check readiness probe status
kubectl logs -n seam deployment/seam | grep '/_seam/readyz'

# Monitor credential health
kubectl logs -n seam deployment/seam | grep '/health/credentials'
```

### Log Aggregation

For production deployments, integrate SEAM logs with your logging system:

#### Loki Integration

```yaml
# Add to SEAM pod spec
annotations:
  prometheus.io/scrape: "true"
  prometheus.io/port: "8081"
  prometheus.io/path: "/_seam/metrics"
```

#### Elasticsearch Integration

```yaml
# Add Filebeat sidecar
apiVersion: apps/v1
kind: Deployment
spec:
  template:
    spec:
      containers:
      - name: filebeat
        image: docker.elastic.co/beats/filebeat:8.0.0
        volumeMounts:
        - name: logs
          mountPath: /var/log
      volumes:
      - name: logs
        emptyDir: {}
```

---

## Rollback Strategies

### Deployment Rollback

#### Immediate Rollback

```bash
# Check rollout history
kubectl rollout history deployment/seam -n seam

# Rollback to previous revision
kubectl rollout undo deployment/seam -n seam

# Rollback to specific revision
kubectl rollout undo deployment/seam -n seam --to-revision=2
```

#### Safe Rollback Procedure

1. **Verify Current State**
   ```bash
   # Get current revision
   kubectl get deployment seam -n seam -o jsonpath='{.metadata.annotations.deployment\.kubernetes\.io/revision}'
   
   # Check pod status
   kubectl get pods -n seam -l app=seam
   
   # Run health checks
   curl http://seam.example.com/_seam/healthz
   curl http://seam.example.com/_seam/readyz
   ```

2. **Initiate Rollback**
   ```bash
   # Start rollback
   kubectl rollout undo deployment/seam -n seam
   
   # Watch rollout status
   kubectl rollout status deployment/seam -n seam
   ```

3. **Verify Rollback Success**
   ```bash
   # Check pods are running
   kubectl get pods -n seam -l app=seam
   
   # Verify health endpoints
   curl http://seam.example.com/_seam/healthz
   curl http://seam.example.com/_seam/readyz
   
   # Check metrics are normal
   curl http://seam.example.com:8081/_seam/metrics | grep seam_cache
   ```

### Fragment Rollback

#### Fragment Versioning

Fragments should be versioned using Git commits:

```bash
# Tag fragment release
git tag -a seam-fragments-v1.0.0 -m "SEAM fragments v1.0.0"
git push origin seam-fragments-v1.0.0
```

#### Rollback Procedure

```bash
# 1. Identify bad fragment
kubectl get configmap -n seam | grep fragments

# 2. Checkout previous version
git checkout seam-fragments-v1.0.0

# 3. Reapply fragments
kubectl apply -f fragments/

# 4. Restart SEAM to reload
kubectl rollout restart deployment seam -n seam

# 5. Verify spec hash matches expected
curl -I http://localhost:8080/openapi.json
```

### Configuration Rollback

#### Environment Variable Rollback

```bash
# 1. Get current deployment config
kubectl get deployment seam -n seam -o yaml > seam-deployment-current.yaml

# 2. Edit to restore previous environment variables
kubectl edit deployment seam -n seam

# 3. Wait for rollout
kubectl rollout status deployment seam -n seam
```

#### ConfigMap Rollback

```bash
# 1. List ConfigMap versions (if using versioned ConfigMaps)
kubectl get configmap -n seam -l app=seam

# 2. Apply previous version
kubectl apply -f declarative-config/k8s/seam/configmap-v1.yaml

# 3. Restart pods
kubectl rollout restart deployment seam -n seam
```

### Disaster Recovery

#### Backup Procedure

```bash
# 1. Backup fragments
kubectl get configmap -n seam -l app=seam -o yaml > seam-configmaps-backup.yaml

# 2. Backup deployment
kubectl get deployment seam -n seam -o yaml > seam-deployment-backup.yaml

# 3. Backup OpenBao secrets (requires admin token)
bao export -format=json > openbao-secrets-backup.json

# 4. Store backups securely
gpg --encrypt --recipient backup@example.com seam-configmaps-backup.yaml
```

#### Restore Procedure

```bash
# 1. Restore ConfigMaps
kubectl apply -f seam-configmaps-backup.yaml

# 2. Restore Deployment
kubectl apply -f seam-deployment-backup.yaml

# 3. Restore OpenBao secrets
bao import openbao-secrets-backup.json

# 4. Verify health
curl http://seam.example.com/_seam/healthz
```

---

## Performance Tuning

### Cache Optimization

#### TTL Tuning

```json
{
  "openapi": "3.1.0",
  "info": {
    "title": "Test Service Route",
    "version": "1.0.0"
  },
  "paths": {
    "/test/static": {
      "get": {
        "summary": "Get static data",
        "x-seam-cache-ttl": 3600,
        "x-seam-upstream": "http://test-service:8080/static",
        "x-seam-secret-path": "secret/data/seam/routes/test-route"
      }
    }
  }
}
```

**TTL Guidelines:**
- Static content: 3600s (1 hour)
- API responses: 300s (5 minutes)
- Real-time data: 0s (no cache)

#### Cache Size Limits

```go
// In deployment configuration
env:
- name: SEAM_CACHE_SIZE
  value: "10000"  // Maximum cache entries
```

### Quota Optimization

#### Cost Per Call

```json
{
  "paths": {
    "/test/expensive": {
      "get": {
        "summary": "Expensive operation",
        "x-seam-quota": {
          "limit": 1000,
          "cost_per_call": 10
        }
      }
    },
    "/test/cheap": {
      "get": {
        "summary": "Cheap operation",
        "x-seam-quota": {
          "limit": 1000,
          "cost_per_call": 1
        }
      }
    }
  }
}
```

#### Quota Monitoring

```promql
# Quota utilization rate
(seam_quota_cost_total - seam_quota_bypassed_total) / seam_quota_limit

# Projected quota exhaustion
predict_linear(seam_quota_cost_total[5m], 3600)
```

### Resource Tuning

#### Memory Optimization

```yaml
resources:
  requests:
    memory: "128Mi"
    cpu: "100m"
  limits:
    memory: "512Mi"
    cpu: "1000m"
```

**Tuning Guidelines:**
- Small deployments: 128Mi/100m
- Medium deployments: 256Mi/500m
- Large deployments: 512Mi/1000m

#### Horizontal Scaling

```yaml
# Increase replicas based on load
spec:
  replicas: 5  # Scale horizontally

# Or use HPA
kubectl autoscale deployment seam \
  --namespace=seam \
  --cpu-percent=80 \
  --min=3 \
  --max=10
```

---

## Emergency Procedures

### Immediate Incident Response

#### 1. Service Down

**Symptoms:** All health checks failing

**Actions:**
```bash
# Check pods
kubectl get pods -n seam -l app=seam

# If no pods running:
kubectl scale deployment seam -n seam --replicas=3

# If pods crashing:
kubectl logs -n seam deployment/seam --tail=100
kubectl describe pod -n seam -l app=seam
```

#### 2. OpenBao Outage

**Symptoms:** All requests failing with authentication errors

**Actions:**
```bash
# Check OpenBao status
kubectl get pods -n openbao -l app=openbao

# If OpenBao down, restart:
kubectl rollout restart deployment openbao -n openbao

# If auth issue, verify:
bao read auth/kubernetes/role/seam
```

#### 3. High Error Rate

**Symptoms:** HTTP 5xx responses > 5%

**Actions:**
```bash
# Check error rate in metrics
curl http://localhost:8081/_seam/metrics | grep 'status="5.."'

# Identify failing upstream
curl http://localhost:8080/health/upstreams

# Check specific upstream logs
kubectl logs -n upstream deployment/upstream --tail=100
```

### Escalation Procedures

#### Severity Levels

| Severity | Response Time | Example |
|----------|---------------|---------|
| P0 - Critical | 15 minutes | Complete service outage |
| P1 - High | 1 hour | OpenBao authentication down |
| P2 - Medium | 4 hours | High error rate on one route |
| P3 - Low | 1 day | Performance degradation |

#### Escalation Contacts

| Role | Contact | Responsibility |
|------|---------|----------------|
| On-Call Engineer | PagerDuty | Initial triage |
| SEAM Lead | seam@example.com | Architecture decisions |
| OpenBao Admin | openbao@example.com | Secret management |
| Platform Lead | platform@example.com | Infrastructure issues |

### Run of Book Maintenance

#### Update Schedule

- **Monthly:** Review and update common failure modes
- **Quarterly:** Refresh deployment patterns and examples
- **Annually:** Complete review and restructuring

#### Contribution Process

1. Test new procedures in development environment
2. Document with examples and expected outputs
3. Submit PR to SEAM repository
4. Request review from SEAM lead
5. Update after approval

---

## Appendix

### Quick Reference Cards

#### Health Check Commands

```bash
# Liveness
curl http://seam.example.com/_seam/healthz

# Readiness
curl http://seam.example.com/_seam/readyz

# Credentials
curl http://seam.example.com:8081/health/credentials

# Upstreams
curl http://seam.example.com/health/upstreams

# Metrics
curl http://seam.example.com:8081/_seam/metrics

# Cache status
curl http://seam.example.com:8081/_seam/cache/status

# Config status
curl http://seam.example.com:8081/config/status
```

#### Common Debugging Commands

```bash
# Check pods
kubectl get pods -n seam -l app=seam

# View logs
kubectl logs -n seam deployment/seam -f

# Exec into pod
kubectl exec -n seam deployment/seam -- /bin/sh

# Port forward
kubectl port-forward -n seam deployment/seam 8080:8080 8081:8081

# Check events
kubectl get events -n seam --sort-by='.lastTimestamp'

# Describe deployment
kubectl describe deployment seam -n seam
```

#### OpenBao Debugging Commands

```bash
# Set up environment
export BAO_ADDR=https://openbao.ardenone.com:8200
export BAO_TOKEN=$(cat ~/.bao-token)

# Check role
bao read auth/kubernetes/role/seam

# Check policy
bao read policy/seam

# Test login
bao write auth/kubernetes/login role=seam jwt=$JWT_TOKEN

# Read secret
bao read secret/data/seam/routes/test-route
```

### Version History

| Version | Date | Changes |
|---------|------|---------|
| 1.0 | 2026-08-15 | Initial operational runbook |

### Related Documentation

- [SEAM README](../README.md) - Getting started guide
- [OpenBao Integration](openbao-workflow-templates.md) - OpenBao setup and workflows
- [Health Sentinel Integration](notes/health-sentinel-cache-integration.md) - Health check architecture
- [Testing Isolation Runbook](testing-isolation-runbook.md) - Testing procedures

### Support

For questions or issues with this runbook:
- **GitHub Issues:** https://github.com/jedarden/SEAM/issues
- **Documentation PRs:** https://github.com/jedarden/SEAM/pulls
- **Internal Questions:** #seam channel on Slack

---

**Document Status:** ✅ Complete  
**Last Review:** 2026-08-15  
**Next Review:** 2026-11-15  
