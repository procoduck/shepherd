package mcp

import (
	"shepherd/gen/shepherd/mgmt/v1/mgmtv1connect"
)

// toolProcedures maps every MCP tool this server exposes to the
// shepherd.mgmt.v1 Connect procedure(s) its handler calls. This is the
// "mapping table" G14 requires be enumerated and checked
// (docs/gateway-tier-plan.md: "If you expose MCP tools that map to Connect
// procedures, the mapping table itself must be enumerated and checked").
//
// registry_test.go's TestNoToolReachesAMutatingProcedure walks this table
// against the LIVE set of mutating procedures derived from the registered
// proto descriptors (the same source internal/mgmtapi's own
// TestEveryMutatingProcedureIsCapabilityClassified uses) and fails if any
// entry here names one — so a tool added later that happens to wire up a
// write path is caught by a static check, not discovered by a red run
// against a real server. g14_g15_test.go supplies the complementary,
// stronger runtime proof: it calls EVERY live mutating procedure directly
// with this server's actual propose-scoped credential and asserts
// PermissionDenied for literally all of them, not only the ones this table
// happens to name today.
var toolProcedures = map[string][]string{ //nolint:gochecknoglobals // static mapping table, read-only after init, checked by registry_test.go
	"list_collectors":       {mgmtv1connect.FleetServiceListCollectorsProcedure},
	"get_collector":         {mgmtv1connect.FleetServiceGetCollectorProcedure},
	"list_fleet_attributes": {mgmtv1connect.FleetServiceListAttributesProcedure},
	"list_pipelines":        {mgmtv1connect.PipelineServiceListPipelinesProcedure},
	"get_pipeline":          {mgmtv1connect.PipelineServiceGetPipelineProcedure},
	"validate_pipeline":     {mgmtv1connect.PipelineServiceValidatePipelineProcedure},
	"preview_matches":       {mgmtv1connect.PipelineServicePreviewMatchesProcedure},
	"simulate_relabel":      {mgmtv1connect.SimulateServiceSimulateRelabelProcedure},
	"simulate_logs":         {mgmtv1connect.SimulateServiceSimulateLogsProcedure},
	"create_sandbox_run":    {mgmtv1connect.SimulateServiceCreateRunProcedure},
	"get_sandbox_run":       {mgmtv1connect.SimulateServiceGetRunProcedure},
	// propose_pipeline_revision is a composite tool: it calls
	// ValidatePipeline and (when proposing an update to an existing
	// pipeline) PreviewMatches. It never calls CreatePipeline or
	// UpdatePipeline — see propose.go's doc comment for why "propose" ends
	// at handing back a vetted, unpersisted proposal rather than writing
	// anything.
	"propose_pipeline_revision": {
		mgmtv1connect.PipelineServiceValidatePipelineProcedure,
		mgmtv1connect.PipelineServicePreviewMatchesProcedure,
	},
}
