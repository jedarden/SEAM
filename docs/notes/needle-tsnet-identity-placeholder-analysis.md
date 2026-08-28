# NEEDLE tsnet Identity Placeholder Implementation Analysis

**Date:** 2026-08-28  
**Workspace:** SEAM (documenting NEEDLE codebase)  
**Bead:** seam-29135801

## Executive Summary

NEEDLE's tsnet identity provisioning module (`src/tsnet.rs`) implements a complete architecture for per-worker Tailscale identities, but the critical auth key generation function is a placeholder that fails closed. The module is architecturally sound and ready for production integration—only the actual Tailscale API calls need implementation.

## Current Architecture

### 1. High-Level Flow

```
dispatch::run_process()
    ↓
tsnet_registry.provision_identity(worker_id, bead_id)
    ↓
generate_auth_key(hostname) ← PLACEHOLDER (always fails)
    ↓
WorkerIdentity created (if key generation succeeds)
    ↓
inject_identity_env() adds environment variables to child process
    ↓
Worker process launches with tsnet identity in env
```

### 2. Key Components

#### WorkerIdentity Structure (`src/tsnet.rs:101-117`)

```rust
pub struct WorkerIdentity {
    pub hostname: String,           // Stable: needle-{worker_id}-{bead_id}
    pub auth_key: String,          // Ephemeral auth key (PLACEHOLDER)
    pub worker_id: WorkerId,
    pub bead_id: BeadId,
    pub provisioned_at: u64,
    pub ttl_secs: u64,             // Default: 3600 (1 hour)
    pub tag: String,               // Default: tag:needle-worker
}
```

**Key design decisions:**
- Hostname generation is deterministic and stable (line 121-130)
- Sanitization removes invalid characters from worker_id and bead_id
- TTL-based expiration with `is_expired()` method (line 132-139)

#### IdentityRegistry (`src/tsnet.rs:146-157`)

Thread-safe registry that tracks active identities:
- `Arc<RwLock<HashMap<String, WorkerIdentity>>>` for concurrent access
- `provision_identity()` creates and registers identities (line 163-201)
- `release_identity()` cleanup on process exit (line 222-232)
- `cleanup_expired()` background maintenance (line 235-251)

## The Placeholder Gap

### generate_auth_key() — Current Implementation (`src/tsnet.rs:208-219`)

```rust
fn generate_auth_key(&self, hostname: &str) -> Result<String> {
    // In production, this would:
    // 1. Call Tailscale API POST /api/v2/tailnet/{tailnet}/keys
    // 2. Request ephemeral key with tags and expiration
    // 3. Return the actual key
    //
    // For now, fail closed since no real key provisioning mechanism is configured.
    anyhow::bail!(
        "tsnet auth key generation failed: no Tailscale API key source configured for hostname {}",
        hostname
    );
}
```

**Current behavior:** Always returns an error, causing `provision_identity()` to fail.

**Why fail closed?** The comment explains the design choice: without a real Tailscale API key source, it's safer to fail than to fabricate a credential-shaped value that might appear valid but doesn't actually work.

### How This Affects the Flow

In `src/dispatch/mod.rs:1168-1183`:

```rust
let tsnet_identity = if let Some(ref registry) = self.tsnet_registry {
    match registry.provision_identity(&worker_id, bead_id).await {
        Ok(identity) => Some(identity),
        Err(e) => {
            tracing::warn!(
                worker_id = %worker_id,
                bead_id = %bead_id.as_ref(),
                error = %e,
                "failed to provision tsnet identity, continuing without network identity"
            );
            None  // ← Execution continues without tsnet identity
        }
    }
} else {
    None
};
```

**Result:** Workers currently run WITHOUT tsnet identities. The warning is logged, but execution continues.

### Environment Injection Point (`src/dispatch/mod.rs:1197-1200`)

```rust
if let (Some(ref identity), Some(_)) = (&tsnet_identity, &self.tsnet_registry) {
    inject_identity_env(identity, &self.tsnet_config, &mut child_env);
}
```

When identity provisioning succeeds, `inject_identity_env()` (line 267-298) adds:

