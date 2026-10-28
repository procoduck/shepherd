package store_test

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5/pgxpool"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/store"
)

// Migration 0053_group_assignment_org: a group assignment is bound to the org
// that granted it. The backfill binds a row to its collector's current org
// unless the cluster has no org, or the audit log records a cluster.claim /
// cluster.unclaim of the cluster after the row was created; those rows are
// written to the audit log, then deleted.
var _ = Describe("Migration: 0053_group_assignment_org", Label("integration"), func() {
	var (
		db  *pgxpool.Pool
		url string
	)

	reopen := func(ctx context.Context) {
		var err error
		db, err = pgxpool.New(ctx, url)
		Expect(err).NotTo(HaveOccurred())
	}

	// RAW-SQL-OK: migration specs drive the schema directly.
	scalar := func(ctx context.Context, q string, args ...any) string {
		var s string
		Expect(db.QueryRow(ctx, q, args...).Scan(&s)).To(Succeed(), q)
		return s
	}

	var (
		orgA, orgB                   string
		collStill, collMoved, collNo string
		collEarly                    string
	)

	BeforeEach(func(ctx context.Context) {
		url = sharedPG.IsolatedDB(ctx, GinkgoTB())
		Expect(store.MigrateTo(ctx, url, 52)).To(Succeed())
		reopen(ctx)

		orgA = scalar(ctx, `INSERT INTO orgs (name, display_name, admin_group_id) VALUES ('m53-a', 'A', 'a-admin') RETURNING id::text`)
		orgB = scalar(ctx, `INSERT INTO orgs (name, display_name, admin_group_id) VALUES ('m53-b', 'B', 'b-admin') RETURNING id::text`)

		cluster := func(name string, org any) string {
			return scalar(ctx, `INSERT INTO clusters (name, org_id) VALUES ($1, $2) RETURNING id::text`, name, org)
		}
		collector := func(clusterID string) string {
			return scalar(ctx, `INSERT INTO collectors (cluster_id, role) VALUES ($1, 'metrics') RETURNING id::text`, clusterID)
		}
		grant := func(collectorID, group, at string) {
			_, err := db.Exec(ctx, `INSERT INTO group_assignments (collector_id, group_id, group_display_name, created_at)
				VALUES ($1, $2, $2, $3::timestamptz)`, collectorID, group, at)
			Expect(err).NotTo(HaveOccurred())
		}
		audit := func(org any, action, resType, resID, detail, at string) {
			_, err := db.Exec(ctx, `INSERT INTO audit_log (at, actor, actor_type, org_id, action, resource_type, resource_id, detail)
				VALUES ($1::timestamptz, 'admin@example.com', 'user', $2, $3, $4, $5, $6::jsonb)`,
				at, org, action, resType, resID, detail)
			Expect(err).NotTo(HaveOccurred())
		}

		// (i) never moved: claimed into A before the grant, nothing after.
		still := cluster("m53-still", orgA)
		collStill = collector(still)
		audit(orgA, "cluster.claim", "cluster", still, `{}`, "2026-01-01T00:00:00Z")
		grant(collStill, "grp-still", "2026-02-01T00:00:00Z")

		// (ii) granted while in B, then claimed into A: the claim is audited
		// after the row was created.
		moved := cluster("m53-moved", orgA)
		collMoved = collector(moved)
		audit(orgB, "assignment.create", "collector", collMoved, `{"group_id":"grp-moved"}`, "2026-02-01T00:00:00Z")
		grant(collMoved, "grp-moved", "2026-02-01T00:00:00Z")
		audit(orgA, "cluster.claim", "cluster", moved, `{}`, "2026-03-01T00:00:00Z")

		// (iii) the cluster has no org.
		none := cluster("m53-none", nil)
		collNo = collector(none)
		grant(collNo, "grp-none", "2026-02-01T00:00:00Z")

		// An unclaim BEFORE the grant is not a change after it.
		early := cluster("m53-early", orgB)
		collEarly = collector(early)
		audit(orgA, "cluster.unclaim", "cluster", early, `{}`, "2026-01-01T00:00:00Z")
		grant(collEarly, "grp-early", "2026-02-01T00:00:00Z")

		db.Close()
		Expect(store.MigrateUp(ctx, url)).To(Succeed())
		reopen(ctx)
		DeferCleanup(func() { db.Close() })
	})

	It("binds rows on clusters whose org has not changed since the grant", func(ctx context.Context) {
		Expect(scalar(ctx, `SELECT org_id::text FROM group_assignments WHERE collector_id = $1`, collStill)).To(Equal(orgA))
		Expect(scalar(ctx, `SELECT org_id::text FROM group_assignments WHERE collector_id = $1`, collEarly)).To(Equal(orgB))
	})

	It("deletes rows whose cluster changed org after the grant, or has no org", func(ctx context.Context) {
		for _, c := range []string{collMoved, collNo} {
			Expect(scalar(ctx, `SELECT count(*)::text FROM group_assignments WHERE collector_id = $1`, c)).To(Equal("0"), c)
		}
		Expect(scalar(ctx, `SELECT count(*)::text FROM group_assignments`)).To(Equal("2"))
	})

	It("records one audit entry per deleted row", func(ctx context.Context) {
		rows, err := db.Query(ctx, `SELECT coalesce(org_id::text, ''), resource_id, detail FROM audit_log
			WHERE actor = 'migration:0053' AND actor_type = 'system' AND action = 'assignment.delete'
			  AND resource_type = 'collector' ORDER BY resource_id`)
		Expect(err).NotTo(HaveOccurred())
		defer rows.Close()
		got := map[string]map[string]any{}
		orgOf := map[string]string{}
		for rows.Next() {
			var org, res string
			var raw []byte
			Expect(rows.Scan(&org, &res, &raw)).To(Succeed())
			d := map[string]any{}
			Expect(json.Unmarshal(raw, &d)).To(Succeed())
			got[res] = d
			orgOf[res] = org
		}
		Expect(rows.Err()).NotTo(HaveOccurred())
		Expect(got).To(HaveLen(2))

		Expect(got[collMoved]).To(HaveKeyWithValue("group_id", "grp-moved"))
		Expect(got[collMoved]).To(HaveKeyWithValue("reason", "org changed"))
		Expect(got[collMoved]).To(HaveKeyWithValue("previous_org_id", orgB))
		Expect(got[collMoved]).To(HaveKeyWithValue("current_org_id", orgA))
		Expect(orgOf[collMoved]).To(Equal(orgB), "filed under the org that granted it")

		Expect(got[collNo]).To(HaveKeyWithValue("group_id", "grp-none"))
		Expect(got[collNo]).To(HaveKeyWithValue("reason", "cluster has no org"))
		Expect(got[collNo]).To(HaveKeyWithValue("previous_org_id", BeNil()))
		Expect(orgOf[collNo]).To(BeEmpty(), "no granting org is known, so the entry is platform-level")
	})

	It("refuses a v0.21 binary's insert, which names no org", func(ctx context.Context) {
		// Filling the org in for it could bind a grant to an org the cluster
		// moved to after the old binary's ownership check, so it is refused.
		_, err := db.Exec(ctx, `INSERT INTO group_assignments (collector_id, group_id, group_display_name) VALUES ($1, 'g-old', 'g-old')`, collStill)
		Expect(err).To(MatchError(ContainSubstring("not-null")))
	})

	It("drops the column on the down migration and keeps the bound rows", func(ctx context.Context) {
		db.Close()
		Expect(store.MigrateTo(ctx, url, 52)).To(Succeed())
		reopen(ctx)
		Expect(scalar(ctx, `SELECT count(*)::text FROM information_schema.columns
			WHERE table_name = 'group_assignments' AND column_name = 'org_id'`)).To(Equal("0"))
		Expect(scalar(ctx, `SELECT count(*)::text FROM group_assignments`)).To(Equal("2"))
	})
})
