package cli

// Developer-only database fixtures. These commands are intentionally grouped
// under "dev" and must not be used against a production database.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/go-git/go-billy/v6/memfs"
	billyutil "github.com/go-git/go-billy/v6/util"
	git "github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing/client"
	"github.com/go-git/go-git/v6/plumbing/object"
	xhttp "github.com/go-git/go-git/v6/plumbing/transport/http"
	"github.com/go-git/go-git/v6/storage/memory"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/spf13/cobra"

	"shepherd/internal/config"
	"shepherd/internal/crypto"
	"shepherd/internal/store"
	"shepherd/internal/store/sqlc"
)

const (
	seedOrgPlatformID         = "11111111-aaaa-4000-8000-000000000001"
	seedOrgDataEngID          = "11111111-aaaa-4000-8000-000000000002"
	seedPlatformAdminGroupID  = "22222222-aaaa-4000-8000-000000000001"
	seedPlatformReaderGroupID = "22222222-aaaa-4000-8000-000000000002"
	seedDataEngAdminGroupID   = "22222222-aaaa-4000-8000-000000000003"
	seedAgentTokenID          = "00000000-de00-4000-a000-000000000001" //nolint:gosec // G101: deterministic dev fixture UUID, not a credential
	seedAgentTokenSecret      = "dev-only-agent-secret-32byteslong"    //nolint:gosec // deterministic development fixture
	seedClusterPlatformName   = "prod-eu-1"
	seedClusterStagingName    = "staging-eu-1"
	seedClusterDataEngName    = "data-eng-eu-1"
	seedDestProdPromURL       = "https://prometheus.example.com/api/v1/write"
	seedDestProdLokiURL       = "https://loki.example.com/loki/api/v1/push"
)

// baseMetricsTemplate is a complete, self-contained scrape->remote_write chain:
// it scrapes Shepherd's own /metrics via prometheus.exporter.self and forwards
// to the seeded "prom-prod" destination. It is stage-1 (and, given a real
// alloy binary, stage-2) valid on its own so the dev stack serves a working
// config instead of an empty one. %q verbs are filled with the cluster name
// and destination URL so the two stay in sync with the rest of the seed.
const baseMetricsTemplate = `prometheus.exporter.self "seed" { }

prometheus.scrape "seed" {
  targets    = prometheus.exporter.self.seed.targets
  forward_to = [prometheus.remote_write.seed.receiver]
}

prometheus.remote_write "seed" {
  external_labels = {
    cluster = %q,
  }

  endpoint {
    url = %q
  }
}
`

// demoVisualGraph is a valid alloy-graph/v1 document. Every edge runs the way
// the canvas draws it — source on the left, destination on the right (D1) — and
// every port name must exist in the schema artifact, or the edge is silently
// dropped: React Flow never creates it, and visual.Render reports
// edge_unresolved. Note that the two hops are opposite kinds:
//
//   - discovery.kubernetes EXPORTS targets (a data export, role "produces"), so
//     the reference is emitted in the consumer: scrape { targets = ...pods.targets }.
//   - prometheus.remote_write EXPORTS receiver (a receiver export, role
//     "accepts"), so the reference is emitted in the producer:
//     scrape { forward_to = [...demo.receiver] }.
//
// Graph: discovery.kubernetes -> prometheus.scrape -> prometheus.remote_write —
// adapted from the smallest suitable corpus fixture
// (internal/visual/testdata/corpus/minimal-scrape.graph.json) with node labels
// changed ("k8s"/"app"/"sink" -> "pods"/"demo"/"demo") so it reads as its own
// example rather than a copy of that fixture. It seeds the demo-visual
// pipeline's wizard_state so opening the visual builder shows a real, editable
// graph instead of an empty canvas (D1/R3-H5).
const demoVisualGraph = `{"kind":"alloy-graph/v1","schema_version":"alloy-v1.18.1","nodes":[{"id":"n1","component":"discovery.kubernetes","label":"pods","position":{"x":40,"y":80},"props":{"role":"pod"},"disabled":false,"notes":""},{"id":"n2","component":"prometheus.scrape","label":"demo","position":{"x":420,"y":80},"props":{"job_name":"demo","scrape_interval":"30s"},"disabled":false,"notes":""},{"id":"n3","component":"prometheus.remote_write","label":"demo","position":{"x":800,"y":80},"props":{"endpoint":[{"url":"` + seedDestProdPromURL + `"}]},"disabled":false,"notes":""}],"edges":[{"id":"e1","from":{"node":"n1","port":"targets"},"to":{"node":"n2","port":"targets"}},{"id":"e2","from":{"node":"n2","port":"forward_to"},"to":{"node":"n3","port":"receiver"}}],"bindings":[],"viewport":{"x":0,"y":0,"zoom":1},"meta":{"created_with":"shepherd-dev-seed"}}`

