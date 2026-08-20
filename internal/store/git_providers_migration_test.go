package store_test

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/store"
)

// Migration 0006_git_providers (docs/git-provider-design.md §3.3):
//   - ado_credentials renames to git_credentials, gains kind/username/secret2_enc/
//     provider_config/ssh_known_hosts/ca_cert/tls_insecure_skip_verify, and the ADO
//     trio (ado_org_url, entra_tenant_id, client_id) becomes nullable.
//   - repo_links gains repo_url, backfilled from the credential's ado_org_url plus the
//     link's project/repository, then drops those two columns.
//
// This spec seeds a pre-0006 row shaped exactly like production data, applies the
// migration, and asserts the backfill; then rolls the migration back and asserts the
// original shape is reconstructed.
var _ = Describe("Migration: 0006_git_providers", Label("integration"), func() {
	It("backfills repo_url from ADO org/project/repo and reverses cleanly", func(ctx context.Context) {
		url := sharedPG.RootURL

		// Land on the schema immediately before 0006 (i.e. 0005's shape): migrate to
		// head, then step back one version at a time until ado_credentials
		// (dropped by 0006) exists again. A single step back is not enough once any
		// migration lands after 0006 (0007_simulate_runs and beyond) — this loop
		// keeps the spec correct regardless of how many migrations now sit above it,
		// rather than hardcoding a step count that silently goes stale.
		Expect(store.MigrateUp(ctx, url)).To(Succeed())
		const maxStepsAbove0006 = 10 // generous headroom for migrations added after 0006
		foundPre0006Shape := false
		for i := 0; i < maxStepsAbove0006; i++ {
			Expect(store.MigrateDown(ctx, url)).To(Succeed())
			probe, err := pgxpool.New(ctx, url)
			Expect(err).NotTo(HaveOccurred())
			var exists bool
			scanErr := probe.QueryRow(ctx,
				`SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name = 'ado_credentials')`,
			).Scan(&exists)
			probe.Close()
			Expect(scanErr).NotTo(HaveOccurred())
			if exists {
				foundPre0006Shape = true
				break
			}
		}
		Expect(foundPre0006Shape).To(BeTrue(), "should step back past 0006 to a schema with ado_credentials within a bounded number of steps")

		db, err := pgxpool.New(ctx, url)
		Expect(err).NotTo(HaveOccurred())
		defer db.Close()

		// Seed an old-shape ado_credentials row + repo_link, as if written by the
		// pre-F9 application code.
		var orgID, clusterID, collectorID, credID, linkID string
		Expect(db.QueryRow(ctx,
			`INSERT INTO orgs (name, display_name, admin_group_id)
			 VALUES ('acme', 'Acme', 'grp-admin') RETURNING id`,
		).Scan(&orgID)).To(Succeed())

		Expect(db.QueryRow(ctx,
			`INSERT INTO clusters (name, org_id) VALUES ('prod', $1) RETURNING id`,
			orgID,
		).Scan(&clusterID)).To(Succeed())

		Expect(db.QueryRow(ctx,
			`INSERT INTO collectors (cluster_id, role) VALUES ($1, 'metrics') RETURNING id`,
			clusterID,
		).Scan(&collectorID)).To(Succeed())

		Expect(db.QueryRow(ctx,
			`INSERT INTO ado_credentials (org_id, name, ado_org_url, entra_tenant_id, client_id, client_secret_enc)
			 VALUES ($1, 'primary', 'https://dev.azure.com/myorg/', 'tenant-1', 'client-1', 'secret'::bytea)
			 RETURNING id`,
			orgID,
		).Scan(&credID)).To(Succeed())

		Expect(db.QueryRow(ctx,
			`INSERT INTO repo_links (org_id, collector_id, credential_id, project, repository, branch, path, poll_interval_seconds)
			 VALUES ($1, $2, $3, 'myproject', 'myrepo', 'main', '/', 180)
			 RETURNING id`,
			orgID, collectorID, credID,
		).Scan(&linkID)).To(Succeed())

		db.Close() // release the connection before migrating (matches IsolatedDB's own convention)

		// Apply 0006.
		Expect(store.MigrateUp(ctx, url)).To(Succeed())

		db2, err := pgxpool.New(ctx, url)
		Expect(err).NotTo(HaveOccurred())
		defer db2.Close()

		var tableExists bool
		Expect(db2.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name = 'git_credentials')`,
		).Scan(&tableExists)).To(Succeed())
		Expect(tableExists).To(BeTrue(), "ado_credentials should have been renamed to git_credentials")

		Expect(db2.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name = 'ado_credentials')`,
		).Scan(&tableExists)).To(Succeed())
		Expect(tableExists).To(BeFalse(), "ado_credentials should no longer exist under its old name")

		var kind string
		Expect(db2.QueryRow(ctx, `SELECT kind FROM git_credentials WHERE id = $1`, credID).Scan(&kind)).To(Succeed())
		Expect(kind).To(Equal("ado_sp"), "backfilled credentials should default to kind=ado_sp")

		var repoURL string
		Expect(db2.QueryRow(ctx, `SELECT repo_url FROM repo_links WHERE id = $1`, linkID).Scan(&repoURL)).To(Succeed())
		Expect(repoURL).To(Equal("https://dev.azure.com/myorg/myproject/_git/myrepo"),
			"repo_url should be the ADO clone URL derived from ado_org_url/project/repository")

		var projectColExists bool
		Expect(db2.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name = 'repo_links' AND column_name = 'project')`,
		).Scan(&projectColExists)).To(Succeed())
		Expect(projectColExists).To(BeFalse(), "repo_links.project should have been dropped")

		var repoURLNullable string
		Expect(db2.QueryRow(ctx,
			`SELECT is_nullable FROM information_schema.columns WHERE table_name = 'repo_links' AND column_name = 'repo_url'`,
		).Scan(&repoURLNullable)).To(Succeed())
		Expect(repoURLNullable).To(Equal("NO"), "repo_links.repo_url should be NOT NULL")

		db2.Close()

		// Reverse it. store.MigrateUp above went all the way to head, which may sit
		// above 0006 (0007_simulate_runs and beyond) — step down the same
		// bounded, condition-checked way as the initial pre-0006 descent, since a
		// single MigrateDown now only reverts whatever is above 0006, not 0006
		// itself.
		foundPre0006Shape = false
		for i := 0; i < maxStepsAbove0006; i++ {
			Expect(store.MigrateDown(ctx, url)).To(Succeed())
			probe, err := pgxpool.New(ctx, url)
			Expect(err).NotTo(HaveOccurred())
			var exists bool
			scanErr := probe.QueryRow(ctx,
				`SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name = 'ado_credentials')`,
			).Scan(&exists)
			probe.Close()
			Expect(scanErr).NotTo(HaveOccurred())
			if exists {
				foundPre0006Shape = true
				break
			}
		}
		Expect(foundPre0006Shape).To(BeTrue(), "should step back past 0006 to a schema with ado_credentials within a bounded number of steps")

		db3, err := pgxpool.New(ctx, url)
		Expect(err).NotTo(HaveOccurred())
		defer db3.Close()

		Expect(db3.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name = 'ado_credentials')`,
		).Scan(&tableExists)).To(Succeed())
		Expect(tableExists).To(BeTrue(), "down migration should restore the ado_credentials name")

		var project, repository string
		Expect(db3.QueryRow(ctx,
			`SELECT project, repository FROM repo_links WHERE id = $1`, linkID,
		).Scan(&project, &repository)).To(Succeed())
		Expect(project).To(Equal("myproject"))
		Expect(repository).To(Equal("myrepo"))

		var kindColExists bool
		Expect(db3.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name = 'ado_credentials' AND column_name = 'kind')`,
		).Scan(&kindColExists)).To(Succeed())
		Expect(kindColExists).To(BeFalse(), "kind should have been dropped by the down migration")

		// Leave the shared DB back at head so other specs relying on the latest schema
		// see it. (Also proves the up direction is stable after a round trip.)
		Expect(store.MigrateUp(ctx, url)).To(Succeed())
	})
})
