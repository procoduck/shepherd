#!/usr/bin/env bash
# tools/alloy-schema-gen/run.sh
# Generates schema/alloy-v<X>.json by cloning grafana/alloy at the pinned
# tag and running extract.go inside the checkout.
#
# Usage: make schema
#        make schema-verify                     (regenerate elsewhere and diff)
#        SCHEMA_OUT_DIR=/tmp/x SKIP_RECONCILE=1 ./tools/alloy-schema-gen/run.sh
#
# Environment:
#   SCHEMA_OUT_DIR   directory to write the artifact into
#                    (default: internal/schema/artifacts)
#   SKIP_RECONCILE   when non-empty, do not touch overlay.json
#   ALLOY_SRC        reuse an existing grafana/alloy checkout instead of cloning
#
# Prerequisites: git, go, jq, docker, network access to ALLOY_REPO. The
# extractor always runs in a linux container (see below) so the artifact is
# identical regardless of the host OS.
# CI: override ALLOY_REPO with your organisation's mirror if applicable.
# This script MUST NOT run at application build time — app builds are hermetic.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"

# shellcheck source=../../deploy/versions.env
source "${REPO_ROOT}/deploy/versions.env"

# `source` does not export, and run.sh is also invoked directly (not only via
# make, whose bare `export` would otherwise be the only reason this works).
# The injected extractor reads ALLOY_VERSION from its own environment.
export ALLOY_VERSION="${ALLOY_VERSION:?ALLOY_VERSION must be set in deploy/versions.env}"
ALLOY_REPO="${ALLOY_REPO:-https://github.com/grafana/alloy}"
OUT_DIR="${SCHEMA_OUT_DIR:-${REPO_ROOT}/internal/schema/artifacts}"
OUTPUT="${OUT_DIR}/alloy-${ALLOY_VERSION}.json"

echo "==> Generating schema for alloy ${ALLOY_VERSION}"
echo "    Source: ${ALLOY_REPO}"
echo "    Output: ${OUTPUT}"

if [[ -n "${ALLOY_SRC:-}" ]]; then
  SRC="${ALLOY_SRC}"
  echo "==> Reusing checkout at ${SRC}"
else
  SRC=$(mktemp -d)
  trap 'rm -rf "${SRC}"' EXIT
  echo "==> Cloning grafana/alloy@${ALLOY_VERSION}..."
  git clone --depth 1 --branch "${ALLOY_VERSION}" "${ALLOY_REPO}" "${SRC}"
fi

echo "==> Injecting extractor..."
mkdir -p "${SRC}/cmd/shepherd-schema-dump"
# extract.go carries "//go:build ignore" so this repo's own `go build ./...` skips it.
# That constraint must be stripped on the way in, or the injected package has no
# buildable files and `go run` fails with "build constraints exclude all Go files".
sed '/^\/\/go:build ignore$/d' "${SCRIPT_DIR}/extract.go" > "${SRC}/cmd/shepherd-schema-dump/main.go"
# portmodel.go is the port/wire model. It has no alloy imports, so it compiles
# (and is unit-tested) inside THIS module as part of package main next to
# reconcile.go, and is copied in verbatim here as a second package-main file.
cp "${SCRIPT_DIR}/portmodel.go" "${SRC}/cmd/shepherd-schema-dump/portmodel.go"

echo "==> Running extractor (linux container)..."
# The extractor executes Alloy's SetToDefault via reflection, so the artifact
# is GOOS-dependent: a darwin run misses the linux platform defaults
# (prometheus.exporter.unix's /sys and mount regexes, pyroscope's symb-cache,
# …) that the linux fleet actually gets. First completed CI schema-verify run
# caught exactly that drift in a mac-generated artifact. Pinning generation to
# a linux container makes local and CI output identical by construction.
# The host module cache is mounted so nothing re-downloads; the container runs
# as the invoking user so cached files stay owned by them (GOCACHE/HOME point
# into /tmp because that user has no home inside the image).
GO_CONTAINER_IMAGE="${GO_IMAGE:-golang:1.26}"
mkdir -p "${OUT_DIR}"
# Pre-create the module cache dir: if it does not exist when docker mounts it
# (e.g. a fresh CI runner on a cache miss), docker creates the mount point as
# root and the uid-mapped container user cannot write into it.
GOMODCACHE_DIR="$(go env GOMODCACHE)"
mkdir -p "${GOMODCACHE_DIR}"
docker run --rm \
  -u "$(id -u):$(id -g)" \
  -e HOME=/tmp -e GOCACHE=/tmp/go-build -e GOGC -e ALLOY_VERSION \
  -v "${SRC}":/src -v "${GOMODCACHE_DIR}":/go/pkg/mod \
  -w /src "${GO_CONTAINER_IMAGE}" \
  go run ./cmd/shepherd-schema-dump | jq -S . > "${OUTPUT}"

echo "==> Done: ${OUTPUT}"
echo "    components_total: $(jq '._meta.components_total' "${OUTPUT}")"

if [[ -n "${SKIP_RECONCILE:-}" ]]; then
  echo "==> SKIP_RECONCILE set — overlay.json left untouched."
  exit 0
fi

OVERLAY="${REPO_ROOT}/internal/schema/artifacts/overlay.json"
echo "==> Reconciling overlay.json against the freshly generated artifact..."
go run "${SCRIPT_DIR}/reconcile.go" "${OUTPUT}" "${OVERLAY}"
echo "==> Review any entries above marked needs_review, then commit."