// demoVisualContents is the exact render of demoVisualGraph produced by
// visual.Render against the SHIPPED schema (internal/schema's embedded artifact
// merged with the overlay) — not against a test fixture, which is how this
// constant previously drifted into a config that referenced an export Alloy does
// not have. Keeping them in lockstep matters because
// PipelineService.checkVisualRenderMatch re-renders wizard_state with the real
// registry and reports a mismatch as drift on every freshly seeded environment.
const demoVisualContents = `// generated by shepherd visual builder — do not edit by hand (edits will be overwritten); graph revision 3, schema v1.18.1

discovery.kubernetes "pods" {
  role = "pod"
}

prometheus.scrape "demo" {
  targets = [discovery.kubernetes.pods.targets]
  forward_to = [prometheus.remote_write.demo.receiver]
  job_name = "demo"
  scrape_interval = "30s"
}

prometheus.remote_write "demo" {
  endpoint {
    url = "` + seedDestProdPromURL + `"
  }
}
`

var (
	devCmd              = &cobra.Command{Use: "dev", Short: "Developer tooling (direct DB access — never use in production)"}
	devSeedCmd          = &cobra.Command{Use: "seed", Short: "Seed development fixture data (idempotent)", RunE: runDevSeed}
	devCreateSessionCmd = &cobra.Command{Use: "create-session", Short: "Mint a dev session for persona testing (direct DB — never production)", RunE: runDevCreateSession}
)

func init() {
	devCreateSessionCmd.Flags().String("persona", "appadmin", "persona: appadmin|orgadmin-platform|reader-platform|nobody")
	devCreateSessionCmd.Flags().Duration("ttl", time.Hour, "session TTL")
	devCmd.AddCommand(devSeedCmd, devCreateSessionCmd)
	rootCmd.AddCommand(devCmd)
}

