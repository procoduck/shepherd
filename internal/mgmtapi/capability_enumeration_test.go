package mgmtapi

import (
	"fmt"
	"strings"
	"testing"

	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"

	"shepherd/gen/shepherd/mgmt/v1/mgmtv1connect"
)

// allServiceProcedures walks every registered shepherd.mgmt.v1 service
// method and returns its Connect procedure name
// ("/shepherd.mgmt.v1.Xxx/Yyy"), matching mgmtv1connect's own
// XxxServiceYyyProcedure constant format exactly. This is read from the
// proto FILE DESCRIPTORS themselves, not from procedureRequirements or
// capabilityRequirements — the whole point is a source of truth neither
// table can silently drift from, because a newly added RPC exists in the
// descriptors the instant `buf generate` runs, before anyone remembers to
// touch either table.
func allServiceProcedures(tb testing.TB) []string {
	tb.Helper()
	var procs []string
	protoregistry.GlobalFiles.RangeFiles(func(fd protoreflect.FileDescriptor) bool {
		if fd.Package() != "shepherd.mgmt.v1" {
			return true
		}
		services := fd.Services()
		for i := 0; i < services.Len(); i++ {
			svc := services.Get(i)
			methods := svc.Methods()
			for j := 0; j < methods.Len(); j++ {
				procs = append(procs, fmt.Sprintf("/%s/%s", svc.FullName(), methods.Get(j).Name()))
			}
		}
		return true
	})
	if len(procs) == 0 {
		tb.Fatal("allServiceProcedures found zero shepherd.mgmt.v1 methods — the blank mgmtv1connect import is not registering descriptors, this test is not testing anything")
	}
	return procs
}

// TestEveryProcedureHasAnAuthzRequirement is the pre-W10 fail-closed
// contract restated as an explicit, named test rather than only an
// interceptor runtime behavior (newAuthzInterceptor already denies an
// unclassified procedure — see errUnknownProcedure — but that only fires
// if some caller actually invokes the new RPC in a test; this fires the
// instant the RPC exists, whether or not anything calls it yet).
func TestEveryProcedureHasAnAuthzRequirement(t *testing.T) {
	for _, proc := range allServiceProcedures(t) {
		if _, ok := procedureRequirements[proc]; !ok {
			t.Errorf("procedure %s has no entry in procedureRequirements (rpc_interceptor.go) — newAuthzInterceptor denies it at runtime, but it must be classified explicitly, not merely default-denied", proc)
		}
	}
}

// writeVerbPrefixes are the method-name verbs this API uses, consistently,
// for every RPC that mutates stored state — read off the actual set of
// procedures in procedureRequirements (rpc_interceptor.go): every
// Create/Update/Delete/Enable/Disable/Revoke/Claim/Unclaim/Rotate/Set/Commit
// writes; every List/Get/Preview/Validate/Render/Simulate/GraphView/Search/
// Test/UpgradeCheck does not. Deriving "is a write" from the METHOD NAME
// (read straight off the descriptor) rather than from procedureRequirements'
// role values is deliberate: several org-admin-gated procedures are reads
// gated admin-only for VISIBILITY (ListAudit, ListCredentials,
// TestCredential, VisualService.Render/Validate/UpgradeCheck,
// SimulateService's whole surface) — role level alone cannot distinguish
// "admin-only read" from "write", so this test does not try to.
var writeVerbPrefixes = []string{ //nolint:gochecknoglobals // static test fixture
	"Create", "Update", "Delete", "Enable", "Disable", "Revoke", "Claim", "Unclaim", "Rotate", "Set", "Commit",
}

// nonMutatingNameExceptions are procedures whose method name matches a
// write verb but do not mutate PRODUCTION state, so G12 does not require
// apply capability to reach them. SimulateService.CreateRun is the one
// case in this API: it persists a simulate_runs row, but that row is a
// sandboxed dry-run result (docs/gateway-tier-plan.md §3a: "try-before-commit
// is unusually complete... render → validate → simulate... all before a
// collector sees anything") — deliberately propose-safe, the same way a
// future W11 agent must be able to simulate freely while apply stays
// gated. Any addition to this set must justify, in this comment, why the
// procedure's Create/Update/Delete-shaped name does not mean what it means
// everywhere else in this table.
var nonMutatingNameExceptions = map[string]bool{ //nolint:gochecknoglobals // static test fixture
	mgmtv1connect.SimulateServiceCreateRunProcedure: true,
}

// TestEveryMutatingProcedureIsCapabilityClassified is G12's enumeration
// test (docs/gateway-tier-plan.md: "the single most valuable thing in this
// workstream: write it first"). It does not by itself prove enforcement —
// requireWriteAuthorized (machine_auth.go), called per write path inside
// each handler, does that; a red run against THIS test only proves a
// classification was forgotten, not that the check was skipped. That is
// deliberate division of labor: this test makes forgetting to CLASSIFY a
// new write path impossible to do silently; the per-handler
// requireWriteAuthorized calls make forgetting to ENFORCE the
// classification impossible to do silently.
func TestEveryMutatingProcedureIsCapabilityClassified(t *testing.T) {
	for _, proc := range allServiceProcedures(t) {
		methodName := proc[strings.LastIndex(proc, "/")+1:]
		isWrite := false
		for _, verb := range writeVerbPrefixes {
			if strings.HasPrefix(methodName, verb) {
				isWrite = true
				break
			}
		}
		if !isWrite || nonMutatingNameExceptions[proc] {
			continue
		}
		capReq, ok := capabilityRequirements[proc]
		if !ok {
			t.Errorf("procedure %s looks like a write (method name %q) but has no entry in capabilityRequirements — a new mutating RPC must be classified propose-safe or apply-only before it ships, per G12; if it genuinely does not mutate production state, add it to nonMutatingNameExceptions with a reason", proc, methodName)
			continue
		}
		if capReq != capabilityApply {
			t.Errorf("procedure %s is classified %q in capabilityRequirements; every currently-shipped write requires %q (there is no propose-workflow RPC yet — W11 introduces one)", proc, capReq, capabilityApply)
		}
	}
}
