package mcp

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"

	// Blank-imported so the proto file descriptors are registered in
	// protoregistry.GlobalFiles even though this test never calls a
	// generated constructor directly (registry.go's toolProcedures map
	// already imports mgmtv1connect for its procedure constants, which
	// pulls this in transitively — imported again here, explicitly, so
	// this file's dependency on descriptor registration does not rely on
	// that transitivity surviving a refactor).
	_ "shepherd/gen/shepherd/mgmt/v1/mgmtv1connect"
)

// allShepherdMgmtV1Procedures walks the LIVE registered proto descriptors
// for every shepherd.mgmt.v1 Connect procedure, mirroring
// internal/mgmtapi/capability_enumeration_test.go's allServiceProcedures
// byte-for-byte in approach (not by importing it — that test lives in an
// unexported package this one must not depend on): the point in both
// places is the same, an authoritative source neither table can silently
// drift from.
func allShepherdMgmtV1Procedures(tb testing.TB) []string {
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
		tb.Fatal("allShepherdMgmtV1Procedures found zero methods — proto descriptors are not registered, this test is not testing anything")
	}
	return procs
}

// writeVerbPrefixes mirrors internal/mgmtapi/capability_enumeration_test.go's
// writeVerbPrefixes exactly (see that file's comment for why method-name
// verbs, not a hand-kept classification, are the authoritative signal of
// "this procedure mutates state"). Duplicated here rather than imported
// because it lives in an unexported var in a different package; the two
// are pinned to the same list by comment cross-reference, and a mismatch
// would show up as this test flagging (or missing) a procedure the mgmtapi
// suite already classified differently, so drift cannot hide silently.
var writeVerbPrefixes = []string{ //nolint:gochecknoglobals // static test fixture, mirrors mgmtapi's
	"Create", "Update", "Delete", "Enable", "Disable", "Revoke", "Claim", "Unclaim", "Rotate", "Set", "Commit",
}

// nonMutatingNameExceptions mirrors
// internal/mgmtapi/capability_enumeration_test.go's identical var: the one
// Create-named procedure that does not require apply capability because it
// persists a sandboxed dry-run artifact, not production state (G12's
// nonMutatingNameExceptions doc comment explains why in full).
var nonMutatingNameExceptions = map[string]bool{ //nolint:gochecknoglobals // static test fixture, mirrors mgmtapi's
	"/shepherd.mgmt.v1.SimulateService/CreateRun": true,
}

// looksLikeAWrite classifies proc by method-name verb, exactly as
// internal/mgmtapi/capability_enumeration_test.go's
// TestEveryMutatingProcedureIsCapabilityClassified does.
func looksLikeAWrite(proc string) bool {
	methodName := proc[strings.LastIndex(proc, "/")+1:]
	for _, verb := range writeVerbPrefixes {
		if strings.HasPrefix(methodName, verb) {
			return !nonMutatingNameExceptions[proc]
		}
	}
	return false
}

// TestNoToolReachesAMutatingProcedure is G14's static half
// (docs/gateway-tier-plan.md: "the mapping table itself must be enumerated
// and checked"). It derives the mutating set from the live proto
// descriptors — not from toolProcedures itself, which would make this
// test unable to catch anything — and fails if any MCP tool names a
// procedure that set contains. See g14_g15_test.go for the runtime
// counterpart: calling every live mutating procedure with this server's
// actual propose-scoped credential and asserting refusal, which this test
// alone cannot prove (a table can be correct today and still say nothing
// about what actually happens over the wire).
func TestNoToolReachesAMutatingProcedure(t *testing.T) {
	mutating := map[string]bool{}
	for _, proc := range allShepherdMgmtV1Procedures(t) {
		if looksLikeAWrite(proc) {
			mutating[proc] = true
		}
	}
	if len(mutating) == 0 {
		t.Fatal("zero mutating procedures found — the write-verb heuristic or descriptor registration is broken, this test is not testing anything")
	}

	for tool, procs := range toolProcedures {
		for _, proc := range procs {
			if mutating[proc] {
				t.Errorf("tool %q maps to %s, which looks like a mutating procedure by name — "+
					"an MCP tool must never reach a write path (G14); if this procedure genuinely "+
					"does not mutate production state, it needs the same documented exception "+
					"internal/mgmtapi/capability_enumeration_test.go's nonMutatingNameExceptions uses, "+
					"restated in this file's copy", tool, proc)
			}
		}
	}
}

// TestEveryRegisteredToolHasAServerEntry catches the OTHER drift direction:
// a tool added to NewServer (tools.go) but never entered into
// toolProcedures, which would make TestNoToolReachesAMutatingProcedure
// silently stop covering it. Since toolProcedures is hand-maintained
// (there is no live-descriptor equivalent for "which tools does this MCP
// server register" the way protoregistry gives us for gRPC/Connect
// procedures), this test instead pins the exact tool NAME set NewServer
// registers, read back from a live server instance, against
// toolProcedures's keys — so adding a tool without a mapping entry fails
// here immediately rather than silently under-covering G14.
func TestEveryRegisteredToolHasAServerEntry(t *testing.T) {
	b := &Backend{} // tool handlers are registered as method values; none is invoked here.
	names := registeredToolNames(t, NewServer(b, "test"))
	for _, name := range names {
		if _, ok := toolProcedures[name]; !ok {
			t.Errorf("tool %q is registered on the server but has no toolProcedures entry (registry.go) — "+
				"G14's static check cannot cover a tool it doesn't know about", name)
		}
	}
	for name := range toolProcedures {
		if !contains(names, name) {
			t.Errorf("toolProcedures names %q, but NewServer never registers a tool by that name — stale entry", name)
		}
	}
}

// registeredToolNames connects a real in-process MCP client/server pair
// (mcp.NewInMemoryTransports) and calls tools/list, so it reflects exactly
// what NewServer actually registers — not a hand-kept list a registration
// call could drift from.
func registeredToolNames(t *testing.T, s *mcp.Server) []string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := s.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatalf("server.Connect: %v", err)
	}
	defer serverSession.Close() //nolint:errcheck // test cleanup

	client := mcp.NewClient(&mcp.Implementation{Name: "registry-test-client", Version: "test"}, nil)
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("client.Connect: %v", err)
	}
	defer clientSession.Close() //nolint:errcheck // test cleanup

	res, err := clientSession.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	names := make([]string, 0, len(res.Tools))
	for _, tool := range res.Tools {
		names = append(names, tool.Name)
	}
	return names
}

func contains(ss []string, s string) bool {
	for _, v := range ss {
		if v == s {
			return true
		}
	}
	return false
}
