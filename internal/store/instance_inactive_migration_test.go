package store_test

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/store"
)

// #237: 'inactive' stops being stored in remote_config_status (it is derived
// from last_seen at read time); 0029 converts the old sweeper's sentinel to
// NULL and leaves every real outcome alone.
var _ = Describe("Migration: 0029_instance_inactive_at_read_time", Label("integration"), func() {
	It("clears the 'inactive' sentinel and keeps real outcomes and last_seen", func(ctx context.Context) {
		url := sharedPG.IsolatedDB(ctx, GinkgoTB())
		Expect(store.MigrateTo(ctx, url, 28)).To(Succeed())
		db, err := pgxpool.New(ctx, url)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(db.Close)

		_, err = db.Exec(ctx, `
			WITH cl AS (INSERT INTO clusters (name) VALUES ('mig-0029') RETURNING id),
			     co AS (INSERT INTO collectors (cluster_id, role) SELECT id, 'metrics' FROM cl RETURNING id)
			INSERT INTO collector_instances (id, collector_id, name, last_seen, remote_config_status, remote_config_error)
			SELECT v.id, co.id, v.id, now() - interval '1 hour', v.status, v.err
			FROM co, (VALUES ('swept', 'inactive', NULL), ('failed', 'FAILED', 'boom'),
			                 ('applied', 'APPLIED', NULL), ('never', NULL, NULL)) AS v(id, status, err)`)
		Expect(err).NotTo(HaveOccurred())

		Expect(store.MigrateUp(ctx, url)).To(Succeed())

		got := map[string]*string{}
		rows, err := db.Query(ctx, `SELECT id, remote_config_status FROM collector_instances WHERE last_seen < now() - interval '59 minutes'`)
		Expect(err).NotTo(HaveOccurred())
		for rows.Next() {
			var id string
			var status *string
			Expect(rows.Scan(&id, &status)).To(Succeed())
			got[id] = status
		}
		Expect(rows.Err()).NotTo(HaveOccurred())
		Expect(got).To(HaveLen(4), "last_seen is untouched, so staleness still reads the same")
		Expect(got["swept"]).To(BeNil())
		Expect(got["never"]).To(BeNil())
		Expect(*got["failed"]).To(Equal("FAILED"))
		Expect(*got["applied"]).To(Equal("APPLIED"))
	})
})
