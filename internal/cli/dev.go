package cli

// Developer-only database fixtures. These commands are intentionally grouped
// under "dev" and must not be used against a production database.

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/spf13/cobra"

	"shepherd/internal/config"
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
)

var devCmd = &cobra.Command{Use: "dev", Short: "Developer tooling (direct DB access — never use in production)"}
var devSeedCmd = &cobra.Command{Use: "seed", Short: "Seed development fixture data (idempotent)", RunE: runDevSeed}
var devCreateSessionCmd = &cobra.Command{Use: "create-session", Short: "Mint a dev session for persona testing (direct DB — never production)", RunE: runDevCreateSession}

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
	fmt.Printf("clusters: %s (claimed), %s (unclaimed)\n", prod.Name, staging.Name)

	roles := []struct{ role, id, name, status, failure string }{
		{"metrics", "dev-instance-metrics", "metrics-agent", "RemoteConfigStatuses_APPLIED", ""},
		{"logs", "dev-instance-logs", "logs-agent", "RemoteConfigStatuses_UNSET", ""},
		{"singleton", "dev-instance-singleton", "singleton-agent", "RemoteConfigStatuses_FAILED", "config parse error at line 3"},
	}
	for _, item := range roles {
		collector, err := st.Queries.UpsertCollector(ctx, sqlc.UpsertCollectorParams{ClusterID: prod.ID, Role: item.role})
		if err != nil {
			return fmt.Errorf("upserting %s collector: %w", item.role, err)
		}
		attrs := fmt.Sprintf(`{"cluster":"prod-eu-1","role":%q}`, item.role)
		if item.role == "logs" {
			_, err = st.Pool().Exec(ctx, `INSERT INTO collector_instances (id, collector_id, name, local_attributes, last_seen, remote_config_status) VALUES ($1, $2, $3, $4::jsonb, now() - interval '2 hours', $5) ON CONFLICT (id) DO NOTHING`, item.id, collector.ID, item.name, attrs, item.status)
		} else {
			_, err = st.Queries.UpsertCollectorInstance(ctx, sqlc.UpsertCollectorInstanceParams{ID: item.id, CollectorID: collector.ID, Name: item.name, LocalAttributes: []byte(attrs)})
			if err == nil {
				err = st.Queries.UpdateInstanceStatus(ctx, sqlc.UpdateInstanceStatusParams{ID: item.id, RemoteConfigStatus: pgtype.Text{String: item.status, Valid: true}, RemoteConfigError: pgtype.Text{String: item.failure, Valid: item.failure != ""}})
			}
		}
		if err != nil {
			return fmt.Errorf("seeding %s instance: %w", item.role, err)
		}
	}
	fmt.Println("collectors: metrics(healthy), logs(stale), singleton(failed)")
	if err := seedPipelines(ctx, st, platform.ID); err != nil {
		return fmt.Errorf("seeding pipelines: %w", err)
	}
	if err := seedDestinations(ctx, st, platform.ID); err != nil {
		return fmt.Errorf("seeding destinations: %w", err)
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

func seedPipelines(ctx context.Context, st *store.Store, orgID pgtype.UUID) error {
	matchers := json.RawMessage(`[]`)
	items := []struct {
		name, contents, source string
		enabled                bool
	}{
		{"base-metrics", `prometheus.exporter.self "seed" {}`, "ui", true},
		{"loki-logs", `loki.source.file "seed" { targets = [] forward_to = [] }`, "ui", false},
		{"app-obs-wizard", `// wizard-generated`, "wizard", false},
	}
	for _, item := range items {
		if _, err := st.Queries.GetPipelineByOrgAndName(ctx, sqlc.GetPipelineByOrgAndNameParams{OrgID: orgID, Name: item.name}); err == nil {
			continue
		}
		if _, err := st.Queries.CreatePipeline(ctx, sqlc.CreatePipelineParams{OrgID: orgID, Name: item.name, Contents: item.contents, Matchers: matchers, Enabled: item.enabled, Source: item.source, CreatedBy: "seed", UpdatedBy: "seed"}); err != nil && !isUnique(err) {
			return err
		}
	}
	fmt.Println("pipelines: base-metrics(enabled), loki-logs(disabled), app-obs-wizard(wizard)")
	return nil
}

func seedDestinations(ctx context.Context, st *store.Store, orgID pgtype.UUID) error {
	for _, item := range []struct{ name, typ, url string }{{"prom-prod", "prometheus", "https://prometheus.example.com/api/v1/write"}, {"loki-prod", "loki", "https://loki.example.com/loki/api/v1/push"}} {
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
