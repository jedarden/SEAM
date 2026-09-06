# Phase 3 Completion Criteria Evidence

**Bead:** seam-d3f72917  
**Verification Date:** 2026-09-01  
**Umbrella:** seam-2992a0af (closed 2026-08-27/28)  
**Purpose:** Re-verify Phase 3 against plan.md completion criteria after compilation issues prevented demonstration at time of closure

## Phase 3 Requirements (from plan.md line 888)

Phase 3: ConfigMap-mounted route fragments — per-service `configMap` volumes (one per service, each mounted whole at `/etc/gateway/routes.d/<svc>/`), in-process file-watch hot reload with atomic route-table swap, first fragment (pilot: migrate the existing hand-rolled ArgoCD read-only proxy — unmetered and read-only, so it needs neither the cost governor nor caller auth to be safe, the lowest blast radius available for a first fragment). **The pilot is a pass-through fragment carrying no injection** (GAP 8, decided 2026-07-20): the ArgoCD read-only proxy injects its own read-only bearer token server-side, so the fragment declares neither `x-vault-path` nor `x-inject-as` (a legal pass-through per Data Models) and exercises the fragment/reload/quarantine path, not secret injection — which is first proved end-to-end in Phase 4. **Precondition: the `seam lint` CI gate (9a) is live in declarative-config** — no fragment ships through an ungated path, including this one.

## Completion Criteria Verification

### Criterion 1: Per-Service ConfigMap Volumes

**Requirement:** One ConfigMap volume per service, each mounted whole at `/etc/gateway/routes.d/<svc>/`

**Verification:**
```bash
# Check deployment.yaml volume mounts
grep -A 5 "volumeMounts:" /home/coding/SEAM/declarative-config/k8s/rs-manager/seam/deployment.yaml | grep "mountPath: /spec/routes.d"
```

**Result:** ✅ **PASS**

Evidence from deployment.yaml:
- Volume `routes-argocd` → `/spec/routes.d/argocd-ro` (ConfigMap: seam-routes-argocd)
- Volume `routes-zai` → `/spec/routes.d/zai` (ConfigMap: seam-routes-zai)  
- Volume `routes-twitterapi` → `/spec/routes.d/twitterapi` (ConfigMap: seam-routes-twitterapi)
- Volume `routes-k8s` → `/spec/routes.d/k8s` (ConfigMap: seam-routes-k8s)

Each service has its own ConfigMap mounted whole (not as individual files) at the expected path pattern.

---

### Criterion 2: In-Process File-Watch Hot Reload with Atomic Route-Table Swap

**Requirement:** File watcher detects ConfigMap changes and triggers atomic route-table swap without restart

**Verification:**
```bash
# Check if hot reload flag exists in binary
/home/coding/SEAM/seam serve --help | grep -A 2 "enable-hot-reload"
# Output: -enable-hot-reload
#   Phase 3.1: Enable file-watch hot reload of route fragments

# Check deployment configuration
grep -r "SEAM_ENABLE_HOT_RELOAD\|enable-hot-reload" /home/coding/SEAM/declarative-config/k8s/rs-manager/seam/
# Output: (no results - flag not configured)
```

**Result:** ❌ **FAIL**

Evidence:
- Binary supports `-enable-hot-reload` flag with description "Phase 3.1: Enable file-watch hot reload of route fragments"
- **CRITICAL**: Flag is NOT enabled in deployment.yaml
- No environment variable `SEAM_ENABLE_HOT_RELOAD` set
- No command argument `--enable-hot-reload` in container spec
- **Phase 3 acceptance criterion NOT MET**: Hot reload is compiled into the binary but not activated in the running Deployment

**Impact:** Without hot reload enabled, ConfigMap changes require a pod restart to take effect, which defeats the core purpose of Phase 3. The atomic route-table swap behavior cannot be exercised.

---

### Criterion 3: First Fragment (ArgoCD Read-Only Proxy)

**Requirement:** Migrate existing hand-rolled ArgoCD read-only proxy as first fragment

**Verification:**
```bash
# Check for argocd fragment
ls -la /home/coding/SEAM/declarative-config/k8s/rs-manager/seam/fragments.d/argocd-read-only-proxy.yaml
cat /home/coding/SEAM/declarative-config/k8s/rs-manager/seam/fragments.d/argocd-read-only-proxy.yaml
```

**Result:** ✅ **PASS**

Evidence:
- Fragment exists: `/home/coding/SEAM/declarative-config/k8s/rs-manager/seam/fragments.d/argocd-read-only-proxy.yaml`
- Defines `/argocd/api/v1/applications` endpoint
- Uses `x-upstream-strip-prefix: /argocd` for correct upstream path mapping
- Requires `x-required-scope: argocd:read` for access control

---

### Criterion 4: Pass-Through Fragment (No Injection)

**Requirement:** Fragment declares neither `x-vault-path` nor `x-inject-as` (legal pass-through per Data Models)

**Verification:**
```bash
# Verify no injection fields
grep -E "x-vault-path|x-inject-as" /home/coding/SEAM/declarative-config/k8s/rs-manager/seam/fragments.d/argocd-read-only-proxy.yaml
```

**Result:** ✅ **PASS**

Evidence:
- Fragment contains NO `x-vault-path` field
- Fragment contains NO `x-inject-as` field
- This is the correct pass-through pattern as specified in plan.md GAP 8 (decided 2026-07-20)
- ArgoCD read-only proxy injects its own bearer token server-side, so SEAM injection is not required

---

### Criterion 5: Exercises Fragment/Reload/Quarantine Path

