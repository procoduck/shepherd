package server

// Test-only exports: the specs in server_test.go (package server_test) must
// assert against the production route tree and metrics mux, not fixtures.
var (
	NewRouter     = newRouter
	NewMetricsMux = newMetricsMux
)