func runDevSeed(cmd *cobra.Command, _ []string) error {
	cfg, err := config.Load(cfgFile)
	if err != nil {
		return err
	}
	st, err := store.New(cmd.Context(), &cfg.Database)
	if err != nil {
		return fmt.Errorf("connecting: %w", err)
	}
	defer st.Close()
	ctx := cmd.Context()
	platform, err := upsertSeedOrg(ctx, st, seedOrgPlatformID, "platform-org", "Platform Engineering", seedPlatformAdminGroupID, seedPlatformReaderGroupID)
	if err != nil {
		return fmt.Errorf("creating platform org: %w", err)
	}
	dataEng, err := upsertSeedOrg(ctx, st, seedOrgDataEngID, "data-eng", "Data Engineering", seedDataEngAdminGroupID, "")
	if err != nil {
		return fmt.Errorf("creating data-eng org: %w", err)
	}
	fmt.Printf("orgs: platform-org(%s), data-eng(%s)\n", platform.ID, dataEng.ID)

	prod, err := st.Queries.UpsertCluster(ctx, seedClusterPlatformName)
	if err != nil {
		return fmt.Errorf("upserting prod cluster: %w", err)
	}
	staging, err := st.Queries.UpsertCluster(ctx, seedClusterStagingName)
	if err != nil {
		return fmt.Errorf("upserting staging cluster: %w", err)
	}
	if !prod.OrgID.Valid {
		if err := st.Queries.ClaimCluster(ctx, sqlc.ClaimClusterParams{ID: prod.ID, OrgID: platform.ID}); err != nil {
			return fmt.Errorf("claiming prod cluster: %w", err)
		}
	}
	if !staging.OrgID.Valid {
		if err := st.Queries.ClaimCluster(ctx, sqlc.ClaimClusterParams{ID: staging.ID, OrgID: platform.ID}); err != nil {
			return fmt.Errorf("claiming staging cluster: %w", err)
		}
	}

	// data-eng starts with its own claimed cluster + collector so the org
	// (which sorts first alphabetically in the UI) is never blank on first
	// login — see R3-H1. No collector_instances are seeded here (see below);
	// nothing in the compose stack registers against it, so it legitimately
	// shows zero live instances until someone points an agent at it.
	dataEngCluster, err := st.Queries.UpsertCluster(ctx, seedClusterDataEngName)
	if err != nil {
		return fmt.Errorf("upserting data-eng cluster: %w", err)
	}
	if !dataEngCluster.OrgID.Valid {
		if err := st.Queries.ClaimCluster(ctx, sqlc.ClaimClusterParams{ID: dataEngCluster.ID, OrgID: dataEng.ID}); err != nil {
			return fmt.Errorf("claiming data-eng cluster: %w", err)
		}
	}
	if _, err := st.Queries.UpsertCollector(ctx, sqlc.UpsertCollectorParams{ClusterID: dataEngCluster.ID, Role: "metrics"}); err != nil {
		return fmt.Errorf("upserting data-eng collector: %w", err)
	}
	// Both prod-eu-1 and staging-eu-1 are claimed by platform-org above, so
	// say so — a prior version of this message printed a hardcoded
	// "(unclaimed)" for staging even though it was claimed (R3-M1).
	fmt.Printf("clusters: %s (claimed by platform-org), %s (claimed by platform-org), %s (claimed by data-eng)\n", prod.Name, staging.Name, dataEngCluster.Name)

	// Collector rows only — no stub collector_instances. Real Alloy
	// registrations come from the compose stack's alloy-metrics/alloy-logs/
	// alloy-staging containers; seeding fake instances here used to sit
	// alongside them and confuse status (R3-H2). The "singleton" collector
	// has no compose container backing it, so it will show zero instances
	// until something registers against it — that's expected, not stale.
	var prodMetricsCollector sqlc.Collector
	for _, role := range []string{"metrics", "logs", "singleton"} {
		c, err := st.Queries.UpsertCollector(ctx, sqlc.UpsertCollectorParams{ClusterID: prod.ID, Role: role})
		if err != nil {
			return fmt.Errorf("upserting %s collector: %w", role, err)
		}
		if role == "metrics" {
			prodMetricsCollector = c
		}
	}
	fmt.Println("collectors: metrics, logs, singleton (prod-eu-1); metrics (data-eng-eu-1) — instances register themselves")

	if err := seedDestinations(ctx, st, platform.ID); err != nil {
		return fmt.Errorf("seeding destinations: %w", err)
	}
	if err := seedPipelines(ctx, st, platform.ID, platformPipelineItems()); err != nil {
		return fmt.Errorf("seeding platform pipelines: %w", err)
	}
	if err := seedPipelines(ctx, st, dataEng.ID, dataEngPipelineItems()); err != nil {
		return fmt.Errorf("seeding data-eng pipelines: %w", err)
	}
	if err := seedGitOps(ctx, st, cfg, platform.ID, prodMetricsCollector.ID); err != nil {
		// Best-effort: Gitea living in the same compose stack (F9,
		// docs/git-provider-design.md §4) is a nice-to-have for GitPage to
		// have something real to show, not something the rest of dev
		// seeding should die over — a Gitea hiccup here must not stop
		// shepherd itself from starting.
		fmt.Printf("gitops seed: skipped (%v)\n", err)
	}
	if err := seedAgentToken(ctx, st); err != nil {
		return fmt.Errorf("seeding agent token: %w", err)
	}
	fmt.Println("seed complete")
	return nil
}

