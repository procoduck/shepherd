package agentapi_test

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"time"

	"connectrpc.com/connect"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"

	collectorv1 "shepherd/gen/collector/v1"
	"shepherd/gen/collector/v1/collectorv1connect"
	"shepherd/internal/agentapi"
	"shepherd/internal/config"
	"shepherd/internal/serve"
	"shepherd/internal/store"
	"shepherd/internal/store/sqlc"
	"shepherd/internal/validate"
	"shepherd/internal/version"
)

// #375's serve-time half: a collector's instances change after the save that
// validated its config (an older pod joins). The config keeps being served,
// and the poll that hands it out records a version warning on the collector,
// audited once per change, cleared once the old pod is no longer active.
var _ = Describe("Served-config version check (#375)", Label("integration"), func() {
	const inactiveAfter = 10 * time.Minute
	const podMonitor = `prometheus.operator.podmonitors "pods" {
  forward_to = []
  scrape {
    scrape_classic_histograms = true
  }
}
`
	pinned := validate.DisplayVersion(version.AlloySchemaVersion)

	var (
		st     *store.Store
		svc    *agentapi.Service
		server *httptest.Server
		org    sqlc.Org
		client collectorv1connect.CollectorServiceClient
	)

	BeforeEach(func(ctx context.Context) {
		var err error
		st, err = store.New(ctx, &config.DatabaseConfig{URL: sharedPG.IsolatedDB(ctx, GinkgoTB()), MaxConns: 10})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(st.Close)

		svc = agentapi.New(st, nil, slog.Default(), testSchemaRegistry(), agentapi.WithInactiveAfter(inactiveAfter))
		path, handler := agentapi.NewHandler(svc, connect.WithRequestGate(agentapi.NewAuthGate(st, nil)))
		mux := http.NewServeMux()
		mux.Handle(path, handler)
		server = httptest.NewServer(h2c.NewHandler(mux, &http2.Server{}))
		DeferCleanup(server.Close)

		org, err = st.Queries.CreateOrg(ctx, sqlc.CreateOrgParams{Name: "ver", DisplayName: "Ver", AdminGroupID: "g"})
		Expect(err).NotTo(HaveOccurred())
		cl, err := st.Queries.UpsertCluster(ctx, "prod")
		Expect(err).NotTo(HaveOccurred())
		Expect(st.Queries.ClaimCluster(ctx, sqlc.ClaimClusterParams{ID: cl.ID, OrgID: org.ID})).To(Succeed())
		_, err = st.Queries.CreatePipeline(ctx, sqlc.CreatePipelineParams{
			OrgID: org.ID, Name: "pods", Contents: podMonitor, Matchers: json.RawMessage(`["role=\"metrics\""]`),
			Enabled: true, Source: "ui", WizardState: json.RawMessage(`{}`), CreatedBy: "test", UpdatedBy: "test",
		})
		Expect(err).NotTo(HaveOccurred())

		raw := make([]byte, 32)
		_, _ = rand.Read(raw)
		secret := base64.URLEncoding.EncodeToString(raw)
		hash := sha256.Sum256([]byte(secret))
		tok, err := st.Queries.CreateAgentToken(ctx, sqlc.CreateAgentTokenParams{
			Name: "t", TokenHash: hash[:], CreatedBy: "test", OrgID: org.ID,
			Clusters: json.RawMessage(`[]`), Roles: json.RawMessage(`[]`), MayClaim: true,
		})
		Expect(err).NotTo(HaveOccurred())
		authz := "Basic " + base64.StdEncoding.EncodeToString([]byte(tok.ID.String()+":"+secret))
		client = collectorv1connect.NewCollectorServiceClient(server.Client(), server.URL, connect.WithGRPC(),
			connect.WithInterceptors(connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
				return func(c context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
					req.Header().Set("Authorization", authz)
					return next(c, req)
				}
			})))
	})

	poll := func(ctx context.Context, id, alloyVersion string) *collectorv1.GetConfigResponse {
		resp, err := client.GetConfig(ctx, connect.NewRequest(&collectorv1.GetConfigRequest{
			Id: id, LocalAttributes: map[string]string{"cluster": "prod", "role": "metrics", "collector.version": alloyVersion},
		}))
		Expect(err).NotTo(HaveOccurred())
		return resp.Msg
	}
	collector := func(ctx context.Context) sqlc.Collector {
		c, err := st.Queries.GetCollectorByClusterAndRole(ctx, sqlc.GetCollectorByClusterAndRoleParams{Name: "prod", Role: "metrics"})
		Expect(err).NotTo(HaveOccurred())
		return c
	}
	audits := func(ctx context.Context) int32 {
		n, err := st.Queries.CountAuditLog(ctx, sqlc.CountAuditLogParams{
			Column1: org.ID, Column3: serve.AuditActionVersionWarning,
		})
		Expect(err).NotTo(HaveOccurred())
		return n
	}
	// RAW-SQL-OK: backdating last_seen makes an instance inactive — no sqlc
	// query takes an explicit last_seen, and this is a _test.go file.
	backdate := func(ctx context.Context, id string, ago time.Duration) {
		_, err := st.Pool().Exec(ctx,
			`UPDATE collector_instances SET last_seen = now() - make_interval(secs => $2) WHERE id = $1`,
			id, ago.Seconds())
		Expect(err).NotTo(HaveOccurred())
	}

	It("records nothing for a fleet on the pinned version", func(ctx context.Context) {
		served := poll(ctx, "new-0", pinned)
		Expect(served.GetContent()).To(ContainSubstring("scrape_classic_histograms"))
		c := collector(ctx)
		Expect(c.VersionWarning.Valid).To(BeFalse())
		Expect(c.VersionCheckedHash.String).To(Equal(served.GetHash()))
		Expect(c.VersionCheckedVersions).To(Equal([]string{pinned}))
		Expect(audits(ctx)).To(BeZero())
	})

	It("flags an older pod that joins after the save, keeps serving, audits once, and clears when it leaves", func(ctx context.Context) {
		agentapi.SetVersionNewWindow(svc, 0)
		first := poll(ctx, "new-0", pinned)

		// An older pod joins: it is served the same config, unchanged.
		old := poll(ctx, "old-0", "v1.18.1")
		Expect(old.GetHash()).To(Equal(first.GetHash()), "serving never changes over a version mismatch")
		c := collector(ctx)
		Expect(c.VersionWarning.Valid).To(BeTrue())
		Expect(c.VersionWarning.String).To(ContainSubstring("attribute scrape_classic_histograms"))
		Expect(c.VersionWarning.String).To(ContainSubstring("v1.18.1"))
		Expect(c.VersionMismatch).To(BeTrue())
		Expect(c.VersionWarningAt.Valid).To(BeTrue())
		Expect(audits(ctx)).To(Equal(int32(1)))

		// Steady polls from both pods add nothing, re-checked or not.
		for range 3 {
			poll(ctx, "old-0", "v1.18.1")
			poll(ctx, "new-0", pinned)
		}
		agentapi.SetVersionRecheck(svc, 0)
		poll(ctx, "new-0", pinned)
		Expect(audits(ctx)).To(Equal(int32(1)), "one audit row per change, not per poll")

		// The old pod goes inactive: the next re-check clears the warning.
		backdate(ctx, "old-0", 2*inactiveAfter)
		poll(ctx, "new-0", pinned)
		c = collector(ctx)
		Expect(c.VersionWarning.Valid).To(BeFalse())
		Expect(c.VersionMismatch).To(BeFalse())
		Expect(c.VersionWarningAt.Valid).To(BeFalse())
		Expect(audits(ctx)).To(Equal(int32(2)), "the clearing is audited once")
		poll(ctx, "new-0", pinned)
		Expect(audits(ctx)).To(Equal(int32(2)))
	})

	// 0.21 walkthrough: disabling an unrelated pipeline moved the finding
	// from served line 487 to 486 and wrote a second audit row. A finding is
	// the same finding wherever it sits; the stored text still follows the
	// line, so what the collector page shows stays right.
	It("does not audit a finding again when only its line in the served config moved", func(ctx context.Context) {
		agentapi.SetVersionNewWindow(svc, 0)
		agentapi.SetVersionRecheck(svc, 0) // a mismatch is re-checked on every poll
		poll(ctx, "new-0", pinned)
		before := poll(ctx, "old-0", "v1.18.1")
		Expect(audits(ctx)).To(Equal(int32(1)))
		firstWarning := collector(ctx).VersionWarning.String
		since := collector(ctx).VersionWarningAt
		Expect(firstWarning).To(MatchRegexp(`^line \d+: attribute scrape_classic_histograms`))

		// A pipeline merged ahead of "pods" shifts every line after it.
		_, err := st.Queries.CreatePipeline(ctx, sqlc.CreatePipelineParams{
			OrgID: org.ID, Name: "aaa-shift", Contents: "// one\n// two\n// three\n", Matchers: json.RawMessage(`["role=\"metrics\""]`),
			Enabled: true, Source: "ui", WizardState: json.RawMessage(`{}`), CreatedBy: "test", UpdatedBy: "test",
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(st.Queries.MarkServeCacheDirtyByOrg(ctx, org.ID)).To(Succeed())
		after := poll(ctx, "old-0", "v1.18.1")
		Expect(after.GetHash()).NotTo(Equal(before.GetHash()), "the served config changed")

		c := collector(ctx)
		Expect(c.VersionWarning.String).NotTo(Equal(firstWarning), "the shown line follows the served config")
		Expect(c.VersionWarning.String).To(ContainSubstring("attribute scrape_classic_histograms"))
		Expect(audits(ctx)).To(Equal(int32(1)), "a moved line is not a new finding")
		Expect(c.VersionWarningAt).To(Equal(since), "the warning has stood since it first appeared")
	})

	It("skips the check on a steady poll and re-runs it when a new version polls", func(ctx context.Context) {
		agentapi.SetVersionNewWindow(svc, 0)
		poll(ctx, "new-0", pinned)
		checked := collector(ctx).VersionCheckedAt
		poll(ctx, "new-0", pinned)
		Expect(collector(ctx).VersionCheckedAt).To(Equal(checked), "a steady poll does not re-run the check")

		poll(ctx, "other-0", "v9.0.0")
		c := collector(ctx)
		Expect(c.VersionCheckedAt).NotTo(Equal(checked))
		Expect(c.VersionCheckedVersions).To(ConsistOf(pinned, "v9.0.0"))
		Expect(c.VersionWarning.String).To(ContainSubstring("runs Alloy 9.0.0, newer than every shipped schema"))
		Expect(c.VersionMismatch).To(BeFalse(), "a fallback is a note, not a mismatch")
	})

	It("audits a flapping collector.version at most once per window", func(ctx context.Context) {
		poll(ctx, "new-0", pinned)
		for i := range 20 {
			poll(ctx, "flappy", fmt.Sprintf("v9.9.%d", i))
		}
		Expect(audits(ctx)).To(BeNumerically("<=", 1), "an agent-chosen version must not buy an audit row per poll")
		c := collector(ctx)
		Expect(len(c.VersionCheckedVersions)).To(BeNumerically("<=", 2))
	})

	It("does not re-check a note on time alone, and re-checks it once a checked version can have aged out", func(ctx context.Context) {
		agentapi.SetVersionNewWindow(svc, 0)
		agentapi.SetVersionRecheck(svc, 0)
		poll(ctx, "new-0", pinned)
		poll(ctx, "future-0", "v9.0.0")
		c := collector(ctx)
		Expect(c.VersionWarning.Valid).To(BeTrue())
		Expect(c.VersionMismatch).To(BeFalse())
		Expect(c.VersionCheckedUntil.Valid).To(BeTrue(), "a standing warning records when a re-check can clear it")
		checked := c.VersionCheckedAt

		poll(ctx, "new-0", pinned)
		Expect(collector(ctx).VersionCheckedAt).To(Equal(checked), "a note is not re-checked on time alone")

		// The future pod ages out, and its version with it.
		backdate(ctx, "future-0", 2*inactiveAfter)
		// RAW-SQL-OK: move version_checked_until into the past, as time
		// would — no sqlc query sets it directly, and this is a _test.go file.
		_, err := st.Pool().Exec(ctx, `UPDATE collectors SET version_checked_until = now() - interval '1 second'`)
		Expect(err).NotTo(HaveOccurred())
		poll(ctx, "new-0", pinned)
		c = collector(ctx)
		Expect(c.VersionWarning.Valid).To(BeFalse())
		Expect(c.VersionCheckedUntil.Valid).To(BeFalse())
	})

	// auditDetails is every version-warning audit row's detail, oldest first.
	auditDetails := func(ctx context.Context) []string {
		// RAW-SQL-OK: reading the audit rows' detail for a size assertion — this is a _test.go file
		rows, err := st.Pool().Query(ctx, `SELECT detail::text FROM audit_log WHERE action = $1 ORDER BY id`, serve.AuditActionVersionWarning)
		Expect(err).NotTo(HaveOccurred())
		defer rows.Close()
		var out []string
		for rows.Next() {
			var d string
			Expect(rows.Scan(&d)).To(Succeed())
			out = append(out, d)
		}
		Expect(rows.Err()).NotTo(HaveOccurred())
		return out
	}

	// v0.20.0 follow-up: collector.version is agent-chosen, up to the agent
	// API's request limit. Junk is an unknown version (a note against the
	// pinned schema); it must not break the poll or reach the warning, the
	// audit row or the recorded fingerprint unbounded.
	It("records a junk collector.version as an unknown version, bounded everywhere it is written", func(ctx context.Context) {
		agentapi.SetVersionNewWindow(svc, 0)
		poll(ctx, "new-0", pinned)
		huge := "v1.21.2-" + strings.Repeat("x", 512<<10)
		for i, v := range []string{
			"latest", "v", "1.2", "v1.21.2-foo+bar", "ｖ１.２１.２", `v1"; DROP TABLE collectors; --`,
			"v1.21.2\nline 1: forged", huge,
		} {
			id := fmt.Sprintf("junk-%d", i)
			served := poll(ctx, id, v)
			Expect(served.GetContent()).To(ContainSubstring("scrape_classic_histograms"), "%q is served as before", v)
			c := collector(ctx)
			Expect(c.VersionMismatch).To(BeFalse(), "%q is a note, not a mismatch", v)
			Expect(c.VersionWarning.String).To(ContainSubstring("which is not a release version"))
			Expect(len(c.VersionWarning.String)).To(BeNumerically("<", 1024))
			Expect(c.VersionWarning.String).NotTo(ContainSubstring("DROP TABLE"))
			Expect(c.VersionWarning.String).NotTo(MatchRegexp(`(?m)^line 1: forged`), "a version cannot forge a finding line")
			for _, cv := range c.VersionCheckedVersions {
				Expect(len(cv)).To(BeNumerically("<=", validate.MaxRecordedVersionLen), "the fingerprint holds no unbounded version")
			}
			backdate(ctx, id, 2*inactiveAfter) // one junk instance at a time
		}
		for _, d := range auditDetails(ctx) {
			Expect(len(d)).To(BeNumerically("<", 8<<10), "an audit row never carries unbounded agent input")
			Expect(d).NotTo(ContainSubstring("DROP TABLE"))
		}

		// The same huge version polled again is not a new version: no re-check.
		agentapi.SetVersionNewWindow(svc, time.Hour)
		poll(ctx, "junk-huge", huge)
		checked := collector(ctx).VersionCheckedAt
		poll(ctx, "junk-huge", huge)
		Expect(collector(ctx).VersionCheckedAt).To(Equal(checked))
	})

	It("answers a collector.version with a NUL byte without an internal error", func(ctx context.Context) {
		_, err := client.GetConfig(ctx, connect.NewRequest(&collectorv1.GetConfigRequest{
			Id: "nul-0", LocalAttributes: map[string]string{"cluster": "prod", "role": "metrics", "collector.version": "v1.21.2\x00truncated:5"},
		}))
		if err != nil {
			Expect(connect.CodeOf(err)).To(Equal(connect.CodeInvalidArgument), "an agent's bad input is not a server fault: %v", err)
		}
	})

	// An instance whose reported version changes on successive polls: the
	// warning follows the actual state, audited once per change of it, and
	// a change of which unknown version is quoted is no change of state.
	It("audits a version flapping between two states once per actual change, and not for flapping junk", func(ctx context.Context) {
		agentapi.SetVersionNewWindow(svc, 0) // every new version re-checks: the window is not what bounds this
		agentapi.SetVersionRecheck(svc, 0)   // and a standing mismatch re-checks on every poll
		poll(ctx, "new-0", pinned)
		poll(ctx, "flap", "v1.18.1")
		Expect(audits(ctx)).To(Equal(int32(1)))
		for range 3 {
			poll(ctx, "flap", "v1.18.1") // steady: nothing
		}
		Expect(audits(ctx)).To(Equal(int32(1)))
		poll(ctx, "flap", pinned) // cleared
		poll(ctx, "flap", pinned)
		Expect(audits(ctx)).To(Equal(int32(2)))
		poll(ctx, "flap", "v1.18.1") // back
		Expect(audits(ctx)).To(Equal(int32(3)), "each actual change is audited once")
		Expect(collector(ctx).VersionMismatch).To(BeTrue())

		poll(ctx, "flap", pinned)
		Expect(audits(ctx)).To(Equal(int32(4)))
		poll(ctx, "flap", "nightly-1")
		Expect(audits(ctx)).To(Equal(int32(5)), "a first unknown version is a change")
		since := collector(ctx).VersionWarningAt
		for i := 2; i < 12; i++ {
			poll(ctx, "flap", fmt.Sprintf("nightly-%d", i))
		}
		c := collector(ctx)
		Expect(audits(ctx)).To(Equal(int32(5)), "another unknown version in its place is the same state")
		Expect(c.VersionWarning.String).To(ContainSubstring(`"nightly-11"`), "the stored text still quotes the current one")
		Expect(c.VersionWarningAt).To(Equal(since))
		Expect(len(c.VersionCheckedVersions)).To(BeNumerically("<=", 2))
	})

	It("holds the per-collector version cap in the recorded fingerprint and the audit row", func(ctx context.Context) {
		agentapi.SetVersionNewWindow(svc, time.Hour) // the instances below register without re-checking each
		for i := range validate.MaxReportedVersions + 20 {
			poll(ctx, fmt.Sprintf("many-%d", i), fmt.Sprintf("v9.%d.0", i))
		}
		agentapi.SetVersionNewWindow(svc, 0)
		poll(ctx, "many-last", "v1.18.1")
		c := collector(ctx)
		Expect(len(c.VersionCheckedVersions)).To(BeNumerically("<=", validate.MaxReportedVersions))
		Expect(c.VersionCheckedVersions).To(ContainElement("v1.18.1"), "the oldest release is never the one dropped")
		Expect(c.VersionWarning.String).To(ContainSubstring("further reported versions were not checked"))
		details := auditDetails(ctx)
		Expect(details).NotTo(BeEmpty())
		var detail struct {
			Versions      []string `json:"versions"`
			VersionsTotal int      `json:"versions_total"`
		}
		Expect(json.Unmarshal([]byte(details[len(details)-1]), &detail)).To(Succeed())
		Expect(len(detail.Versions)).To(BeNumerically("<=", validate.MaxReportedVersions))
		Expect(detail.VersionsTotal).To(Equal(validate.MaxReportedVersions + 21))
	})

	It("skips the fingerprint UPDATE when a re-check finds what the last one did", func(ctx context.Context) {
		agentapi.SetVersionNewWindow(svc, 0)
		poll(ctx, "new-0", pinned)
		poll(ctx, "old-0", "v1.18.1")
		agentapi.SetVersionRecheck(svc, 0) // a mismatch is re-checked on every poll now
		checked := collector(ctx).VersionCheckedAt
		poll(ctx, "new-0", pinned)
		poll(ctx, "old-0", "v1.18.1")
		Expect(collector(ctx).VersionCheckedAt).To(Equal(checked), "nothing changed, nothing written")
	})
})
