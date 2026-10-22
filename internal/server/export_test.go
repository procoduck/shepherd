package server

import "shepherd/internal/schema"

// Test-only exports: the specs in server_test.go (package server_test) must
// assert against the production route tree and metrics mux, not fixtures.
var (
	NewRouter     = newRouter
	NewMetricsMux = newMetricsMux
)

// EmbeddedSchema is the registry New loads, for specs that build the route
// tree without New.
func EmbeddedSchema() *schema.Registry {
	reg, err := loadSchema()
	if err != nil {
		panic(err)
	}
	return reg
}

// SetLoadSchema replaces New's schema loader and returns a func restoring it.
func SetLoadSchema(f func() (*schema.Registry, error)) (restore func()) {
	prev := loadSchema
	loadSchema = f
	return func() { loadSchema = prev }
}