func upsertSeedOrg(ctx context.Context, st *store.Store, id, name, displayName, adminGroup, readerGroup string) (sqlc.Org, error) {
	var org sqlc.Org
	var reader any
	if readerGroup != "" {
		reader = readerGroup
	}
	err := st.Pool().QueryRow(ctx, `INSERT INTO orgs (id, name, display_name, admin_group_id, reader_group_id) VALUES ($1, $2, $3, $4, $5) ON CONFLICT (name) DO UPDATE SET display_name=EXCLUDED.display_name, admin_group_id=EXCLUDED.admin_group_id, reader_group_id=EXCLUDED.reader_group_id RETURNING id, name, display_name, admin_group_id, reader_group_id, created_at, updated_at`, id, name, displayName, adminGroup, reader).Scan(&org.ID, &org.Name, &org.DisplayName, &org.AdminGroupID, &org.ReaderGroupID, &org.CreatedAt, &org.UpdatedAt)
	return org, err
}

// seedPipelineItem describes one pipeline to seed.
type seedPipelineItem struct {
	name, contents, source string
	matchers               []string
	enabled                bool
	// wizardState is the raw JSON wizard_state document (non-empty only for
	// source="visual"/"wizard" items). Empty means NULL wizard_state.
	wizardState string
}

// platformPipelineItems returns the pipelines seeded for platform-org.
//
// base-metrics carries a complete, self-contained scrape->remote_write chain
// and real matchers (cluster="prod-eu-1", role="metrics") so it actually
// matches the alloy-metrics collector from the compose stack and the dev
// stack serves a working config instead of an empty one (R3-C1/R3-H3).
func platformPipelineItems() []seedPipelineItem {
	return []seedPipelineItem{
		{
			name:     "base-metrics",
			contents: fmt.Sprintf(baseMetricsTemplate, seedClusterPlatformName, seedDestProdPromURL),
			source:   "ui",
			matchers: []string{fmt.Sprintf("cluster=%q", seedClusterPlatformName), `role="metrics"`},
			enabled:  true,
		},
		{
			// demo-visual is source="visual" with a real alloy-graph/v1
			// wizard_state (see demoVisualGraph) so the visual builder opens
			// with an example to explore instead of a blank canvas (D1/
			// R3-H5). It shares base-metrics' matchers (both legitimately
			// match the alloy-metrics collector) but is a distinct pipeline
			// by name and by mechanism — discovery.kubernetes scrape rather
			// than base-metrics' self-exporter — so it doesn't collide with
			// base-metrics' name or purpose.
			name:        "demo-visual",
			contents:    demoVisualContents,
			source:      "visual",
			matchers:    []string{fmt.Sprintf("cluster=%q", seedClusterPlatformName), `role="metrics"`},
			enabled:     true,
			wizardState: demoVisualGraph,
		},
		{
			name:     "loki-logs",
			contents: "loki.source.file \"seed\" {\n  targets    = []\n  forward_to = []\n}\n",
			source:   "ui",
			// Matches the alloy-logs collector, but left disabled since the
			// contents are a placeholder stub (no real forward_to target).
			matchers: []string{fmt.Sprintf("cluster=%q", seedClusterPlatformName), `role="logs"`},
			enabled:  false,
		},
		{
			name:     "app-obs-wizard",
			contents: `// wizard-generated`,
			source:   "wizard",
			// No matchers: this is a stub demonstrating a wizard-sourced
			// pipeline in the UI, not something meant to actually match a
			// collector. Left disabled; producing real contents requires
			// walking the wizard.
			matchers: nil,
			enabled:  false,
		},
	}
}

