#!/usr/bin/env bash
# tools/alloy-schema-gen/run.sh
# Generates schema/alloy-v<X>.json by cloning grafana/alloy at the pinned
# tag and running extract.go inside the checkout.
#
# Usage: make schema
# Prerequisites: git, go, jq, network access to ALLOY_REPO.
# CI: override ALLOY_REPO with your organisation's mirror if applicable.
# This script MUST NOT run at application build time — app builds are hermetic.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"

# shellcheck source=../../deploy/versions.env
source "${REPO_ROOT}/deploy/versions.env"

ALLOY_VERSION="${ALLOY_VERSION:?ALLOY_VERSION must be set in deploy/versions.env}"
ALLOY_REPO="${ALLOY_REPO:-https://github.com/grafana/alloy}"
OUTPUT="${REPO_ROOT}/internal/schema/artifacts/alloy-${ALLOY_VERSION}.json"

echo "==> Generating schema for alloy ${ALLOY_VERSION}"
echo "    Source: ${ALLOY_REPO}"
echo "    Output: ${OUTPUT}"

SRC=$(mktemp -d)
trap 'rm -rf "${SRC}"' EXIT

echo "==> Cloning grafana/alloy@${ALLOY_VERSION}..."
git clone --depth 1 --branch "${ALLOY_VERSION}" "${ALLOY_REPO}" "${SRC}"

echo "==> Injecting extractor..."
mkdir -p "${SRC}/cmd/shepherd-schema-dump"
cp "${SCRIPT_DIR}/extract.go" "${SRC}/cmd/shepherd-schema-dump/main.go"

echo "==> Running extractor..."
mkdir -p "${REPO_ROOT}/internal/schema/artifacts"
( cd "${SRC}" && go run ./cmd/shepherd-schema-dump ) | jq -S . > "${OUTPUT}"

echo "==> Done: ${OUTPUT}"
echo "    components_total: $(jq '._meta.components_total' "${OUTPUT}")"

OVERLAY="${REPO_ROOT}/internal/schema/artifacts/overlay.json"
echo "==> Reconciling overlay.json against the freshly generated artifact..."
go run "${SCRIPT_DIR}/reconcile.go" "${OUTPUT}" "${OVERLAY}"
echo "==> Review any entries above marked needs_review, then commit."