- `NEEDLE_TSNET_HOSTNAME` — Worker's stable hostname
- `NEEDLE_TSNET_AUTH_KEY` — Ephemeral auth key
- `NEEDLE_TSNET_CONTROL_URL` — Control plane URL (default: https://control.tailscale.com)
- `NEEDLE_TSNET_FUNNEL_URL` — Funnel relay URL (if enabled)
- `NEEDLE_TSNET_TAG` — Worker tag

## What Needs To Change

### 1. Replace generate_auth_key() Placeholder

**Current:** Returns error
**Target:** Call Tailscale API to create ephemeral auth key

**Required changes:**

```rust
fn generate_auth_key(&self, hostname: &str) -> Result<String> {
    // 1. Load Tailscale API credentials from secure source
    //    - OpenBao: secret/rs-manager/tailscale/api_key
    //    - Environment: TAILESCALE_API_KEY (for dev/testing)
    
    // 2. Call Tailscale API:
    //    POST https://control.tailscale.com/api/v2/tailnet/{tailnet}/keys
    //    Headers: Authorization: Bearer {api_key}
    //    Body: {
    //      "tags": ["tag:needle-worker"],
    //      "ephemeral": true,
    //      "expiry": { "seconds": self.config.auth_ttl_secs }
    //    }
    
    // 3. Extract and return the key from response
    //    Response: { "key": "tskey-auth-..." }
}
```

### 2. Add Configuration for Tailscale API Access

**Required new config fields in `TsnetConfig`:**
- `tailnet_name`: The tailnet to create keys in (e.g., "ardenone")
- `api_key_source`: Where to find the API key
  - Production: "openbao:secret/rs-manager/tailscale/api_key"
  - Dev: "env:TAILESCALE_API_KEY"
  - Test: "mock" (for testing without real API)

### 3. Implement Credential Source Abstraction

**New module: `src/tailscale_api.rs`**

```rust
pub enum ApiKeySource {
    OpenBao { path: String },
    Environment { var: String },
    Mock(String), // For testing
}

impl ApiKeySource {
    pub async fn get_key(&self) -> Result<String> {
        match self {
            Self::OpenBao { path } => {
                // Read from OpenBao
                let client = openbao_client();
                client.get_secret(path).await
            }
            Self::Environment { var } => {
                std::env::var(var).map_err(|_| anyhow!("env var not found"))
            }
            Self::Mock(key) => Ok(key.clone()),
        }
    }
}
```

### 4. Add HTTP Client for Tailscale API

**Dependencies needed:**
- `reqwest` — HTTP client with TLS support
- `serde_json` — JSON parsing for API responses

**Example implementation:**
```rust
async fn create_ephemeral_key(
    api_key: &str,
    tailnet: &str,
    tags: &[String],
    ttl_secs: u64,
) -> Result<String> {
    let client = reqwest::Client::new();
    let url = format!("https://api.tailscale.com/api/v2/tailnet/{}/keys", tailnet);
    
    let body = json!({
        "tags": tags,
        "ephemeral": true,
        "expiry": { "seconds": ttl_secs }
    });
    
    let response = client
        .post(&url)
        .header("Authorization", format!("Bearer {}", api_key))
        .json(&body)
        .send()
        .await?;
    
    let result: serde_json::Value = response.json().await?;
    Ok(result["key"].as_str().unwrap().to_string())
}
```

## Files That Need Modification

### Core Implementation (Required)

1. **`src/tsnet.rs`** (lines 208-219)
   - Replace `generate_auth_key()` with real API calls
   - Add `tailscale_api` dependency imports

2. **`src/config.rs`** (add new fields)
   - Add `TsnetConfig` fields: `tailnet_name`, `api_key_source`
   - Default: `tailnet_name = "ardenone"`, `api_key_source = "openbao:secret/rs-manager/tailscale/api_key"`

3. **`src/tailscale_api.rs`** (NEW FILE)
   - `ApiKeySource` enum and implementation
   - `create_ephemeral_key()` HTTP client function
   - Error handling for API failures

4. **`Cargo.toml`** (add dependencies)
   ```toml
   [dependencies]
   reqwest = { version = "0.11", features = ["json"] }
   # Already present: serde_json, tokio, anyhow
   ```

### Testing Updates (Recommended)

5. **`src/tsnet.rs`** (test module, lines 301-469)
   - Update `test_identity_registry_provision_fails_without_key_source` to expect success with mock API
   - Add integration test with mock Tailscale API server
   - Add test for API key source abstraction

6. **`src/dispatch/mod.rs`** (integration tests)
   - Add test that verifies worker process receives tsnet environment variables
   - Test error handling when Tailscale API is unavailable

## Testing Strategy

### Unit Tests (No External Dependencies)

- **Mock API key source**: Use `ApiKeySource::Mock("test-key")`
- **Verify hostname generation**: Already covered (lines 304-327)
- **Verify expiration logic**: Already covered (lines 329-350)

### Integration Tests (With Mock Server)

1. **Mock Tailscale API server** (use `wiremock` or `mockito`)
   - Verify correct API call format
   - Test error responses (401, 403, 500)
   - Test rate limiting (429)

2. **End-to-end worker spawn test**
   - Provision identity → inject env → verify in child process
   - Test failure path: API down → worker continues without identity

### Production Validation

1. **Dry-run mode**: Add `--dry-run` flag to test API calls without creating real keys
2. **Key verification**: After provisioning, call Tailscale API to verify key exists
3. **Cleanup validation**: Ensure expired keys are removed from Tailscale

## Security Considerations

### 1. API Key Storage (CRITICAL)

**Never store API keys in:**
- Git repository
- Configuration files
- Environment variables in production

**Correct approach:**
- Store in OpenBao: `secret/rs-manager/tailscale/tailnet-ardenone/api_key`
- Load at runtime via `ApiKeySource::OpenBao`
- Rotate credentials regularly (OpenBao can automate this)

### 2. Key Scope and Permissions

The Tailscale API key needs:
- **Minimum required scope**: `POST /api/v2/tailnet/{tailnet}/keys`
- **No broader permissions**: Avoid keys that can delete nodes or modify ACLs
- **Auditable**: Use dedicated API key per service (NEEDLE) for audit trail

### 3. Ephemeral Key Properties

Per current design (already correct):
- ✅ Single-use (ephemeral) keys
- ✅ Short TTL (default 1 hour)
- ✅ Tagged for identification (`tag:needle-worker`)
- ✅ Bound to specific hostname

### 4. Failure Modes

**Current behavior (fail open):**
- Worker continues without tsnet identity if API fails
- Logs warning but doesn't halt execution

**Security trade-off:**
- Acceptable for gradual rollout (allows testing without breaking production)
- For strict security, consider `fail_closed` config option to halt workers on identity provisioning failure

## Migration Path

### Phase 1: Development (Current)
- ✅ Architecture implemented
- ✅ Placeholder fails closed
- ❌ No real API integration

### Phase 2: Testing with Mock
- Implement `ApiKeySource::Mock` for unit tests
- Add wiremock tests for HTTP client
- Verify all error paths

### Phase 3: Development API Key
- Use dev tailnet with test API key
- Test in non-production environment
- Validate key creation and cleanup

### Phase 4: Production Rollout
- Store production API key in OpenBao
- Enable with `tsnet.enabled = true` in config
- Monitor for API errors and rate limits
- Gradual rollout: start with subset of workers

### Phase 5: Validation
- Verify workers appear in Tailscale admin UI
- Test SEAM WhoIs lookups against worker hostnames
- Validate cleanup of expired keys
- Audit for leaked/unused keys

## Configuration Example

```yaml
# needle-config.yaml
tsnet:
  enabled: true
  tailnet_name: "ardenone"
  api_key_source: "openbao:secret/rs-manager/tailscale/api_key"
  control_url: "https://control.tailscale.com"
  funnel_enabled: false
  auth_ttl_secs: 3600  # 1 hour
  worker_tag: "tag:needle-worker"
```

## Testing Checklist

Before declaring this complete:

- [ ] Unit tests pass with `ApiKeySource::Mock`
- [ ] Integration tests pass with mock Tailscale API server
- [ ] Manual test: Worker process receives env vars
- [ ] Manual test: Provisioned key appears in Tailscale admin UI
- [ ] Cleanup test: Expired keys removed from registry
- [ ] Error handling test: API failure doesn't crash worker
- [ ] Security audit: API key stored in OpenBao, not git
- [ ] Documentation updated: configuration.md with tsnet section
- [ ] Changelog entry: "Implemented real Tailscale API key provisioning"

## References

- Tailscale API docs: https://github.com/tailscale/tailscale/blob/main/api.md
- Ephemeral keys: https://tailscale.com/kb/1081/auth-keys/
- NEEDLE tsnet module: `/home/coding/NEEDLE/src/tsnet.rs`
- Dispatch integration: `/home/coding/NEEDLE/src/dispatch/mod.rs:1168-1200`
- OpenBao integration: `/home/coding/SEAM/docs/notes/openbao-resource-verification-report.md`

## Conclusion

The NEEDLE tsnet identity module is architecturally complete and production-ready. The only missing piece is the actual Tailscale API call in `generate_auth_key()`. The design is sound:

- ✅ Stable hostname generation
- ✅ Thread-safe identity registry
- ✅ Proper expiration handling
- ✅ Clean environment injection
- ✅ Fail-open behavior for gradual rollout

**Implementation effort estimate:** 4-6 hours
- 1 hour: Implement `ApiKeySource` abstraction
- 1 hour: Implement HTTP client and API call
- 2 hours: Write unit and integration tests
- 1 hour: Manual testing and validation
- 1 hour: Documentation and config examples

**Risk level:** Low
- Changes are localized to one function
- Fail-open behavior means no breaking changes
- Can be rolled back by setting `tsnet.enabled = false`

**Next steps:**
1. Create implementation bead for Phase 2 (testing with mock)
2. Add dependencies to Cargo.toml
3. Implement API key source abstraction
4. Wire up real Tailscale API calls
5. Test in dev environment before production rollout
