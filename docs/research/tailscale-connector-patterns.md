# Tailscale Connector Patterns

This document describes the existing Tailscale networking patterns used across the fleet for cross-cluster connectivity.

## Overview

Tailscale is used to create private mesh connections between Kubernetes clusters, enabling:
- kubectl proxy access from management clusters to managed clusters
- Service-to-service communication across cluster boundaries
- Private ingress without LoadBalancers (critical for Rackspace Spot clusters)

## Pattern 1: Tailscale Egress Services (rs-manager)

**Location:** `declarative-config/k8s/rs-manager/tailscale/`

**Purpose:** Allow pods on rs-manager to reach kubectl-proxy services on remote clusters via the Tailscale mesh.

**Resource Type:** `Service` (not a Connector CRD)

### Naming Convention

```yaml
metadata:
  name: kubectl-proxy-<target-cluster>-egress
  namespace: tailscale
```

**Pattern:** `kubectl-proxy-{CLUSTER}-egress`

**Examples:**
- `kubectl-proxy-iad-ci-egress`
- `kubectl-proxy-iad-kalshi-egress`
- `kubectl-proxy-ord-devimprint-egress`
- `kubectl-proxy-apexalgo-iad-egress`
- `kubectl-proxy-iad-options-egress`
- `kubectl-proxy-iad-native-ads-egress` ⚠️ **DECOMMISSIONED**

### Labels

```yaml
labels:
  component: kubectl-proxy-egress
  target-cluster: <cluster-name>
```

**Purpose:** 
- `component: kubectl-proxy-egress` - Groups all egress services for discovery
- `target-cluster: <name>` - Identifies the destination cluster

### Annotation - The Key Mechanism

```yaml
annotations:
  tailscale.com/tailnet-fqdn: <hostname>.tail1b1987.ts.net
```

**What this does:**
- Tells the Tailscale operator to create a proxy pod
- The proxy pod tunnels traffic from rs-manager → remote cluster's Tailscale hostname
- The operator creates an ExternalName service pointing to the proxy

**Pattern:** `{service-name}.{cluster}.tail1b1987.ts.net`

**Examples:**
- `kubectl-proxy-iad-ci.tail1b1987.ts.net`
- `kubectl-proxy-iad-kalshi.tail1b1987.ts.net`
- `traefik-apexalgo-iad.tail1b1987.ts.net`

### Service Specification

```yaml
spec:
  type: ClusterIP
  ports:
    - name: https
      port: 8001
      targetPort: 8001
      protocol: TCP
```

**Standard:** All kubectl-proxy services expose port 8001 (the kubectl-proxy default).

### Complete Example

```yaml
---
# Tailscale egress to iad-ci kubectl-proxy (port 8001).
# The Tailscale operator creates a proxy pod that tunnels traffic from
# rs-manager pods to iad-ci's kubectl-proxy over the tailnet.
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

### How to Use

From a pod on rs-manager:

```bash
# Access kubectl-proxy on iad-ci via the Tailscale mesh
kubectl --server=http://kubectl-proxy-iad-ci-egress.tailscale.svc.cluster.local:8001 get pods
```

The Tailscale operator resolves:
1. `kubectl-proxy-iad-ci-egress.tailscale.svc.cluster.local` 
2. → ExternalName service (user-facing)
3. → `ts-kubectl-proxy-iad-ci-egress-*` (operator-managed ClusterIP)
4. → Proxy pod on tailnet
5. → `kubectl-proxy-iad-ci.tail1b1987.ts.net:8001`

### Active vs Decommissioned Clusters

| Service | Target Cluster | Status |
|---------|---------------|--------|
| `kubectl-proxy-iad-ci-egress` | iad-ci | ✅ Active |
| `kubectl-proxy-iad-kalshi-egress` | iad-kalshi | ✅ Active |
| `kubectl-proxy-ord-devimprint-egress` | ord-devimprint | ✅ Active |
| `kubectl-proxy-apexalgo-iad-egress` | apexalgo-iad | ✅ Active |
| `kubectl-proxy-iad-options-egress` | iad-options | ✅ Active |
| `kubectl-proxy-iad-native-ads-egress` | iad-native-ads | ⚠️ Decommissioned (2026-07-27) |

**Note:** The iad-native-ads egress still exists but points to a decommissioned cluster. Consider removing it.

---

## Pattern 2: Tailscale Connector CRDs (ardenone-cluster)

**Location:** `declarative-config/k8s/ardenone-cluster/seaweedfs/`

**Purpose:** Expose a service to the entire tailnet as a subnet router, allowing any cluster to reach it.

**Resource Type:** `Connector` (tailscale.com/v1alpha1)

### Example: SeaweedFS Connector

```yaml
---
# Tailscale Connector for SeaweedFS S3
# Exposes SeaweedFS S3 API to other clusters via Tailscale mesh
apiVersion: tailscale.com/v1alpha1
kind: Connector
metadata:
  name: seaweedfs-connector
  namespace: seaweedfs
spec:
  hostname: seaweedfs-ardenone
  tags:
    - tag:k8s
  subnetRouter:
    advertiseRoutes: []
---
# Tailscale Ingress to route traffic to SeaweedFS S3 service
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: seaweedfs-s3-tailscale
  namespace: seaweedfs
  annotations:
    tailscale.com/funnel: "false"
spec:
  ingressClassName: tailscale
  rules:
    - http:
        paths:
          - path: /
            pathType: Prefix
            backend:
              service:
                name: seaweedfs-s3
                port:
                  number: 8333
