package mgmtapi_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"

	_ "shepherd/gen/shepherd/mgmt/v1" // register the mgmt descriptors
	"shepherd/internal/auth"
	"shepherd/internal/config"
	"shepherd/internal/mgmtapi"
	"shepherd/internal/store"
	"shepherd/internal/store/sqlc"
)

// carriesRawAlloy reports whether a request message has Alloy text (a
// string field named contents or content) or a visual graph (a
// GraphDocument field), directly.
func carriesRawAlloy(md protoreflect.MessageDescriptor) bool {
	fields := md.Fields()
	for i := range fields.Len() {
		f := fields.Get(i)
		if f.Kind() == protoreflect.StringKind && (f.Name() == "contents" || f.Name() == "content") {
			return true
		}
		if f.Kind() == protoreflect.MessageKind && f.Message().Name() == "GraphDocument" {
			return true
		}
	}
	return false
}

// Final review of #388: every procedure that takes Alloy text or a graph
// refuses one over the pipeline contents cap before parsing or rendering it
// (FormatPipeline let a viewer crash the server). The table must list every
// such procedure — a new one fails here until it is capped and listed — and
// each listed one is exercised with an oversized request.
var _ = Describe("Raw Alloy request caps", Label("integration"), func() {
	It("lists every procedure whose request carries Alloy text or a graph", func() {
		var found []string
		protoregistry.GlobalFiles.RangeFilesByPackage("shepherd.mgmt.v1", func(fd protoreflect.FileDescriptor) bool {
			if fd.Package() != "shepherd.mgmt.v1" {
				return true
			}
			svcs := fd.Services()
			for i := range svcs.Len() {
				methods := svcs.Get(i).Methods()
				for j := range methods.Len() {
					m := methods.Get(j)
					if carriesRawAlloy(m.Input()) {
						found = append(found, "/"+string(svcs.Get(i).FullName())+"/"+string(m.Name()))
					}
				}
			}
			return true
		})
		Expect(found).NotTo(BeEmpty())
		for _, p := range found {
			Expect(mgmtapi.RawAlloyProceduresForTest).To(HaveKey(p), "%s takes Alloy text or a graph but is not capped", p)
		}
		Expect(mgmtapi.RawAlloyProceduresForTest).To(HaveLen(len(found)))
	})

	It("refuses an oversized request on each of them", func() {
		ctx := context.Background()
		st, err := store.New(ctx, &config.DatabaseConfig{URL: sharedPG.IsolatedDB(ctx, GinkgoTB()), MaxConns: 4}, slog.Default())
		Expect(err).NotTo(HaveOccurred())
		defer st.Close()
		org, err := st.Queries.CreateOrg(ctx, sqlc.CreateOrgParams{Name: "caps-org", DisplayName: "Caps", AdminGroupID: "caps-admins"})
		Expect(err).NotTo(HaveOccurred())
		p, err := st.Queries.CreatePipeline(ctx, sqlc.CreatePipelineParams{
			OrgID: org.ID, Name: "p", Contents: "// x", Matchers: json.RawMessage(`[]`), Source: "ui",
		})
		Expect(err).NotTo(HaveOccurred())
		cfg := &config.Config{Auth: config.AuthConfig{InsecureCookies: true}}
		cfg.Simulator.Enabled = true
		server := httptest.NewServer(newRPCWiringRouter(st, auth.NewLocalAdmin(cfg, st, slog.Default()), cfg))
		defer server.Close()
		admin := newAppAdminSession(ctx, st)

		big := "// " + strings.Repeat("x", mgmtapi.MaxPipelineContentsBytes)
		graph := map[string]any{"kind": strings.Repeat("x", mgmtapi.MaxPipelineContentsBytes+1)}
		bodies := map[string]map[string]any{
			"/shepherd.mgmt.v1.PipelineService/CreatePipeline":   {"orgId": org.ID.String(), "name": "big", "contents": big, "matchers": []string{}},
			"/shepherd.mgmt.v1.PipelineService/UpdatePipeline":   {"orgId": org.ID.String(), "id": p.ID.String(), "name": "p", "contents": big, "matchers": []string{}},
			"/shepherd.mgmt.v1.PipelineService/ValidatePipeline": {"orgId": org.ID.String(), "contents": big},
			"/shepherd.mgmt.v1.PipelineService/FormatPipeline":   {"orgId": org.ID.String(), "contents": big},
			"/shepherd.mgmt.v1.VisualService/Render":             {"orgId": org.ID.String(), "graph": graph},
			"/shepherd.mgmt.v1.VisualService/Validate":           {"orgId": org.ID.String(), "graph": graph},
			"/shepherd.mgmt.v1.VisualService/UpgradeCheck":       {"orgId": org.ID.String(), "graph": graph},
			"/shepherd.mgmt.v1.SimulateService/CreateRun":        {"orgId": org.ID.String(), "graph": graph},
		}
		Expect(bodies).To(HaveLen(len(mgmtapi.RawAlloyProceduresForTest)))
		for proc := range mgmtapi.RawAlloyProceduresForTest {
			body, ok := bodies[proc]
			Expect(ok).To(BeTrue(), "no oversized request for %s", proc)
			resp := postJSON(server, proc, body, admin)
			raw, err := io.ReadAll(resp.Body)
			Expect(err).NotTo(HaveOccurred())
			_ = resp.Body.Close() //nolint:errcheck // test cleanup
			out := map[string]any{}
			Expect(json.Unmarshal(raw, &out)).To(Succeed(), string(raw))
			Expect(resp.StatusCode).To(Equal(http.StatusBadRequest), "%s: %.300s", proc, raw)
			Expect(out["code"]).To(Equal("invalid_argument"), proc)
		}
	})
})
