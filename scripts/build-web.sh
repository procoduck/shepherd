#!/usr/bin/env bash
# scripts/build-web.sh — canonical SPA build script.
# All build paths (Makefile, Dockerfiles, goreleaser) invoke this script.
# Exempt from this rule: pnpm dev (dev server, not a build).
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"

cd "${REPO_ROOT}/web"

echo "==> Installing dependencies..."
pnpm install --frozen-lockfile

echo "==> Building SPA..."
CI=true pnpm run build

echo "==> SPA build complete."