```

### Key Differences from Egress Services

| Aspect | Egress Service | Connector CRD |
|--------|---------------|---------------|
| **Resource Type** | Service | Connector + Ingress |
| **Direction** | Outbound from local cluster | Inbound to local cluster |
| **Use Case** | Reach remote cluster services | Expose local services to mesh |
| **Annotation** | `tailscale.com/tailnet-fqdn` | `tailscale.com/funnel` on Ingress |
| **Cluster** | rs-manager (management) | ardenone-cluster (workload) |

### Naming Convention

```yaml
metadata:
  name: <service>-connector
  namespace: <service-namespace>

spec:
  hostname: <service>-<cluster>
```

**Pattern:** `{service}-connector` → hostname `{service}-{cluster}`

### Tags

```yaml
spec:
  tags:
    - tag:k8s
```

Tags are used for ACL policy in the Tailscale admin console. `tag:k8s` is the standard tag for Kubernetes resources.

### Ingress Configuration

```yaml
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  annotations:
    tailscale.com/funnel: "false"  # Do not expose to public internet
spec:
  ingressClassName: tailscale
```

**Important:** Always set `tailscale.com/funnel: "false"` for private-only services. Funnel exposes services to the public internet via Tailscale Funnel.

---

## Pattern Selection Guide

### Use Egress Services (Pattern 1) when:
- You need to reach **remote** cluster services from a management cluster
- The use case is kubectl proxy, monitoring, or cross-cluster API access
- You're on rs-manager or another management cluster
- Port is fixed (e.g., 8001 for kubectl-proxy)

### Use Connector CRDs (Pattern 2) when:
- You need to expose **local** services to the entire tailnet
- Multiple clusters need to reach your service
- You're running a workload that should be accessible fleet-wide
- You need subnet router functionality

---

## Replication Checklist

### Creating a New Egress Service (rs-manager pattern)

1. **Prerequisites:**
   - [ ] Target cluster has a Tailscale hostname registered
   - [ ] Target service exists and is accessible on the tailnet
   - [ ] You know the port number (usually 8001 for kubectl-proxy)

2. **Create the service:**
   ```yaml
   apiVersion: v1
   kind: Service
   metadata:
     name: kubectl-proxy-<CLUSTER>-egress
     namespace: tailscale
     annotations:
       tailscale.com/tailnet-fqdn: <service>.tail1b1987.ts.net
     labels:
       component: kubectl-proxy-egress
       target-cluster: <CLUSTER>
   spec:
     type: ClusterIP
     ports:
       - name: https
         port: <PORT>
         targetPort: <PORT>
         protocol: TCP
   ```

3. **Verify:**
   ```bash
   # Wait for Tailscale operator to reconcile (should be immediate)
   kubectl --kubeconfig=/home/coding/.kube/rs-manager.kubeconfig get svc -n tailscale | grep <CLUSTER>-egress
   
   # You should see both:
   # - kubectl-proxy-<CLUSTER>-egress (ExternalName)
   # - ts-kubectl-proxy-<CLUSTER>-egress-* (ClusterIP, operator-managed)
   ```

### Creating a New Connector (ardenone-cluster pattern)

1. **Prerequisites:**
   - [ ] Service to expose exists in a namespace
   - [ ] Tailscale operator is installed on the cluster
   - [ ] You have decided on a hostname

2. **Create the Connector:**
   ```yaml
   apiVersion: tailscale.com/v1alpha1
   kind: Connector
   metadata:
     name: <service>-connector
     namespace: <namespace>
   spec:
     hostname: <service>-<cluster>
     tags:
       - tag:k8s
     subnetRouter:
       advertiseRoutes: []
   ```

3. **Create the Ingress:**
   ```yaml
   apiVersion: networking.k8s.io/v1
   kind: Ingress
   metadata:
     name: <service>-tailscale
     namespace: <namespace>
     annotations:
       tailscale.com/funnel: "false"
   spec:
     ingressClassName: tailscale
     rules:
       - http:
           paths:
             - path: /
               pathType: Prefix
               backend:
                 service:
                   name: <service-name>
                   port:
                     number: <port>
   ```

4. **Verify:**
   ```bash
   # Check connector status
   kubectl get connector <service>-connector -n <namespace>
   
   # Check ingress
   kubectl get ingress <service>-tailscale -n <namespace>
   
   # Test from another cluster on the tailnet
   curl http://<service>-<cluster>.tail1b1987.ts.net:<port>
   ```

---

## References

- **Tailscale Kubernetes Operator:** https://github.com/tailscale/tailscale-operator
- **Tailscale Connector CRD:** https://tailscale.com/kb/1236/k8s-operator-resources/
- **rs-manager cluster docs:** `/home/coding/declarative-config/k8s/rs-manager/CLAUDE.md`
- **ardenone-cluster connector:** `declarative-config/k8s/ardenone-cluster/seaweedfs/seaweedfs-tailscale-connector.yml`

---

## Summary

- **rs-manager** uses **Egress Services** (Pattern 1) to reach remote clusters
- **ardenone-cluster** uses **Connector CRDs** (Pattern 2) to expose SeaweedFS fleet-wide
- The two patterns serve opposite directions: outbound vs inbound
- All configurations use the shared tailnet domain: `tail1b1987.ts.net`
- Always verify target cluster/service status before creating connectors
