# Phase 4: z.ai/GLM End-to-End Injection Proof

**Task:** seam-9ed3097c

This document describes the complete Phase 4 implementation for z.ai/GLM credential injection through SEAM, including provisioning, testing, and verification.

## Overview

Phase 4 delivers the **first end-to-end injection proof** against a real credential:
- Caller sends request (with their own stripped copy of the credential)
- SEAM fetches the real credential from OpenBao
- SEAM injects the server-side value into the upstream request
- SEAM scrubs the credential from the response (echo scrubbing)

**Critical constraint:** NO agent cutover (Phase 6b). This is infrastructure setup only.

## Implementation Components

### 1. Route Fragment

**File:** `declarative-config/k8s/rs-manager/seam/routes/zai/zai-glm-proxy.yaml`

Fragment declares:
- `x-seam-owner: zai`
- `x-upstream: https://api.z.ai`
- `x-vault-path: seam/routes/zai/api-key` (OpenBao credential location)
- `x-inject-as: {kind: bearer}` (Authorization: Bearer <token>)
- `x-credential-probe: /v1/models` every 10 minutes (credential health check)
- Cost/quota guards on all metered endpoints (chat completions, embeddings)

### 2. OpenBao Credential Provisioning

**Workflow:** `declarative-config/k8s/iad-ci/argo-workflows/provision-zai-credential.yaml`

Provisions the secret at: `secret/data/seam/routes/zai/api-key`

**Submit the workflow:**

```bash
kubectl --kubeconfig=/home/coding/.kube/iad-ci.kubeconfig create -f - <<EOF
apiVersion: argoproj.io/v1alpha1
kind: Workflow
metadata:
  name: provision-zai-credential-$(date +%s)
  namespace: argo-workflows
spec:
  workflowTemplateRef:
    name: provision-zai-credential
  arguments:
    parameters:
    - name: api-key
      value: "YOUR_ZAI_API_KEY_HERE"  # Replace with actual key
    - name: dry-run
      value: "false"
EOF
```

**Dry-run first:**

```bash
kubectl --kubeconfig=/home/coding/.kube/iad-ci.kubeconfig create -f - <<EOF
apiVersion: argoproj.io/v1alpha1
kind: Workflow
metadata:
  name: provision-zai-credential-dryrun-$(date +%s)
  namespace: argo-workflows
spec:
  workflowTemplateRef:
    name: provision-zai-credential
  arguments:
    parameters:
    - name: api-key
      value: "test-key-dry-run"
    - name: dry-run
      value: "true"
EOF
```

### 3. Kubernetes Manifests

**Updated files:**
- `declarative-config/k8s/rs-manager/seam/kustomization.yaml` - Added zai ConfigMap generator
- `declarative-config/k8s/rs-manager/seam/deployment.yaml` - Added zai volumeMount and volume
- `declarative-config/k8s/rs-manager/seam/configmap-allowlist.yaml` - Added api.z.ai host

**Allowlist entry:**
```yaml
upstreamHosts:
  - "api.z.ai"  # Phase 4: z.ai/GLM public upstream
```

## End-to-End Injection Proof Test

### Test 1: Basic Credential Injection

**Setup:**
1. Provision the z.ai credential in OpenBao
2. Deploy SEAM with the z.ai fragment
3. Wait for ArgoCD sync

**Test:**

```bash
# Caller sends request with their own (stripped) credential
curl -X POST https://seam-rs-manager.tail1b1987.ts.net:8444/zai/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer CALLER_STRIPME_CREDENTIAL" \
  -d '{
    "model": "glm-4",
    "messages": [{"role": "user", "content": "Hello"}],
    "max_tokens": 10
  }'
```

**Expected behavior:**
- ✅ Upstream request contains SEAM's injected credential (Authorization: Bearer <real-key>)
- ✅ Caller's "CALLER_STRIPME_CREDENTIAL" is absent from upstream request
- ✅ Response contains the API response, not the credential
- ✅ Any error responses from upstream have the credential scrubbed: `[REDACTED-BY-SEAM]`

### Test 2: Echo Scrubbing

**Setup:** Use an endpoint that echoes the credential in error messages

**Test:**

```bash
# Send a malformed request that triggers an upstream error
curl -X POST https://seam-rs-manager.tail1b1987.ts.net:8444/zai/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{"invalid": "request"}'
```

