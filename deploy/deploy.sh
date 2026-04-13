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

# Platforms to build for
PLATFORMS="${PLATFORMS:-linux/amd64,linux/arm64}"

echo -e "${BLUE}Deploying get-iplayer-go to Docker Registry${NC}"
echo ""

# Ensure a buildx builder with multi-arch support is available
if ! docker buildx inspect multiarch-builder &>/dev/null; then
  echo -e "${GREEN}[setup] Creating multi-arch buildx builder...${NC}"
  docker buildx create --name multiarch-builder --driver docker-container --use
else
  docker buildx use multiarch-builder
fi

# Step 1: Build the image locally
echo -e "${GREEN}[1/2] Building Docker image for ${PLATFORMS}...${NC}"

BUILD_TIME=$(date -u +"%Y-%m-%dT%H:%M:%SZ")
VERSION="v$(date -u +"%Y.%m.%d")-$(git -C "${REPO_ROOT}" rev-parse --short HEAD)"

docker buildx build \
  -f "${SCRIPT_DIR}/Dockerfile" \
  --platform "${PLATFORMS}" \
  --build-arg BUILD_TIME="${BUILD_TIME}" \
  --build-arg VERSION="${VERSION}" \
  -t "${REGISTRY_IMAGE}" \
  --push \
  "${REPO_ROOT}"

echo ""
echo -e "${GREEN}Build and push complete!${NC}"
echo -e "Image available at: ${BLUE}${REGISTRY_IMAGE}${NC}"
