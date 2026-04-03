#!/bin/bash
set -e

# Colors for output
GREEN='\033[0;32m'
BLUE='\033[0;34m'
RED='\033[0;31m'
NC='\033[0m' # No Color

# Resolve paths (script lives in deploy/, repo root is one level up)
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"

# Load .env from repo root if present
if [ -f "${REPO_ROOT}/.env" ]; then
  # shellcheck source=/dev/null
  source "${REPO_ROOT}/.env"
fi

# Configuration — override REGISTRY_IMAGE in your environment or .env
REGISTRY_IMAGE="${REGISTRY_IMAGE:-get-iplayer-go:latest}"

echo -e "${BLUE}Deploying get-iplayer-go to Docker Registry${NC}"
echo ""

# Step 1: Build the image locally
echo -e "${GREEN}[1/2] Building Docker image locally...${NC}"

BUILD_TIME=$(date -u +"%Y-%m-%dT%H:%M:%SZ")
VERSION="v$(date -u +"%Y.%m.%d")"

docker build \
  -f "${SCRIPT_DIR}/Dockerfile" \
  --build-arg BUILD_TIME="${BUILD_TIME}" \
  --build-arg VERSION="${VERSION}" \
  -t "${REGISTRY_IMAGE}" \
  "${REPO_ROOT}"

# Step 2: Push the image to the registry
echo -e "${GREEN}[2/2] Pushing Docker image to registry...${NC}"
docker push "${REGISTRY_IMAGE}"

echo ""
echo -e "${GREEN}Build and push complete!${NC}"
echo -e "Image available at: ${BLUE}${REGISTRY_IMAGE}${NC}"
