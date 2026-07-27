#!/usr/bin/env bash
set -euo pipefail

# Capture differential corpus from kubectl-proxy endpoints
# One corpus per cluster with representative GETs

CORPUS_DIR="corpus/kubectl-proxies"
mkdir -p "$CORPUS_DIR"

# Array of cluster configurations: name server-url
declare -a CLUSTERS=(
    "apexalgo-iad|http://traefik-apexalgo-iad:8001"
    "ardenone-cluster|http://traefik-ardenone-cluster:8001"
    "ardenone-hub|http://traefik-ardenone-hub:8001"
    "ardenone-manager|http://traefik-ardenone-manager:8001"
    "rs-manager|http://traefik-rs-manager:8001"
    "ord-devimprint|http://kubectl-proxy-ord-devimprint:8001"
    "iad-kalshi|http://kubectl-proxy-iad-kalshi:8001"
    "iad-options|http://traefik-iad-options:8001"
)

for cluster_spec in "${CLUSTERS[@]}"; do
    IFS='|' read -r cluster_name server_url <<< "$cluster_spec"
    cluster_dir="$CORPUS_DIR/$cluster_name"
    mkdir -p "$cluster_dir"

    echo "Capturing corpus for $cluster_name ($server_url)"

    # Test connectivity first
    if ! kubectl --server="$server_url" get --raw='/healthz' &>/dev/null; then
        echo "  WARNING: Cannot reach $server_url - skipping cluster"
        continue
    fi

    # Capture representative resources
    # pods (sample from all namespaces)
    kubectl --server="$server_url" get pods --all-namespaces -o json > "$cluster_dir/pods.json" 2>/dev/null || echo "  Warning: Could not capture pods"

    # deployments
    kubectl --server="$server_url" get deployments --all-namespaces -o json > "$cluster_dir/deployments.json" 2>/dev/null || echo "  Warning: Could not capture deployments"

    # events (recent, limited)
    kubectl --server="$server_url" get events --all-namespaces -o json --chunk-size=500 > "$cluster_dir/events.json" 2>/dev/null || echo "  Warning: Could not capture events"

    # namespaces
    kubectl --server="$server_url" get namespaces -o json > "$cluster_dir/namespaces.json" 2>/dev/null || echo "  Warning: Could not capture namespaces"

    # nodes
    kubectl --server="$server_url" get nodes -o json > "$cluster_dir/nodes.json" 2>/dev/null || echo "  Warning: Could not capture nodes"

    # services
    kubectl --server="$server_url" get services --all-namespaces -o json > "$cluster_dir/services.json" 2>/dev/null || echo "  Warning: Could not capture services"

    # capture metadata
    echo "$server_url" > "$cluster_dir/_source.txt"
    date -u +"%Y-%m-%dT%H:%M:%SZ" > "$cluster_dir/_captured.txt"
    kubectl --server="$server_url" version --output=json > "$cluster_dir/_version.json" 2>/dev/null || true

    echo "  Captured $(ls -1 "$cluster_dir" | wc -l) artifacts"
done

echo "Corpus capture complete. Summary:"
ls -la "$CORPUS_DIR"
