#!/bin/bash
# Capture k8s corpus for a cluster endpoint
# Usage: ./capture-k8s-corpus.sh <cluster-name> <source-url>

set -e

CLUSTER_NAME="$1"
SOURCE_URL="$2"
CORPUS_DIR="corpus/kubectl-proxies/${CLUSTER_NAME}"
TIMESTAMP=$(date -u +"%Y-%m-%dT%H:%M:%SZ")

echo "Capturing corpus for ${CLUSTER_NAME} from ${SOURCE_URL}"

# Create corpus directory if it doesn't exist
mkdir -p "${CORPUS_DIR}"

# Write metadata files
echo "${TIMESTAMP}" > "${CORPUS_DIR}/_captured.txt"
echo "${SOURCE_URL}" > "${CORPUS_DIR}/_source.txt"

# Capture version info
echo "Capturing version info..."
curl -s "${SOURCE_URL}/version" -o "${CORPUS_DIR}/_version.json"

# Capture k8s resources
echo "Capturing namespaces..."
curl -s "${SOURCE_URL}/api/v1/namespaces" -o "${CORPUS_DIR}/namespaces.json"

echo "Capturing pods..."
curl -s "${SOURCE_URL}/api/v1/pods" -o "${CORPUS_DIR}/pods.json"

echo "Capturing nodes..."
curl -s "${SOURCE_URL}/api/v1/nodes" -o "${CORPUS_DIR}/nodes.json"

echo "Capturing services..."
curl -s "${SOURCE_URL}/api/v1/services" -o "${CORPUS_DIR}/services.json"

echo "Capturing deployments..."
# Deployments are in apps/v1, need to query all namespaces
curl -s "${SOURCE_URL}/apis/apps/v1/deployments" -o "${CORPUS_DIR}/deployments.json"

echo "Capturing events..."
curl -s "${SOURCE_URL}/api/v1/events" -o "${CORPUS_DIR}/events.json"

echo "Corpus capture complete for ${CLUSTER_NAME}"
echo "Files written to ${CORPUS_DIR}/"
ls -la "${CORPUS_DIR}/"
