package schema

import (
	"embed"
)

// Embedded contains the schema directory at build time.
// This embed is in a separate file to keep registry.go testable with a custom FS.
//
//go:embed artifacts
var Embedded embed.FS