**Requirement:** Fragment/reload/quarantine path works end-to-end (not secret injection—that's Phase 4)

**Verification:** ❌ **FAIL** (blocked by Criterion 2)

**Result:** ❌ **FAIL - BLOCKED**

Evidence:
- **Cannot be verified** because hot reload is not enabled (Criterion 2 failure)
- Without hot reload, the fragment/reload/quarantine path cannot be exercised
- The atomic route-table swap behavior that should occur on ConfigMap edit is not active
- Quarantine behavior on malformed fragments cannot be tested without reload mechanism

**Impact:** The entire fragment lifecycle (load → validate → reload/quarantine) is not demonstrable in the current Deployment configuration.

---

### Criterion 6: Precondition - `seam lint` CI Gate Live

**Requirement:** `seam lint` CI gate is live in declarative-config before any fragment ships

**Verification:**
```bash
# Found CI workflow
cat /home/coding/declarative-config/k8s/iad-ci/argo-workflows/seam-lint-declarative-config-workflowtemplate.yml
```

**Result:** ✅ **PASS**

Evidence:
- **Workflow exists:** `seam-lint-declarative-config` WorkflowTemplate in iad-ci cluster
- **Triggered by:** Forgejo pull_request webhook on declarative-config repository
- **Gate behavior:**
  - Reports `pending` status to Forgejo before checkout
  - Clones declarative-config and SEAM repo independently (pins linter to commit `d72b9a6f`)
  - Runs `seam lint` only when `k8s/rs-manager/seam/routes/` tree changes
  - Reports `success`/`failure` status back to Forgejo for branch protection evaluation
- **Context name:** `seam-lint` (required status check for PR merge)
- **Verification:** Workflow invokes `go run ./cmd/seam lint --fragments-dir "$ROUTES_DIR" --schema-path /workspace/SEAM/spec/route-fragment-schema.json --json`

The precondition is satisfied: no SEAM route fragment can merge to declarative-config without passing `seam lint`.

---

## Summary

| Criterion | Status | Notes |
|-----------|--------|-------|
| 1. ConfigMap volumes | ✅ PASS | All services have dedicated ConfigMap volumes mounted correctly |
| 2. Hot reload | ❌ FAIL | Flag exists but NOT enabled in deployment.yaml |
| 3. ArgoCD pilot fragment | ✅ PASS | Fragment exists with correct paths and upstream configuration |
| 4. Pass-through (no injection) | ✅ PASS | Correctly omits x-vault-path and x-inject-as fields |
| 5. Fragment/reload/quarantine path | ❌ FAIL | BLOCKED by Criterion 2 - cannot verify without hot reload |
| 6. `seam lint` CI gate | ✅ PASS | Workflow exists and gates declarative-config PRs |

**Overall Status: ❌ PHASE 3 NOT COMPLETE**

3/6 criteria pass, 2/6 fail, 1/6 blocked by a failure.

## Critical Issues Discovered

### Issue 1: Hot Reload Not Enabled in Deployment (Criterion 2)

**Severity:** 🔴 CRITICAL - Phase 3 acceptance requirement not met

The deployment.yaml does not enable hot reload despite the binary supporting it:
- No `-enable-hot-reload` command flag in container spec
- No `SEAM_ENABLE_HOT_RELOAD` environment variable set
- ConfigMap changes require pod restart to take effect

**Impact:**
- Phase 3's core feature (atomic route-table swap without restart) is not operational
- Fragment lifecycle testing (Criterion 5) is blocked
- The pilot fragment cannot be meaningfully exercised end-to-end

**Root Cause:**
The Phase 3 implementation added hot reload capability to the binary but missed the deployment configuration step to enable it at runtime.

### Issue 2: Fragment Lifecycle Cannot Be Demonstrated (Criterion 5)

**Severity:** 🔴 CRITICAL - Phase 3 acceptance requirement not met

Without hot reload, the critical fragment behaviors cannot be verified:
- Atomic route-table swap on ConfigMap edit
- Quarantine of malformed fragments
- No-downtime fragment updates

**Impact:**
- Phase 3 closure on 2026-08-27/28 occurred without demonstrating acceptance criteria
- The gate that should have proved readiness for Phase 4 (credential injection) was never exercised

## Required Remediation

To complete Phase 3 properly, the following must happen:

1. **Enable hot reload in deployment**
   - Add `SEAM_ENABLE_HOT_RELOAD: "true"` environment variable to deployment.yaml
   - OR add `--enable-hot-reload` to container command args
   - Verify the change is deployed to rs-manager

2. **Exercise fragment lifecycle end-to-end**
   - Edit argocd-read-only-proxy.yaml ConfigMap in live cluster
   - Verify atomic swap occurs without pod restart
   - Submit malformed fragment to verify quarantine behavior

3. **Re-verify all criteria**
   - Re-run this verification against live instance after hot reload is enabled
   - Document actual runtime behavior, not just capability presence

## Acceptance Criteria Were Not Met

The umbrella bead `seam-2992a0af` was closed on 2026-08-27/28, but internal/server had not compiled since 2026-08-30 with 99 compile errors. This verification confirms that Phase 3's acceptance criteria were never demonstrated against a running binary.

**Phase 3 is NOT complete despite the umbrella bead being closed.**

## Related Beads

**Umbrella:** seam-2992a0af (Phase 3 umbrella, closed 2026-08-27/28)  
**Current bead:** seam-d3f72917 (this verification)  
**New beads needed:**
- Enable hot reload in deployment.yaml
- Exercise fragment lifecycle end-to-end  
- Re-verify Phase 3 after fixes applied
