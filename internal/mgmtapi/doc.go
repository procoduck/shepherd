// Package mgmtapi implements the shepherd.mgmt.v1 Connect services (Me,
// Admin, User, Fleet, Pipeline, Destination, GitOps, Wizard, Visual,
// Simulate, Audit, TenantRoute, Team, ServiceAccount — 14 services, mounted
// by MountRPC in router.go, each behind the shared authz interceptor in
// rpc_interceptor.go), plus Router (router.go, mounted at /api), which
// serves only the Alloy schema artifacts the Connect contract deliberately
// leaves out. The legacy /api REST shim was removed in v0.11.0.
package mgmtapi
