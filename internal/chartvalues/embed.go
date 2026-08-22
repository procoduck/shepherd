package chartvalues

import "embed"

// Embedded holds the vendored k8s-monitoring values.schema.json (fetched
// verbatim from the PinnedChartVersion release, never hand-written — see
// version.go) and its provenance metadata. Kept in a separate file from
// schema.go, mirroring internal/schema/embed.go's own split, so schema.go's
// logic stays testable against a substitute fs.FS if that is ever useful.
//
//go:embed testdata/values.schema.json testdata/values.schema.meta.json
var Embedded embed.FS
