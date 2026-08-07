# Tailscale Egress Patterns on rs-manager

This document establishes the pattern for Tailscale egress configurations on rs-manager, which enable connectivity from rs-manager pods to kubectl-proxy services on managed Rackspace Spot clusters.

## Overview

The Tailscale operator on rs-manager creates proxy pods that tunnel traffic from rs-manager pods to remote clusters' kubectl-proxy services over the tailnet. Each cluster gets a dedicated ClusterIP Service resource that acts as the egress endpoint.

## Active Configurations

As of 2026-08-07, there are three active Rackspace Spot clusters under rs-management:

### 1. iad-ci

```yaml
# File: kubectl-proxy-iad-ci-egress.yml
apiVersion: v1
kind: Service
metadata:
  name: kubectl-proxy-iad-ci-egress
  namespace: tailscale
  annotations:
    tailscale.com/tailnet-fqdn: kubectl-proxy-iad-ci.tail1b1987.ts.net
  labels:
    component: kubectl-proxy-egress
    target-cluster: iad-ci
spec:
  type: ClusterIP
  ports:
    - name: https
      port: 8001
      targetPort: 8001
      protocol: TCP
```

**Notes:**
- Direct connection to kubectl-proxy pod
- CI/CD cluster running Argo Workflows

### 2. iad-kalshi

```yaml
# File: kubectl-proxy-iad-kalshi-egress.yml
apiVersion: v1
kind: Service
metadata:
  name: kubectl-proxy-iad-kalshi-egress
  namespace: tailscale
  annotations:
    tailscale.com/tailnet-fqdn: kubectl-proxy-iad-kalshi.tail1b1987.ts.net
  labels:
    component: kubectl-proxy-egress
    target-cluster: iad-kalshi
spec:
  type: ClusterIP
  ports:
    - name: https
      port: 8001
      targetPort: 8001
      protocol: TCP
```

**Notes:**
- Direct connection to kubectl-proxy pod
- Hosts kalshi-weather workloads

### 3. iad-options

```yaml
# File: kubectl-proxy-iad-options-egress.yml
apiVersion: v1
kind: Service
metadata:
  name: kubectl-proxy-iad-options-egress
  namespace: tailscale
  annotations:
    tailscale.com/tailnet-fqdn: traefik-iad-options.tail1b1987.ts.net
  labels:
    component: kubectl-proxy-egress
    target-cluster: iad-options
spec:
  type: ClusterIP
  ports:
    - name: https
      port: 8001
      targetPort: 8001
      protocol: TCP
```

**Notes:**
- **Exception:** Routes through Traefik instead of direct kubectl-proxy
- iad-options exposes kubectl-proxy through Traefik's kubectl-tcp entrypoint
- Hosts options data pipeline workloads

## Naming Pattern

### Metadata Names
- Pattern: `kubectl-proxy-{target-cluster}-egress`
- Example: `kubectl-proxy-iad-ci-egress`
- Lowercase, hyphen-separated
- Describes both the target service and the purpose

### File Names
- Pattern: `{metadata.name}.yml`
- Example: `kubectl-proxy-iad-ci-egress.yml`
- Matches metadata.name exactly

## Label Structure

### Required Labels
```yaml
labels:
  component: kubectl-proxy-egress
  target-cluster: {cluster-name}
```

- `component`: Fixed value `kubectl-proxy-egress` for all egress services
- `target-cluster`: The cluster identifier (e.g., `iad-ci`, `iad-kalshi`)

## Annotation Pattern

```yaml
annotations:
  tailscale.com/tailnet-fqdn: {tailnet-hostname}
```

The `tailnet-fqdn` annotation tells the Tailscale operator which tailnet hostname to proxy. Two patterns:

1. **Direct kubectl-proxy** (most common):
   - Pattern: `kubectl-proxy-{cluster}.tail1b1987.ts.net`
   - Used by: iad-ci, iad-kalshi

2. **Via Traefik** (exception):
   - Pattern: `traefik-{cluster}.tail1b1987.ts.net`
   - Used by: iad-options
   - Reason: Traefik exposes kubectl-proxy via kubectl-tcp entrypoint

## Service Specification

```yaml
spec:
  type: ClusterIP
  ports:
    - name: https
      port: 8001
      targetPort: 8001
      protocol: TCP
```

All egress services use:
- **type:** `ClusterIP` (headless not needed)
- **port:** 8001 (kubectl-proxy standard port)
- **targetPort:** 8001
- **protocol:** TCP
- **name:** `https` (even though it's TCP)

## Namespace

All egress services are deployed in the `tailscale` namespace on rs-manager.

## Template for New Clusters

```yaml
---
# Tailscale egress to {cluster-name} kubectl-proxy (port 8001).
# The Tailscale operator creates a proxy pod that tunnels traffic from
# rs-manager pods to {cluster-name}'s kubectl-proxy over the tailnet.
apiVersion: v1
kind: Service
metadata:
  name: kubectl-proxy-{cluster-name}-egress
  namespace: tailscale
  annotations:
    tailscale.com/tailnet-fqdn: kubectl-proxy-{cluster-name}.tail1b1987.ts.net
  labels:
    component: kubectl-proxy-egress
    target-cluster: {cluster-name}
spec:
  type: ClusterIP
  ports:
    - name: https
      port: 8001
      targetPort: 8001
      protocol: TCP
```

Replace `{cluster-name}` with the target cluster identifier (e.g., `iad-new-cluster`).

## Decommissioned Clusters

### iad-native-ads (DECOMMISSIONED 2026-07-27)
- The Spot cloudspace is gone
- API endpoint no longer resolves
- Egress route file: `kubectl-proxy-iad-native-ads-egress.yml` (should be removed)
- See CLAUDE.md for deprecation details

## Related Documentation

- CLAUDE.md: Cluster access patterns and kubectl-proxy setup
- Tailscale operator documentation: https://github.com/tailscale/tailscale-operator
- Tailnet domain: `tail1b1987.ts.net`
