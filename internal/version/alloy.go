// Package version exposes build-time version constants.
package version

// AlloySchemaVersion is the pinned Alloy schema version this build serves.
// Must stay in sync with ALLOY_VERSION in deploy/versions.env and the
// committed schema artifact in internal/schema/artifacts/.
// Updated alongside the artifact during: make schema-verify (Alloy-bump PRs).
const AlloySchemaVersion = "alloy-v1.18.1"