// dataEngPipelineItems returns the pipelines seeded for the data-eng org, so
// it is not empty on first login (R3-H1).
func dataEngPipelineItems() []seedPipelineItem {
	return []seedPipelineItem{
		{
			name:     "example-metrics",
			contents: `prometheus.exporter.self "seed" { }`,
			source:   "ui",
			matchers: []string{fmt.Sprintf("cluster=%q", seedClusterDataEngName), `role="metrics"`},
			enabled:  false,
		},
	}
}

// seedPipelines creates the given pipelines under orgID, idempotently. For
// each pipeline actually created (not already present), it also writes
// pipeline_revisions revision 1 and an audit_log row, mirroring what
// PipelinesHandler.Create does in internal/mgmtapi/pipelines.go (R3-H4).
func seedPipelines(ctx context.Context, st *store.Store, orgID pgtype.UUID, items []seedPipelineItem) error {
	summary := make([]string, 0, len(items))
	for _, item := range items {
		if _, err := st.Queries.GetPipelineByOrgAndName(ctx, sqlc.GetPipelineByOrgAndNameParams{OrgID: orgID, Name: item.name}); err == nil {
			summary = append(summary, item.name+"(exists)")
			continue
		}
		matchers := item.matchers
		if matchers == nil {
			matchers = []string{}
		}
		matchersJSON, err := json.Marshal(matchers)
		if err != nil {
			return fmt.Errorf("marshaling matchers for %s: %w", item.name, err)
		}
		var wizardState json.RawMessage
		if item.wizardState != "" {
			wizardState = json.RawMessage(item.wizardState)
		}
		p, err := st.Queries.CreatePipeline(ctx, sqlc.CreatePipelineParams{
			OrgID: orgID, Name: item.name, Contents: item.contents, Matchers: matchersJSON,
			Enabled: item.enabled, Source: item.source, WizardState: wizardState,
			CreatedBy: "seed", UpdatedBy: "seed",
		})
		if err != nil {
			if isUnique(err) {
				summary = append(summary, item.name+"(exists)")
				continue
			}
			return fmt.Errorf("creating pipeline %s: %w", item.name, err)
		}
		if _, err := st.Queries.CreatePipelineRevision(ctx, sqlc.CreatePipelineRevisionParams{
			PipelineID: p.ID, Revision: 1, Contents: p.Contents, Matchers: p.Matchers,
			Enabled: p.Enabled, ChangedBy: "seed", ChangeNote: "seed",
		}); err != nil {
			return fmt.Errorf("creating revision 1 for %s: %w", item.name, err)
		}
		if err := st.Queries.InsertAuditLog(ctx, sqlc.InsertAuditLogParams{
			Actor: "seed", ActorType: "user", OrgID: orgID, Action: "pipeline.create",
			ResourceType: "pipeline", ResourceID: p.ID.String(), Detail: json.RawMessage(`{}`),
		}); err != nil {
			return fmt.Errorf("writing audit log for %s: %w", item.name, err)
		}
		state := "disabled"
		if item.enabled {
			state = "enabled"
		}
		summary = append(summary, fmt.Sprintf("%s(%s)", item.name, state))
	}
	fmt.Printf("pipelines: %s\n", strings.Join(summary, ", "))
	return nil
}

func seedDestinations(ctx context.Context, st *store.Store, orgID pgtype.UUID) error {
	for _, item := range []struct{ name, typ, url string }{{"prom-prod", "prometheus", seedDestProdPromURL}, {"loki-prod", "loki", seedDestProdLokiURL}} {
		dests, err := st.Queries.ListDestinationsByOrg(ctx, orgID)
		if err != nil {
			return err
		}
		found := false
		for i := range dests {
			if dests[i].Name == item.name {
				found = true
				break
			}
		}
		if found {
			continue
		}
		if _, err := st.Queries.CreateDestination(ctx, sqlc.CreateDestinationParams{OrgID: orgID, Name: item.name, Type: item.typ, Url: item.url, SecretName: "", SecretNamespace: "", AuthMode: "none", Extra: json.RawMessage(`{}`)}); err != nil && !isUnique(err) {
			return err
		}
	}
	fmt.Println("destinations: prom-prod, loki-prod")
	return nil
}

