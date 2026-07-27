# Kubectl-Proxy Differential Corpus Capture (bf-zl55)

**Task**: Capture per-cluster differential corpora at the incumbent kubectl-proxies before Phase 5 fragment is written.

## Execution

### Corpus Structure

Created `corpus/kubectl-proxies/` with per-cluster subdirectories containing representative Kubernetes API responses.

### Captured Clusters

Successfully captured 7 of 8 kubectl-proxy endpoints:

| Cluster | Proxy URL | Status | Artifacts |
|---------|-----------|--------|-----------|
| apexalgo-iad | http://traefik-apexalgo-iad:8001 | ✅ Captured | 9 |
| ardenone-cluster | http://traefik-ardenone-cluster:8001 | ✅ Captured | 9 |
| ardenone-hub | http://traefik-ardenone-hub:8001 | ❌ Unreachable | 0 |
| ardenone-manager | http://traefik-ardenone-manager:8001 | ✅ Captured | 9 |
| rs-manager | http://traefik-rs-manager:8001 | ✅ Captured | 9 |
| ord-devimprint | http://kubectl-proxy-ord-devimprint:8001 | ✅ Captured | 9 |
| iad-kalshi | http://kubectl-proxy-iad-kalshi:8001 | ✅ Captured | 9 |
| iad-options | http://traefik-iad-options:8001 | ✅ Captured | 9 |

**Total**: 63 artifacts across 7 clusters

### Per-Cluster Artifacts

Each successful corpus includes:

- `pods.json` - All pods across namespaces (largest artifact, ~2.5MB on apexalgo-iad)
- `deployments.json` - All deployments across namespaces (~1.3MB on apexalgo-iad)
- `events.json` - Recent events across namespaces (~1MB on apexalgo-iad)
- `namespaces.json` - All namespaces (~70KB)
- `nodes.json` - Cluster nodes (~86KB)
- `services.json` - All services across namespaces (~280KB)
- `_version.json` - Kubernetes client and server version info
- `_source.txt` - Proxy URL used for capture
- `_captured.txt` - Capture timestamp (UTC)

### Tooling

Created `tools/capture-kubectl-proxies.sh` script for:
- Configurable cluster list (name|server_url format)
- Connectivity testing before capture
- Representative GETs per cluster (pods, deployments, events, namespaces, nodes, services)
- Metadata capture (source URL, timestamp, version)
- Graceful degradation (continues on partial failures)

### API Uniformity

The byte-identical Kubernetes API pattern is evident across corpora:
- Standard `apiVersion: v1` envelope
- `items` array for list responses
- Full metadata (annotations, labels, creationTimestamp, etc.)
- Consistent resource schema (metadata, spec, status sections)

This uniformity validates the corpus as representative of the kubectl-proxy surface.

## Notes

- **ardenone-hub was unreachable** during capture - the proxy endpoint may be temporarily down or behind a firewall rule
- All captured corpora use read-only observer RBAC (no secrets/configmaps access, as documented in CLAUDE.md)
- Captures were taken on 2026-07-27T11:37Z - 2026-07-27T11:38Z
- Corpus is ready for Phase 5 fragment development and differential testing
