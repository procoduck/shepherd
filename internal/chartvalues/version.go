package chartvalues

// PinnedChartVersion is the Grafana k8s-monitoring chart release this
// package's vendored schema (testdata/values.schema.json) and its remoteConfig
// wiring were verified against. Kept in lockstep with
// K8S_MONITORING_CHART_VERSION in deploy/versions.env by `make
// check-chartvalues-pin` (offline: agreement between this constant, the
// versions.env pin, and testdata/values.schema.meta.json — part of `make
// lint`) and `make chart-verify` (online: re-fetches the pinned release's
// values.schema.json from upstream and fails if it no longer matches the
// vendored copy byte-for-byte — G8).
//
// This is a MAJOR.MINOR.PATCH pin, not a floor like internal/gateway's
// MinBundleVersion: unlike the Gateway API's channel-stable CRDs, a Helm
// chart's values SHAPE is not a compatibility contract between releases — a
// field the schema declares today can be renamed or dropped in the next
// release with no deprecation window, so "newer is fine" cannot be assumed
// the way it can for a versioned API. Render targets exactly this version;
// bumping it means re-vendoring the schema and re-verifying every emitted key
// still exists (see doc.go and schema.go), not just editing this string.
const PinnedChartVersion = "4.4.0"