**Expected behavior:**
- ✅ If upstream error body contains the credential, SEAM scrubs it
- ✅ Response shows `[REDACTED-BY-SEAM]` wherever credential appeared
- ✅ Headers are scrubbed (no Authorization header in response)

### Test 3: Credential Probe

**Test:**

```bash
# Check credential health endpoint
curl http://seam-rs-manager.tail1b1987.ts.net:8081/_seam/health/credentials
```

**Expected response:**

```json
{
  "fragment": "zai",
  "instance": null,
  "path": "seam/routes/zai/api-key",
  "status": "valid",
  "last_probe": "2026-08-27T12:00:00Z",
  "probe_result": {
    "status": 200,
    "error": null
  },
  "probe_interval": "10m"
}
```

### Test 4: Cost Guard Enforcement

**Test:**

```bash
# Check budget remaining
curl -X POST https://seam-rs-manager.tail1b1987.ts.net:8444/zai/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{"model": "glm-4", "messages": [{"role": "user", "content": "test"}], "max_tokens": 5}' \
  -w "\nX-SEAM-Budget-Remaining: %{header_seam_budget_remaining}\n"
```

**Expected response headers:**

```
X-SEAM-Budget-Remaining: amount=9900,unit=quota,window=1h,resets=2026-08-27T13:00:00Z
```

## Verification Checklist

Before closing bead seam-9ed3097c:

- [ ] Fragment created: `routes/zai/zai-glm-proxy.yaml`
- [ ] Fragment has correct x-vault-path: `seam/routes/zai/api-key`
- [ ] Fragment has correct x-inject-as: `{kind: bearer}`
- [ ] Fragment has x-credential-probe configured
- [ ] Allowlist includes `api.z.ai`
- [ ] Kustomization includes zai ConfigMap generator
- [ ] Deployment includes zai volumeMount and volume
- [ ] OpenBao provisioning workflow created
- [ ] Dry-run workflow execution succeeds
- [ ] (After deployment) Basic injection test passes
- [ ] (After deployment) Echo scrubbing test passes
- [ ] (After deployment) Credential probe reports valid
- [ ] (After deployment) Cost guard headers present

## OpenBao Path Ownership

The credential lives at: `secret/data/seam/routes/zai/api-key`

This path is:
- **Co-owned** by the zai fragment (x-seam-owner: zai)
- **Bound** by the allowlist prefix: `seam/routes/`
- **Accessible** to SEAM via its Kubernetes auth role (seam policy)

SEAM's OpenBao policy allows reading from `secret/data/seam/routes/*` and denies all other paths.

## Deployment Sequence (Two-PR Pattern)

Per the plan, onboarding a new service requires **two PRs**:

### PR 1: ConfigMap Volume + Allowlist (SEAM pod manifest)
**Files:** `declarative-config/k8s/rs-manager/seam/`
- `deployment.yaml` - Add routes-zai volumeMount and volume
- `configmap-allowlist.yaml` - Add api.z.ai host

**Result:** Rollout (pod restart), ~5-30 second gap

### PR 2: Fragment (ConfigMap edit, hot-reloaded)
**Files:** `declarative-config/k8s/rs-manager/seam/`
- `kustomization.yaml` - Add zai ConfigMap generator
- `routes/zai/zai-glm-proxy.yaml` - Fragment content

**Result:** Hot reload, no restart

## Notes

- This is **Phase 4 only** - NO agent cutover (that's Phase 6b)
- The cost governor (Phase 13) is **not yet enforced** - quota is route-wide before Phase 7 per-caller splitting
- This is a **pilot upstream** - twitterapi.io is the other Phase 4 target
- The fragment assumes z.ai uses a Bearer token auth pattern
- Adjust the auth shape if z.ai actually uses a different header (e.g., `x-api-key`)

## References

- Plan: `docs/plan/plan.md` - Phase 4 description, acceptance criteria
- Fragment schema: `docs/notes/route-fragment-schema-v1.md`
- OpenBao auth: `docs/research/openbao-kubernetes-auth-seam-research.md`
- TwitterAPI precedent: `docs/research/twitterapi-proxy-deployment.md`

## Status

**Last updated:** 2026-08-27
**Phase:** 4 - z.ai/GLM fragment + credential + e2e injection proof
**Next phase:** Phase 5 (kubectl-proxy map) or Phase 6a (Hosting foundation complete)
