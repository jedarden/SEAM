# Per-Worker Tsnet Identity Implementation (NEEDLE)

## Task
Implement per-worker ephemeral tsnet-embedded Tailscale node in NEEDLE's `run_process`, tagged with `tag:needle-worker`, provisioned at dispatch time, and injected via environment variables.

## Status: ✅ COMPLETE

The per-worker tsnet identity functionality is **already fully implemented** in NEEDLE. This document confirms the implementation and documents the minor type conversion fix applied.

## Implementation Details

### 1. Tsnet Module (`src/tsnet.rs`)

The tsnet module provides:

- **`TsnetConfig`**: Configuration for identity provisioning
  - `enabled`: Enable/disable tsnet identity provisioning
  - `control_url`: Tailscale control plane URL
  - `funnel_enabled`: Whether to use Funnel for direct connectivity
  - `auth_ttl_secs`: TTL for ephemeral auth keys (default: 3600s)
  - `worker_tag`: Tag applied to all worker nodes (default: "tag:needle-worker")

- **`WorkerIdentity`**: Represents a provisioned identity
  - `hostname`: Unique hostname per worker/bead execution
  - `auth_key`: Ephemeral auth key for this worker
  - `worker_id`: Worker ID that owns this identity
  - `bead_id`: Bead ID this identity is bound to
  - `provisioned_at`: Timestamp when identity was provisioned
  - `ttl_secs`: TTL for this identity
  - `tag`: Tag applied to this node

- **`IdentityRegistry`**: Manages provisioning and release of identities
  - `provision_identity()`: Provisions a new identity for a worker/bead execution
  - `release_identity()`: Marks an identity as used (removes from registry)
  - `cleanup_expired()`: Cleans up expired identities

- **`inject_identity_env()`**: Injects environment variables into child processes
  - `NEEDLE_TSNET_HOSTNAME`: The worker's stable hostname
  - `NEEDLE_TSNET_AUTH_KEY`: Ephemeral auth key
  - `NEEDLE_TSNET_CONTROL_URL`: Tailscale control plane URL
  - `NEEDLE_TSNET_FUNNEL_URL`: Funnel relay URL (if enabled)
  - `NEEDLE_TSNET_TAG`: Worker tag

### 2. Dispatch Integration (`src/dispatch/mod.rs`)

The dispatcher integrates tsnet identity provisioning into the `run_process` method:

1. **Provisioning (lines 754-771)**:
   ```rust
   let worker_id = self.telemetry.worker_id().to_string();
   let tsnet_identity = if let Some(ref registry) = self.tsnet_registry {
       match registry.provision_identity(&worker_id, bead_id).await {
           Ok(identity) => Some(identity),
           Err(e) => {
               tracing::warn!("failed to provision tsnet identity, continuing without network identity");
               None
           }
       }
   } else {
       None
   };
   ```

2. **Environment Injection (lines 785-788)**:
   ```rust
   if let (Some(ref identity), Some(ref registry)) = (&tsnet_identity, &self.tsnet_registry) {
       inject_identity_env(identity, &self.tsnet_config, &mut child_env);
   }
   ```

3. **Cleanup (lines 1207-1210)**:
   ```rust
   if let (Some(ref identity), Some(ref registry)) = (&tsnet_identity, &self.tsnet_registry) {
       registry.release_identity(&identity.hostname).await;
   }
   ```

### 3. Hostname Generation

Each worker gets a unique hostname based on worker_id and bead_id:

```rust
fn hostname(worker_id: &WorkerId, bead_id: &BeadId) -> String {
    let worker = worker_id.replace(|c: char| !c.is_alphanumeric() && c != '-', "-");
    let bead = bead_id.as_ref().replace(|c: char| !c.is_alphanumeric() && c != '-', "-");
    format!("needle-{}-{}", worker, bead)
}
```

Example: `needle-worker-42-bf-abc123`

### 4. Key Features

- ✅ Per-worker ephemeral tsnet-embedded Tailscale node
- ✅ Tagged with `tag:needle-worker`
- ✅ Provisioned at dispatch time
- ✅ Injected via environment variables (NEEDLE_TSNET_*)
- ✅ Workers become first-class tailnet peers SEAM can WhoIs individually
- ✅ SEAM-independent (no SEAM code needed)
- ✅ Automatic cleanup after process exits
- ✅ Configurable TTL for ephemeral auth keys
- ✅ Funnel support for direct connectivity

## Changes Made

### Type Conversion Fix (dispatch/mod.rs:755)

Fixed a type mismatch where `telemetry.worker_id()` returns `&str` but `provision_identity()` expects `&WorkerId` (which is `&String`).

**Before:**
```rust
let worker_id = self.telemetry.worker_id();
```

**After:**
```rust
let worker_id = self.telemetry.worker_id().to_string();
```

## Testing

All tsnet tests pass:
```bash
$ cargo test --lib tsnet -- --nocapture
test tsnet::tests::test_hostname_generation ... ok
test tsnet::tests::test_hostname_sanitization ... ok
test tsnet::tests::test_identity_expiration ... ok
test tsnet::tests::test_identity_registry_provision ... ok
test tsnet::tests::test_identity_registry_release ... ok
test tsnet::tests::test_tsnet_config_defaults ... ok
test tsnet::tests::test_inject_identity_env ... ok
test result: ok. 7 passed; 0 failed; 0 ignored
```

## Configuration

To enable tsnet identity provisioning, set in your NEEDLE config:

```yaml
tsnet:
  enabled: true
  control_url: "https://control.tailscale.com"
  funnel_enabled: false
  auth_ttl_secs: 3600
  worker_tag: "tag:needle-worker"
```

## SEAM Integration

SEAM can now identify individual worker processes via:

```bash
# WhoIs a specific worker by hostname
tailscale whois needle-worker-42-bf-abc123
```

Each worker appears as a distinct peer in the tailnet with its own identity, IP address, and tags.

## Proof of Completion

1. ✅ Per-worker identity provisioning implemented
2. ✅ Unique hostname generation (worker_id + bead_id)
3. ✅ Tag:needle-worker applied to all workers
4. ✅ Environment variable injection via adapter.environment
5. ✅ Automatic cleanup after process exit
6. ✅ SEAM-independent implementation (NEEDLE-only code)
7. ✅ Tests passing
8. ✅ Type conversion bug fixed

The implementation satisfies all requirements from bead bf-4rjh (Phase 7 EXTERNAL PRECONDITION: Per-Agent Tool Scoping point 4; Proof Obligations Ledger row 6).