// Gitea connection details for the dev compose stack
// (dev/docker-compose.dev.yaml). seedGitOps runs inside the shepherd-seed
// container, which is on the compose network — unlike a human at a
// browser, or the e2e suite's test process, it reaches Gitea at its
// internal DNS name, not the host-published port. The admin user/password
// must match gitea-init's `gitea admin user create` command exactly.
const (
	seedGiteaBaseURL   = "http://gitea:3000"
	seedGiteaAdminUser = "shepherd-admin"
	seedGiteaAdminPass = "Sh3pherd-Admin-Pass-1" // fixed dev-only Gitea fixture credential, matches dev/docker-compose.dev.yaml's gitea-init
	seedGitRepoName    = "shepherd-demo-config"
	seedGitCredName    = "gitea-demo"
	seedGitFileName    = "demo-git.alloy"
	seedGitFileContent = `// pushed to Gitea by 'shepherd dev seed' — edit it there and Shepherd will
// pick up the change on its next gitsync poll (docs/git-provider-design.md §4).
prometheus.exporter.self "demo_git" { }
`
)

// seedGitOps wires up a real, working git-sourced pipeline against the dev
// stack's Gitea instance: create a repo, push a fixture .alloy file over
// real git, create a `pat` git credential, and link it to
// prodMetricsCollectorID — so GitPage has something real to show instead
// of an empty state (ledger B2), and the pipeline actually shows up in the
// served config for the same collector base-metrics targets. Idempotent:
// if a credential named seedGitCredName already exists for orgID, this is
// a no-op (matching the check-then-skip style the rest of this file uses)
// rather than re-creating the Gitea repo and re-pushing every restart.
func seedGitOps(ctx context.Context, st *store.Store, cfg *config.Config, orgID, prodMetricsCollectorID pgtype.UUID) error {
	if cfg.Security.EncryptionKey == "" {
		return errors.New("SHEPHERD_SECURITY_ENCRYPTION_KEY not set")
	}
	existing, err := st.Queries.ListGitCredentialsByOrg(ctx, orgID)
	if err != nil {
		return fmt.Errorf("listing existing git credentials: %w", err)
	}
	for i := range existing {
		if existing[i].Name == seedGitCredName {
			fmt.Printf("gitops: %s(exists)\n", seedGitCredName)
			return nil
		}
	}

	branch, repoURL, err := seedGiteaCreateRepo(ctx, seedGitRepoName)
	if err != nil {
		return fmt.Errorf("creating gitea repo: %w", err)
	}
	if err := seedGiteaPushFixture(ctx, repoURL); err != nil {
		return fmt.Errorf("pushing fixture to gitea repo: %w", err)
	}
	token, err := seedGiteaCreateToken(ctx, seedGitRepoName+"-token")
	if err != nil {
		return fmt.Errorf("creating gitea token: %w", err)
	}

	enc, err := crypto.NewEncryptor(cfg.Security.EncryptionKey)
	if err != nil {
		return fmt.Errorf("building encryptor: %w", err)
	}
	tokenEnc, err := enc.Encrypt([]byte(token))
	if err != nil {
		return fmt.Errorf("encrypting token: %w", err)
	}

	cred, err := st.Queries.CreateGitCredential(ctx, sqlc.CreateGitCredentialParams{
		OrgID: orgID, Name: seedGitCredName, Kind: "pat",
		Username:        pgtype.Text{String: seedGiteaAdminUser, Valid: true},
		ClientSecretEnc: tokenEnc,
		ProviderConfig:  []byte("{}"),
	})
	if err != nil {
		return fmt.Errorf("creating git credential: %w", err)
	}

	if _, err := st.Queries.CreateRepoLink(ctx, sqlc.CreateRepoLinkParams{
		OrgID: orgID, CollectorID: prodMetricsCollectorID, CredentialID: cred.ID,
		RepoUrl: repoURL, Branch: branch, Path: "/", PollIntervalSeconds: 60,
	}); err != nil {
		return fmt.Errorf("creating repo link: %w", err)
	}

	fmt.Printf("gitops: pushed %s to %s, linked to prod-eu-1/metrics via credential %q\n", seedGitFileName, repoURL, seedGitCredName)
	return nil
}

