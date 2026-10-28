package mgmtapi_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/auth"
	"shepherd/internal/config"
	"shepherd/internal/store"
	"shepherd/internal/store/sqlc"
	"shepherd/internal/tenancy"
	"shepherd/internal/validate"
)

// Connect-path coverage for TenantRouteService (W4,
// docs/gateway-tier-plan.md §4), over the same newRPCWiringRouter(...)
// wiring rpc_destination_test.go uses: a happy path (create, list, rotate,
// revoke), an authz denial, and an error-code mapping case.
var _ = Describe("shepherd.mgmt.v1.TenantRouteService RPC", Label("integration"), func() {
	var (
		ctx         context.Context
		cancel      context.CancelFunc
		st          *store.Store
		authHandler *auth.Handler
		server      *httptest.Server
		orgID       pgtype.UUID
	)

	BeforeEach(func() {
		ctx, cancel = context.WithCancel(context.Background())
		dbURL := sharedPG.IsolatedDB(ctx, GinkgoTB())

		var err error
		st, err = store.New(ctx, &config.DatabaseConfig{URL: dbURL, MaxConns: 5}, slog.Default())
		Expect(err).NotTo(HaveOccurred())

		o, err := st.Queries.CreateOrg(ctx, sqlc.CreateOrgParams{
			Name:          "tenant-route-rpc-org",
			DisplayName:   "Tenant Route RPC Org",
			AdminGroupID:  "tenant-route-admin-group",
			ViewerGroupID: pgtype.Text{String: "tenant-route-viewer-group", Valid: true},
		})
		Expect(err).NotTo(HaveOccurred())
		orgID = o.ID
		// Which tenants an org owns is set by an app admin (org_tenants,
		// 0039). A route names one of them, or gets the default.
		giveOrgTenant(ctx, st, orgID, "acme")

		cfg := &config.Config{Auth: config.AuthConfig{InsecureCookies: true}}
		cfg.Gateway.Routes.PublicBaseURL = "https://telemetry.example.com"
		// MountRPC without WithRouteApplyRunning takes the config's word:
		// these specs read as a server whose reconciler runs.
		cfg.Gateway.Routes.Apply.Enabled = true
		authHandler = auth.NewLocalAdmin(cfg, st, slog.Default())
		server = httptest.NewServer(newRPCWiringRouter(st, authHandler, cfg))
	})

	AfterEach(func() {
		server.Close()
		st.Close()
		cancel()
	})

	createSession := func(isAppAdmin bool, groupIDs []string) *http.Cookie {
		groupsJSON, err := json.Marshal(groupIDs)
		Expect(err).NotTo(HaveOccurred())
		sessionID := fmt.Sprintf("tenant-route-rpc-session-%d", time.Now().UnixNano())
		_, err = st.Queries.CreateSession(ctx, sqlc.CreateSessionParams{
			ID: sessionID, UserOid: "tenant-route-user-oid", Email: "tenant-route-user@example.com", DisplayName: "Tenant Route User",
			GroupIds:   groupsJSON,
			IsAppAdmin: isAppAdmin,
			ExpiresAt:  pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true},
			Source:     "test",
		})
		Expect(err).NotTo(HaveOccurred())
		return &http.Cookie{Name: "shepherd_session", Value: sessionID}
	}

	postConnect := func(procedure string, body map[string]any, cookie *http.Cookie) *http.Response {
		buf, err := json.Marshal(body)
		Expect(err).NotTo(HaveOccurred())
		req, err := http.NewRequest(http.MethodPost, server.URL+procedure, strings.NewReader(string(buf)))
		Expect(err).NotTo(HaveOccurred())
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Requested-With", "XMLHttpRequest")
		if cookie != nil {
			req.AddCookie(cookie)
		}
		resp, err := http.DefaultClient.Do(req)
		Expect(err).NotTo(HaveOccurred())
		return resp
	}

	decodeBody := func(resp *http.Response) map[string]any {
		defer resp.Body.Close() //nolint:errcheck // test cleanup
		body, err := io.ReadAll(resp.Body)
		Expect(err).NotTo(HaveOccurred())
		var payload map[string]any
		Expect(json.Unmarshal(body, &payload)).To(Succeed())
		return payload
	}

	createBody := map[string]any{
		"orgId": "", "kind": "otlp",
		"gatewayMode": "managed", "gatewayName": "shepherd-receiver-gw",
	}

	It("creates, lists, rotates, and revokes a tenant route over the Connect handler (happy path)", func() {
		admin := createSession(false, []string{"tenant-route-admin-group"})
		body := map[string]any{}
		for k, v := range createBody {
			body[k] = v
		}
		body["orgId"] = orgID.String()

		createResp := postConnect("/shepherd.mgmt.v1.TenantRouteService/CreateTenantRoute", body, admin)
		Expect(createResp.StatusCode).To(Equal(http.StatusOK))
		created := decodeBody(createResp)
		Expect(created["tenantId"]).To(Equal("acme"))
		Expect(created["kind"]).To(Equal("otlp"))
		Expect(created["status"]).To(Equal("TENANT_ROUTE_STATUS_ACTIVE"))
		Expect(created["gatewayMode"]).To(Equal("managed"))
		segment, ok := created["segment"].(string)
		Expect(ok).To(BeTrue(), "expected a segment string")
		Expect(segment).To(HavePrefix("acme-"), "default format is slug-suffix (D9)")
		routeID, ok := created["id"].(string)
		Expect(ok).To(BeTrue(), "expected an id string")

		viewer := createSession(false, []string{"tenant-route-viewer-group"})
		listResp := postConnect("/shepherd.mgmt.v1.TenantRouteService/ListTenantRoutes", map[string]any{"orgId": orgID.String()}, viewer)
		Expect(listResp.StatusCode).To(Equal(http.StatusOK))
		listed := decodeBody(listResp)
		items, ok := listed["items"].([]any)
		Expect(ok).To(BeTrue(), "expected an items array")
		Expect(items).To(HaveLen(1))
		first, ok := items[0].(map[string]any)
		Expect(ok).To(BeTrue())
		Expect(first["applyStatus"]).To(Equal("TENANT_ROUTE_APPLY_STATUS_PENDING"), "Create only writes the row; nothing has applied it yet")
		Expect(first).NotTo(HaveKey("appliedAt"))

		rotateResp := postConnect("/shepherd.mgmt.v1.TenantRouteService/RotateTenantRoute",
			map[string]any{"orgId": orgID.String(), "id": routeID}, admin)
		Expect(rotateResp.StatusCode).To(Equal(http.StatusOK))
		rotated := decodeBody(rotateResp)
		active, ok := rotated["active"].(map[string]any)
		Expect(ok).To(BeTrue())
		deprecated, ok := rotated["deprecated"].(map[string]any)
		Expect(ok).To(BeTrue())
		Expect(active["status"]).To(Equal("TENANT_ROUTE_STATUS_ACTIVE"))
		Expect(active["segment"]).NotTo(Equal(segment), "rotation must mint a NEW segment")
		Expect(active["rotatedFromId"]).To(Equal(routeID))
		Expect(deprecated["id"]).To(Equal(routeID))
		Expect(deprecated["status"]).To(Equal("TENANT_ROUTE_STATUS_DEPRECATED"))
		Expect(deprecated["validUntil"]).NotTo(BeEmpty(), "a deprecated route must carry its rotation-overlap deadline")

		listResp2 := postConnect("/shepherd.mgmt.v1.TenantRouteService/ListTenantRoutes", map[string]any{"orgId": orgID.String()}, viewer)
		listed2 := decodeBody(listResp2)
		items2, ok := listed2["items"].([]any)
		Expect(ok).To(BeTrue(), "expected an items array")
		Expect(items2).To(HaveLen(2), "rotation must leave BOTH the deprecated and the new active route visible, not replace in place")

		activeID, ok := active["id"].(string)
		Expect(ok).To(BeTrue(), "expected an id string")
		revokeResp := postConnect("/shepherd.mgmt.v1.TenantRouteService/RevokeTenantRoute",
			map[string]any{"orgId": orgID.String(), "id": activeID}, admin)
		Expect(revokeResp.StatusCode).To(Equal(http.StatusOK))
		revoked := decodeBody(revokeResp)
		Expect(revoked["status"]).To(Equal("TENANT_ROUTE_STATUS_REVOKED"))
		Expect(revoked["revokedAt"]).NotTo(BeEmpty())

		// Every route write is audited (#199): none of the three was before.
		rows, err := st.Pool().Query(ctx, "SELECT action FROM audit_log WHERE resource_type = 'tenant_route' ORDER BY at")
		Expect(err).NotTo(HaveOccurred())
		var actions []string
		for rows.Next() {
			var a string
			Expect(rows.Scan(&a)).To(Succeed())
			actions = append(actions, a)
		}
		rows.Close()
		Expect(actions).To(Equal([]string{"tenant_route.create", "tenant_route.rotate", "tenant_route.revoke"}))
	})

	// Strict tenant mode checks gateway route endpoints: a route the org
	// rotates or revokes changes which of its pipelines may send to the
	// gateway, so each route write marks the org's served configs dirty and
	// recomputes them. Checked where it is consumed: the collector's served
	// config.
	It("recomputes a strict org's served configs when a route is rotated or revoked", func() {
		restore, err := tenancy.SetGatewayHosts([]string{"telemetry.example.com"})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(restore)
		_, err = st.Queries.SetOrgStrictTenants(ctx, sqlc.SetOrgStrictTenantsParams{ID: orgID, StrictTenants: true})
		Expect(err).NotTo(HaveOccurred())
		cluster, err := st.Queries.UpsertCluster(ctx, "route-strict-cluster")
		Expect(err).NotTo(HaveOccurred())
		Expect(st.Queries.ClaimCluster(ctx, sqlc.ClaimClusterParams{ID: cluster.ID, OrgID: orgID})).To(Succeed())
		collector, err := st.Queries.UpsertCollector(ctx, sqlc.UpsertCollectorParams{ClusterID: cluster.ID, Role: "singleton"})
		Expect(err).NotTo(HaveOccurred())

		admin := createSession(false, []string{"tenant-route-admin-group"})
		body := map[string]any{}
		for k, v := range createBody {
			body[k] = v
		}
		body["orgId"] = orgID.String()
		created := decodeBody(postConnect("/shepherd.mgmt.v1.TenantRouteService/CreateTenantRoute", body, admin))
		segment, _ := created["segment"].(string) //nolint:errcheck // checked below
		routeID, _ := created["id"].(string)      //nolint:errcheck // checked below
		Expect(segment).NotTo(BeEmpty())
		// Stored directly, as a pipeline saved before the route existed.
		_, err = st.Queries.CreatePipeline(ctx, sqlc.CreatePipelineParams{
			OrgID: orgID, Name: "via-route", Matchers: json.RawMessage(`["cluster=\"route-strict-cluster\""]`), Source: "ui", Enabled: true,
			Contents: "otelcol.exporter.otlphttp \"o\" {\n  client {\n    endpoint = \"https://telemetry.example.com/otlp/" + segment + "\"\n  }\n}\n",
		})
		Expect(err).NotTo(HaveOccurred())
		served := func() string {
			c, _ := st.Queries.GetServeCache(ctx, collector.ID) //nolint:errcheck // polled
			if c.Dirty {
				return ""
			}
			return c.Content
		}

		// Rotation keeps the old segment routing during the overlap: served.
		Expect(postConnect("/shepherd.mgmt.v1.TenantRouteService/RotateTenantRoute",
			map[string]any{"orgId": orgID.String(), "id": routeID}, admin).StatusCode).To(Equal(http.StatusOK))
		Eventually(served).WithTimeout(10 * time.Second).Should(ContainSubstring(`declare "pipe_via_route"`))

		// Revoked, the segment no longer routes for the org: excluded.
		Expect(postConnect("/shepherd.mgmt.v1.TenantRouteService/RevokeTenantRoute",
			map[string]any{"orgId": orgID.String(), "id": routeID}, admin).StatusCode).To(Equal(http.StatusOK))
		Eventually(served).WithTimeout(10 * time.Second).Should(And(
			ContainSubstring("// Excluded (1) - strict tenant check:"), Not(ContainSubstring(`declare "pipe_via_route"`))))

		// The save path refuses the revoked segment and accepts the org's
		// active route.
		routes := decodeBody(postConnect("/shepherd.mgmt.v1.TenantRouteService/ListTenantRoutes", map[string]any{"orgId": orgID.String()}, admin))
		var activeSegment string
		items, ok := routes["items"].([]any)
		Expect(ok).To(BeTrue())
		for _, it := range items {
			r, ok := it.(map[string]any)
			Expect(ok).To(BeTrue())
			if r["status"] == "TENANT_ROUTE_STATUS_ACTIVE" {
				activeSegment, _ = r["segment"].(string) //nolint:errcheck // checked below
			}
		}
		Expect(activeSegment).NotTo(BeEmpty())
		save := func(name, segment string) (int, string) {
			resp := postConnect("/shepherd.mgmt.v1.PipelineService/CreatePipeline", map[string]any{
				"orgId": orgID.String(), "name": name, "matchers": []string{`cluster="route-strict-cluster"`},
				"contents": "otelcol.exporter.otlphttp \"o\" {\n  client {\n    endpoint = \"https://telemetry.example.com/otlp/" + segment + "\"\n  }\n}\n",
			}, admin)
			b, _ := io.ReadAll(resp.Body) //nolint:errcheck // checked by the caller
			resp.Body.Close()             //nolint:errcheck // test cleanup
			return resp.StatusCode, string(b)
		}
		code, msg := save("revoked-route", segment)
		Expect(code).To(Equal(http.StatusBadRequest), msg)
		validated := decodeBody(postConnect("/shepherd.mgmt.v1.PipelineService/ValidatePipeline", map[string]any{
			"orgId": orgID.String(), "name": "probe",
			"contents": "otelcol.exporter.otlphttp \"o\" {\n  client {\n    endpoint = \"https://telemetry.example.com/otlp/" + segment + "\"\n  }\n}\n",
		}, admin))
		Expect(fmt.Sprint(validated["diagnostics"])).To(ContainSubstring(validate.GatewayEndpointRefusal))
		code, msg = save("active-route", activeSegment)
		Expect(code).To(Equal(http.StatusOK), msg)
	})

	It("returns the apply outcome the reconciler recorded on the row", func() {
		admin := createSession(false, []string{"tenant-route-admin-group"})
		body := map[string]any{}
		for k, v := range createBody {
			body[k] = v
		}
		body["orgId"] = orgID.String()
		created := decodeBody(postConnect("/shepherd.mgmt.v1.TenantRouteService/CreateTenantRoute", body, admin))
		routeID, ok := created["id"].(string)
		Expect(ok).To(BeTrue())

		var id pgtype.UUID
		Expect(id.Scan(routeID)).To(Succeed())
		_, err := st.Queries.SetTenantRouteApplyStatus(ctx, sqlc.SetTenantRouteApplyStatusParams{
			ID: id, ApplyStatus: "refused", ApplyMessage: "NotAllowedByListeners",
		})
		Expect(err).NotTo(HaveOccurred())

		viewer := createSession(false, []string{"tenant-route-viewer-group"})
		listed := decodeBody(postConnect("/shepherd.mgmt.v1.TenantRouteService/ListTenantRoutes", map[string]any{"orgId": orgID.String()}, viewer))
		items, ok := listed["items"].([]any)
		Expect(ok).To(BeTrue())
		Expect(items).To(HaveLen(1))
		item, ok := items[0].(map[string]any)
		Expect(ok).To(BeTrue())
		Expect(item["applyStatus"]).To(Equal("TENANT_ROUTE_APPLY_STATUS_REFUSED"))
		Expect(item["applyMessage"]).To(Equal("NotAllowedByListeners"))
		Expect(item).NotTo(HaveKey("appliedAt"), "a refusal is not a verified apply")
	})

	It("reports a route no reconciler will apply as not_applicable, with the reason (#304)", func() {
		off := &config.Config{Auth: config.AuthConfig{InsecureCookies: true}}
		offServer := httptest.NewServer(newRPCWiringRouter(st, auth.NewLocalAdmin(off, st, slog.Default()), off))
		defer offServer.Close()
		post := func(procedure string, body map[string]any, cookie *http.Cookie) map[string]any {
			buf, err := json.Marshal(body)
			Expect(err).NotTo(HaveOccurred())
			req, err := http.NewRequest(http.MethodPost, offServer.URL+procedure, strings.NewReader(string(buf)))
			Expect(err).NotTo(HaveOccurred())
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-Requested-With", "XMLHttpRequest")
			req.AddCookie(cookie)
			resp, err := http.DefaultClient.Do(req)
			Expect(err).NotTo(HaveOccurred())
			Expect(resp.StatusCode).To(Equal(http.StatusOK))
			return decodeBody(resp)
		}
		admin := createSession(false, []string{"tenant-route-admin-group"})
		body := map[string]any{}
		for k, v := range createBody {
			body[k] = v
		}
		body["orgId"] = orgID.String()
		created := post("/shepherd.mgmt.v1.TenantRouteService/CreateTenantRoute", body, admin)
		Expect(created["applyStatus"]).To(Equal("TENANT_ROUTE_APPLY_STATUS_NOT_APPLICABLE"))
		Expect(created["applyMessage"]).To(ContainSubstring("tenant-route apply is not running"))

		listed := post("/shepherd.mgmt.v1.TenantRouteService/ListTenantRoutes", map[string]any{"orgId": orgID.String()}, admin)
		items, ok := listed["items"].([]any)
		Expect(ok).To(BeTrue())
		Expect(items).To(HaveLen(1))
		item, ok := items[0].(map[string]any)
		Expect(ok).To(BeTrue())
		Expect(item["applyStatus"]).To(Equal("TENANT_ROUTE_APPLY_STATUS_NOT_APPLICABLE"))
		Expect(item["applyMessage"]).To(ContainSubstring("gateway.routes.apply.enabled"))

		// The row itself stays pending, so a reconciler started later applies it.
		var id pgtype.UUID
		Expect(id.Scan(created["id"])).To(Succeed())
		row, err := st.Queries.GetTenantRouteByID(ctx, id)
		Expect(err).NotTo(HaveOccurred())
		Expect(row.ApplyStatus).To(Equal("pending"))
	})

	Describe("RenderConnectApp", func() {
		var admin, viewer *http.Cookie

		createRoute := func(kind string) map[string]any {
			body := map[string]any{}
			for k, v := range createBody {
				body[k] = v
			}
			body["orgId"] = orgID.String()
			body["kind"] = kind
			resp := postConnect("/shepherd.mgmt.v1.TenantRouteService/CreateTenantRoute", body, admin)
			Expect(resp.StatusCode).To(Equal(http.StatusOK))
			return decodeBody(resp)
		}
		render := func(cookie *http.Cookie, body map[string]any) (int, map[string]any) {
			resp := postConnect("/shepherd.mgmt.v1.TenantRouteService/RenderConnectApp", body, cookie)
			return resp.StatusCode, decodeBody(resp)
		}

		BeforeEach(func() {
			admin = createSession(false, []string{"tenant-route-admin-group"})
			viewer = createSession(false, []string{"tenant-route-viewer-group"})
		})

		It("renders every artifact for an org viewer, from the configured gateway URL", func() {
			route := createRoute("otlp")
			code, out := render(viewer, map[string]any{"orgId": orgID.String(), "id": route["id"], "serviceName": "checkout"})
			Expect(code).To(Equal(http.StatusOK), "%v", out)

			segment, ok := route["segment"].(string)
			Expect(ok).To(BeTrue())
			wantBase := "https://telemetry.example.com/otlp/" + segment
			Expect(out["baseEndpoint"]).To(Equal(wantBase), "the endpoint must be the path the route's HTTPRoute matches")
			Expect(out["gatewayBaseUrl"]).To(Equal("https://telemetry.example.com"))
			Expect(out["env"]).To(ContainSubstring("OTEL_EXPORTER_OTLP_ENDPOINT=" + wantBase))
			Expect(out["env"]).To(ContainSubstring("OTEL_SERVICE_NAME=checkout"))
			Expect(out["env"]).To(ContainSubstring("OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf"), "protobuf is the default")
			for _, k := range []string{"lambda", "terraform", "sam", "cdk", "k8s", "sdkNotes"} {
				Expect(out[k]).NotTo(BeEmpty(), "artifact %s", k)
			}
		})

		It("uses the request's gateway URL over the configured one", func() {
			route := createRoute("otlp")
			code, out := render(viewer, map[string]any{
				"orgId": orgID.String(), "id": route["id"], "serviceName": "checkout",
				"gatewayBaseUrl": "https://edge.example.org", "protocol": "http/json",
			})
			Expect(code).To(Equal(http.StatusOK), "%v", out)
			Expect(out["baseEndpoint"]).To(HavePrefix("https://edge.example.org/otlp/"))
			Expect(out["env"]).To(ContainSubstring("OTEL_EXPORTER_OTLP_PROTOCOL=http/json"))
		})

		DescribeTable("refuses what would hand out an endpoint that does not work",
			func(mutate func(body map[string]any), wantCode int, wantMsg string) {
				route := createRoute("otlp")
				body := map[string]any{"orgId": orgID.String(), "id": route["id"], "serviceName": "checkout"}
				mutate(body)
				code, out := render(viewer, body)
				Expect(code).To(Equal(wantCode), "%v", out)
				Expect(out["message"]).To(ContainSubstring(wantMsg))
			},
			Entry("a plaintext gateway URL", func(b map[string]any) { b["gatewayBaseUrl"] = "http://edge.example.org" },
				http.StatusBadRequest, "https"),
			Entry("gRPC", func(b map[string]any) { b["protocol"] = "grpc" }, http.StatusBadRequest, "gRPC"),
			Entry("no service name", func(b map[string]any) { b["serviceName"] = "  " }, http.StatusBadRequest, "service_name"),
		)

		It("refuses a revoked route, and a Faro route", func() {
			route := createRoute("otlp")
			resp := postConnect("/shepherd.mgmt.v1.TenantRouteService/RevokeTenantRoute",
				map[string]any{"orgId": orgID.String(), "id": route["id"]}, admin)
			Expect(resp.StatusCode).To(Equal(http.StatusOK))
			code, out := render(viewer, map[string]any{"orgId": orgID.String(), "id": route["id"], "serviceName": "x"})
			Expect(code).To(Equal(http.StatusBadRequest))
			Expect(out["code"]).To(Equal("failed_precondition"))
			Expect(out["message"]).To(ContainSubstring("revoked"))

			faro := createRoute("faro")
			code, out = render(viewer, map[string]any{"orgId": orgID.String(), "id": faro["id"], "serviceName": "x"})
			Expect(code).To(Equal(http.StatusBadRequest))
			Expect(out["message"]).To(ContainSubstring("Faro"))
		})

		It("does not render another org's route, and denies a caller with no access", func() {
			route := createRoute("otlp")
			other, err := st.Queries.CreateOrg(ctx, sqlc.CreateOrgParams{
				Name: "render-other", DisplayName: "Other", AdminGroupID: "render-other-admins",
				ViewerGroupID: pgtype.Text{String: "render-other-viewers", Valid: true},
			})
			Expect(err).NotTo(HaveOccurred())
			otherViewer := createSession(false, []string{"render-other-viewers"})
			code, _ := render(otherViewer, map[string]any{"orgId": other.ID.String(), "id": route["id"], "serviceName": "x"})
			Expect(code).To(Equal(http.StatusNotFound), "pairing your own org id with another org's route id")

			outsider := createSession(false, []string{"nobody"})
			code, _ = render(outsider, map[string]any{"orgId": orgID.String(), "id": route["id"], "serviceName": "x"})
			Expect(code).To(Equal(http.StatusForbidden))
		})
	})

	It("denies ListTenantRoutes for an authenticated session with no access to the org", func() {
		outsider := createSession(false, []string{"some-other-group"})
		resp := postConnect("/shepherd.mgmt.v1.TenantRouteService/ListTenantRoutes", map[string]any{"orgId": orgID.String()}, outsider)
		Expect(resp.StatusCode).To(Equal(http.StatusForbidden))

		payload := decodeBody(resp)
		Expect(payload["code"]).To(Equal("permission_denied"))
	})

	It("maps a second CreateTenantRoute for the same tenant+kind to the Connect already_exists code (409), enforcing D9's one-active-route-per-tenant model", func() {
		admin := createSession(false, []string{"tenant-route-admin-group"})
		body := map[string]any{}
		for k, v := range createBody {
			body[k] = v
		}
		body["orgId"] = orgID.String()

		first := postConnect("/shepherd.mgmt.v1.TenantRouteService/CreateTenantRoute", body, admin)
		Expect(first.StatusCode).To(Equal(http.StatusOK))
		io.ReadAll(first.Body) //nolint:errcheck // drain before reuse of the same connection

		second := postConnect("/shepherd.mgmt.v1.TenantRouteService/CreateTenantRoute", body, admin)
		Expect(second.StatusCode).To(Equal(http.StatusConflict))
		payload := decodeBody(second)
		Expect(payload["code"]).To(Equal("already_exists"))
	})

	It("uses the opaque format and discloses no tenant name fragment when requested", func() {
		admin := createSession(false, []string{"tenant-route-admin-group"})
		body := map[string]any{}
		for k, v := range createBody {
			body[k] = v
		}
		body["orgId"] = orgID.String()
		body["format"] = "opaque"

		resp := postConnect("/shepherd.mgmt.v1.TenantRouteService/CreateTenantRoute", body, admin)
		Expect(resp.StatusCode).To(Equal(http.StatusOK))
		created := decodeBody(resp)
		segment, ok := created["segment"].(string)
		Expect(ok).To(BeTrue(), "expected a segment string")
		Expect(segment).NotTo(ContainSubstring("acme"), "opaque format must not disclose the tenant name")
	})

	It("rejects rotating a route owned by a different org (404, not the org-mismatch confirmed)", func() {
		admin := createSession(false, []string{"tenant-route-admin-group"})
		body := map[string]any{}
		for k, v := range createBody {
			body[k] = v
		}
		body["orgId"] = orgID.String()
		createResp := postConnect("/shepherd.mgmt.v1.TenantRouteService/CreateTenantRoute", body, admin)
		created := decodeBody(createResp)
		routeID, ok := created["id"].(string)
		Expect(ok).To(BeTrue(), "expected an id string")

		other, err := st.Queries.CreateOrg(ctx, sqlc.CreateOrgParams{
			Name: "other-org-for-tenant-route", DisplayName: "Other Org", AdminGroupID: "other-admin-group",
		})
		Expect(err).NotTo(HaveOccurred())
		giveOrgTenant(ctx, st, other.ID, "globex")
		otherAdmin := createSession(false, []string{"other-admin-group"})

		resp := postConnect("/shepherd.mgmt.v1.TenantRouteService/RotateTenantRoute",
			map[string]any{"orgId": other.ID.String(), "id": routeID}, otherAdmin)
		Expect(resp.StatusCode).To(Equal(http.StatusNotFound))
	})

	// The hole this closes was found by a final review while still latent:
	// tenant_id used to be free-form request input, so an org admin could mint
	// an active route whose injected X-Scope-OrgID named a DIFFERENT org's
	// tenant. Once routes are applied to a cluster, that is a cross-tenant
	// write authorized by the wrong org.
	//
	// Field 2 stays reserved, so a request carrying a stray tenantId is simply
	// ignored by protojson and the route still gets the org's default tenant.
	// The checked replacement, field 8 (tenant), is covered by the "names a
	// tenant" specs below.
	It("takes tenant identity from the org, ignoring any tenant the caller tries to supply", func() {
		admin := createSession(false, []string{"tenant-route-admin-group"})
		body := map[string]any{
			"orgId": orgID.String(), "kind": "faro",
			"gatewayMode": "managed", "gatewayName": "shepherd-receiver-gw",
			// A caller reaching for the old field, or a hand-rolled client
			// aimed at another org's tenant. Field 2 is reserved in the proto.
			"tenantId": "globex",
		}
		resp := postConnect("/shepherd.mgmt.v1.TenantRouteService/CreateTenantRoute", body, admin)
		Expect(resp.StatusCode).To(Equal(http.StatusOK))
		created := decodeBody(resp)
		Expect(created["tenantId"]).To(Equal("acme"),
			"the route was minted with a caller-supplied tenant identity — an org admin could stamp "+
				"another org's tenant on their own traffic")
		Expect(created["segment"]).To(HavePrefix("acme-"),
			"the slug is derived from the tenant too, so it must come from the same place")
	})

	It("refuses to create a route naming no tenant for an org that has no default tenant", func() {
		noTenant, err := st.Queries.CreateOrg(ctx, sqlc.CreateOrgParams{
			Name: "org-without-tenant", DisplayName: "No Tenant", AdminGroupID: "no-tenant-admin-group",
		})
		Expect(err).NotTo(HaveOccurred())
		admin := createSession(false, []string{"no-tenant-admin-group"})

		resp := postConnect("/shepherd.mgmt.v1.TenantRouteService/CreateTenantRoute", map[string]any{
			"orgId": noTenant.ID.String(), "kind": "otlp",
			"gatewayMode": "managed", "gatewayName": "shepherd-receiver-gw",
		}, admin)
		// Connect maps FailedPrecondition to HTTP 400, so the code in the body
		// is the precise signal — a 400 alone would not distinguish this from
		// an ordinary validation error.
		Expect(resp.StatusCode).To(Equal(http.StatusBadRequest))
		body := decodeBody(resp)
		code, _ := body["code"].(string)   //nolint:errcheck // asserted below
		msg, _ := body["message"].(string) //nolint:errcheck // asserted below
		Expect(code).To(Equal("failed_precondition"),
			"a route for a tenant-less org would have no value to inject; that is a precondition "+
				"failure the operator can fix, not a malformed request")
		Expect(msg).To(ContainSubstring("default tenant"),
			"the refusal must say what is missing")
		Expect(msg).To(ContainSubstring("Admin → Organisations"),
			"the refusal must name where an app admin fixes it")
	})

	// M6 (2026-10-08 walkthrough): "Bad Name!" was accepted and stored, and a
	// route naming a Gateway that cannot exist renders, applies, and never
	// attaches. Gateway names are Kubernetes object names (DNS-1123
	// subdomain), namespaces DNS-1123 labels -- internal/gateway owns the rule.
	DescribeTable("refuses a gateway reference no Kubernetes object could have",
		func(name, namespace, wantMsg string) {
			admin := createSession(false, []string{"tenant-route-admin-group"})
			resp := postConnect("/shepherd.mgmt.v1.TenantRouteService/CreateTenantRoute", map[string]any{
				"orgId": orgID.String(), "kind": "otlp", "gatewayMode": "operator",
				"gatewayName": name, "gatewayNamespace": namespace,
			}, admin)
			Expect(resp.StatusCode).To(Equal(http.StatusBadRequest))
			body := decodeBody(resp)
			Expect(body["code"]).To(Equal("invalid_argument"))
			Expect(body["message"]).To(HavePrefix(wantMsg))

			routes, err := st.Queries.ListTenantRoutesByOrg(ctx, orgID)
			Expect(err).NotTo(HaveOccurred())
			Expect(routes).To(BeEmpty(), "a refused route must not be stored")
		},
		Entry("spaces and punctuation in the name", "Bad Name!", "", "gateway name \"Bad Name!\" is not a valid Kubernetes object name"),
		Entry("upper case in the name", "Edge", "", "gateway name \"Edge\" is not a valid Kubernetes object name"),
		Entry("a name over 253 characters", strings.Repeat("a", 254), "", "gateway name"),
		Entry("a dotted namespace", "edge", "gateway.system", "gateway namespace \"gateway.system\" is not a valid Kubernetes namespace"),
		Entry("an underscore in the namespace", "edge", "gateway_system", "gateway namespace \"gateway_system\" is not a valid Kubernetes namespace"),
	)

	It("accepts a dotted gateway name and a DNS-label namespace", func() {
		admin := createSession(false, []string{"tenant-route-admin-group"})
		resp := postConnect("/shepherd.mgmt.v1.TenantRouteService/CreateTenantRoute", map[string]any{
			"orgId": orgID.String(), "kind": "otlp", "gatewayMode": "operator",
			"gatewayName": "edge.gw-1", "gatewayNamespace": "gateway-system",
		}, admin)
		Expect(resp.StatusCode).To(Equal(http.StatusOK))
		created := decodeBody(resp)
		Expect(created["gatewayName"]).To(Equal("edge.gw-1"))
		Expect(created["gatewayNamespace"]).To(Equal("gateway-system"))
	})
})
