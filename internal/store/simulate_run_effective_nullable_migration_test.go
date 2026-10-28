package store_test

import (
	"context"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/store"
	"shepherd/internal/store/sqlc"
)

// Migration 0051_simulate_run_effective_nullable: 0050 made
// effective_duration_seconds NOT NULL, so a v0.20 server (whose run insert
// names only org_id, graph, requested_duration_seconds and created_by) could
// not start a sandbox run during a rolling upgrade or after a rollback. 0051
// allows NULL there; every reader falls back to the requested duration. Down
// backfills NULLs from the requested duration and restores NOT NULL.
var _ = Describe("Migration: 0051_simulate_run_effective_nullable", Label("integration"), func() {
	var (
		db  *pgxpool.Pool
		url string
		org pgtype.UUID
	)

	reopen := func(ctx context.Context) {
		var err error
		db, err = pgxpool.New(ctx, url)
		Expect(err).NotTo(HaveOccurred())
	}

	// insertV020 is the v0.20.0 CreateSimulateRun statement, verbatim in
	// shape: it does not know about the 0050 columns.
	insertV020 := func(ctx context.Context, requested int) (pgtype.UUID, error) {
		var id pgtype.UUID
		// RAW-SQL-OK: migration specs drive the schema directly (v0.20 insert shape).
		err := db.QueryRow(ctx,
			`INSERT INTO simulate_runs (org_id, graph, requested_duration_seconds, created_by)
			 VALUES ($1, '{}'::jsonb, $2, 'v020-pod') RETURNING id`, org, requested,
		).Scan(&id)
		return id, err
	}

	BeforeEach(func(ctx context.Context) {
		url = sharedPG.IsolatedDB(ctx, GinkgoTB())
		Expect(store.MigrateUp(ctx, url)).To(Succeed())
		reopen(ctx)
		DeferCleanup(func() { db.Close() })
		// RAW-SQL-OK: migration specs drive the schema directly.
		Expect(db.QueryRow(ctx, `INSERT INTO orgs (name, display_name, admin_group_id) VALUES ('m51', 'm51', 'g') RETURNING id`).Scan(&org)).To(Succeed())
	})

	It("accepts a v0.20 run insert and reads it back with effective = requested", func(ctx context.Context) {
		id, err := insertV020(ctx, 25)
		Expect(err).NotTo(HaveOccurred())

		q := sqlc.New(db)
		got, err := q.GetSimulateRunByID(ctx, id)
		Expect(err).NotTo(HaveOccurred())
		Expect(got.RequestedDurationSeconds).To(BeEquivalentTo(25))
		Expect(got.EffectiveDurationSeconds).To(BeEquivalentTo(25))

		claimed, err := q.ClaimQueuedSimulateRun(ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(claimed.ID).To(Equal(id))
		Expect(claimed.EffectiveDurationSeconds).To(BeEquivalentTo(25))
	})

	It("keeps the effective-duration range check", func(ctx context.Context) {
		// RAW-SQL-OK: migration specs drive the schema directly.
		_, err := db.Exec(ctx,
			`INSERT INTO simulate_runs (org_id, graph, requested_duration_seconds, created_by, effective_duration_seconds)
			 VALUES ($1, '{}'::jsonb, 10, 'x', 121)`, org)
		Expect(err).To(MatchError(ContainSubstring("simulate_runs_effective_duration_seconds_check")))
	})

	It("backfills NULLs from the requested duration and restores NOT NULL on down", func(ctx context.Context) {
		id, err := insertV020(ctx, 40)
		Expect(err).NotTo(HaveOccurred())
		db.Close()
		Expect(store.MigrateTo(ctx, url, 50)).To(Succeed())
		reopen(ctx)

		var effective int
		// RAW-SQL-OK: migration specs drive the schema directly.
		Expect(db.QueryRow(ctx, `SELECT effective_duration_seconds FROM simulate_runs WHERE id = $1`, id).Scan(&effective)).To(Succeed())
		Expect(effective).To(Equal(40))
		_, err = insertV020(ctx, 10)
		Expect(err).To(MatchError(ContainSubstring("effective_duration_seconds")))

		db.Close()
		Expect(store.MigrateUp(ctx, url)).To(Succeed())
		reopen(ctx)
		_, err = insertV020(ctx, 10)
		Expect(err).NotTo(HaveOccurred())
	})
})