// seedGiteaAPI makes one authenticated (admin basic auth) JSON request
// against Gitea's REST API and decodes a 2xx JSON response into out (which
// may be nil).
func seedGiteaAPI(ctx context.Context, method, path string, body []byte, out any) error {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, seedGiteaBaseURL+path, reader)
	if err != nil {
		return err
	}
	req.SetBasicAuth(seedGiteaAdminUser, seedGiteaAdminPass)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer resp.Body.Close() //nolint:errcheck // response fully read or discarded below

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 2048)) //nolint:errcheck // best-effort diagnostic
		return fmt.Errorf("%s %s: status %d: %s", method, path, resp.StatusCode, string(respBody))
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// seedGiteaCreateRepo creates repoName (owned by the admin user) with an
// initial commit (auto_init), returning its default branch and clone URL.
func seedGiteaCreateRepo(ctx context.Context, repoName string) (branch, cloneURL string, err error) {
	body, err := json.Marshal(map[string]any{
		"name": repoName, "private": false, "auto_init": true,
		"default_branch": "main", "description": "shepherd dev seed fixture",
	})
	if err != nil {
		return "", "", fmt.Errorf("marshal create-repo body: %w", err)
	}
	var result struct {
		DefaultBranch string `json:"default_branch"`
	}
	if err := seedGiteaAPI(ctx, http.MethodPost, "/api/v1/user/repos", body, &result); err != nil {
		return "", "", err
	}
	branch = result.DefaultBranch
	if branch == "" {
		branch = "main"
	}
	return branch, fmt.Sprintf("%s/%s/%s.git", seedGiteaBaseURL, seedGiteaAdminUser, repoName), nil
}

// seedGiteaCreateToken mints a personal access token for the admin user.
func seedGiteaCreateToken(ctx context.Context, name string) (string, error) {
	body, err := json.Marshal(map[string]any{"name": name, "scopes": []string{"all"}})
	if err != nil {
		return "", fmt.Errorf("marshal create-token body: %w", err)
	}
	var result struct {
		Sha1 string `json:"sha1"`
	}
	if err := seedGiteaAPI(ctx, http.MethodPost, "/api/v1/users/"+seedGiteaAdminUser+"/tokens", body, &result); err != nil {
		return "", err
	}
	if result.Sha1 == "" {
		return "", errors.New("create token: response had no sha1")
	}
	return result.Sha1, nil
}

// seedGiteaPushFixture clones repoURL (which must already have at least
// one commit, e.g. via auto_init) and pushes seedGitFileName as a new
// commit — a real git push, matching what production does
// (docs/git-provider-design.md §4), not a database row written directly.
func seedGiteaPushFixture(ctx context.Context, repoURL string) error {
	auth := &xhttp.BasicAuth{Username: seedGiteaAdminUser, Password: seedGiteaAdminPass}
	wtfs := memfs.New()
	repo, err := git.CloneContext(ctx, memory.NewStorage(), wtfs, &git.CloneOptions{
		URL:           repoURL,
		ClientOptions: []client.Option{client.WithHTTPAuth(auth)},
	})
	if err != nil {
		return fmt.Errorf("clone %s: %w", repoURL, err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		return fmt.Errorf("worktree: %w", err)
	}
	if err := billyutil.WriteFile(wtfs, seedGitFileName, []byte(seedGitFileContent), 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", seedGitFileName, err)
	}
	if _, err := wt.Add(seedGitFileName); err != nil {
		return fmt.Errorf("staging %s: %w", seedGitFileName, err)
	}
	if _, err := wt.Commit("seed: add "+seedGitFileName, &git.CommitOptions{
		Author: &object.Signature{Name: "shepherd dev seed", Email: "dev-seed@shepherd.local", When: time.Now()},
	}); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	if err := repo.PushContext(ctx, &git.PushOptions{
		ClientOptions: []client.Option{client.WithHTTPAuth(auth)},
	}); err != nil {
		return fmt.Errorf("push: %w", err)
	}
	return nil
}

func seedAgentToken(ctx context.Context, st *store.Store) error {
	var id pgtype.UUID
	if err := id.Scan(seedAgentTokenID); err != nil {
		return err
	}
	hash := sha256.Sum256([]byte(seedAgentTokenSecret))
	_, err := st.Queries.CreateAgentTokenWithID(ctx, sqlc.CreateAgentTokenWithIDParams{ID: id, Name: "dev-alloy-token", TokenHash: hash[:], CreatedBy: "seed"})
	if err != nil && !isUnique(err) {
		return err
	}
	fmt.Printf("agent_token: id=%s secret=%s\n", seedAgentTokenID, seedAgentTokenSecret)
	return nil
}

func isUnique(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

func runDevCreateSession(cmd *cobra.Command, _ []string) error {
	persona, err := cmd.Flags().GetString("persona")
	if err != nil {
		return err
	}
	ttl, err := cmd.Flags().GetDuration("ttl")
	if err != nil {
		return err
	}
	cfg, err := config.Load(cfgFile)
	if err != nil {
		return err
	}
	st, err := store.New(cmd.Context(), &cfg.Database)
	if err != nil {
		return fmt.Errorf("connecting: %w", err)
	}
	defer st.Close()
	type personaDef struct {
		userOID, email, displayName string
		groups                      []string
		appAdmin                    bool
	}
	personas := map[string]personaDef{
		"appadmin":          {"dev:appadmin", "appadmin@dev.local", "Dev App Admin", []string{seedPlatformAdminGroupID}, true},
		"orgadmin-platform": {"dev:orgadmin-platform", "orgadmin@dev.local", "Dev Platform Admin", []string{seedPlatformAdminGroupID}, false},
		"reader-platform":   {"dev:reader-platform", "reader@dev.local", "Dev Reader", []string{seedPlatformReaderGroupID}, false},
		"nobody":            {"dev:nobody", "nobody@dev.local", "Dev Nobody", nil, false},
	}
	p, ok := personas[persona]
	if !ok {
		return fmt.Errorf("unknown persona %q; valid: appadmin, orgadmin-platform, reader-platform, nobody", persona)
	}
	groups, err := json.Marshal(p.groups)
	if err != nil {
		return fmt.Errorf("encoding groups: %w", err)
	}
	id := fmt.Sprintf("dev-%s-%d", persona, time.Now().UnixNano())
	now := time.Now()
	_, err = st.Queries.CreateSession(cmd.Context(), sqlc.CreateSessionParams{ID: id, UserOid: p.userOID, Email: p.email, DisplayName: p.displayName, GroupIds: groups, IsAppAdmin: p.appAdmin, ExpiresAt: pgtype.Timestamptz{Time: now.Add(ttl), Valid: true}, Source: "dev"})
	if err != nil {
		return fmt.Errorf("creating session: %w", err)
	}
	fmt.Printf("session_id=%s persona=%s ttl=%s\n", id, persona, ttl)
	fmt.Println("# Set cookie: shepherd_session=<session_id>")
	return nil
}
