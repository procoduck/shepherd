package store_test

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/store"
)

// Migration 0034_repo_link_last_ok_at: repo_links.last_ok_at is when a link
// last finished a sync "ok" — the baseline gitsync's mass-delete guard
// counts pipelines against (B-GITSYNC-PRUNE). A trigger stamps it, so a
// write from any binary keeps it current — but only when a write moves the
// link to "ok" or records a new commit while ok; an unchanged-commit "ok"
// poll or an unrelated edit leaves it. Every existing link is backfilled to
// the upgrade's moment, whatever its status. Down drops all of it.
var _ = Describe("Migration: 0034_repo_link_last_ok_at", Label("integration"), func() {
	var (
		db                         *pgxpool.Pool
		url                        string
		org, collector, credential string
		okLink, errLink, newLink   string
		before                     time.Time
	)

	reopen := func(ctx context.Context) {
		var err error
		db, err = pgxpool.New(ctx, url)
		Expect(err).NotTo(HaveOccurred())
	}

	insertLink := func(ctx context.Context, status *string) string {
		var id string
		Expect(db.QueryRow(ctx,
			`INSERT INTO repo_links (org_id, collector_id, credential_id, repo_url, branch, path, poll_interval_seconds, sync_status, last_synced_at)
			 VALUES ($1, $2, $3, 'http://gitea/x.git', 'main', '', 300, $4, '2026-01-01T00:00:00Z') RETURNING id`,
			org, collector, credential, status,
		).Scan(&id)).To(Succeed())
		return id
	}

	lastOK := func(ctx context.Context, id string) pgtype.Timestamptz {
		var t pgtype.Timestamptz
		Expect(db.QueryRow(ctx, `SELECT last_ok_at FROM repo_links WHERE id = $1`, id).Scan(&t)).To(Succeed())
		return t
	}

	// v0.17.0's UpdateRepoLinkSync, verbatim: an older binary's write during
	// a rolling update names none of the new column.
	oldUpdateRepoLinkSync := func(ctx context.Context, id string, commit *string, status string) {
		_, err := db.Exec(ctx, `UPDATE repo_links
SET last_synced_at = now(),
    last_commit    = $2,
    sync_status    = $3,
    sync_error     = $4,
    updated_at     = now()
WHERE id = $1`, id, commit, status, nil)
		Expect(err).NotTo(HaveOccurred())
	}

	strp := func(s string) *string { return &s }

	BeforeEach(func(ctx context.Context) {
		url = sharedPG.IsolatedDB(ctx, GinkgoTB())
		Expect(store.MigrateTo(ctx, url, 33)).To(Succeed())
		reopen(ctx)
		DeferCleanup(func() { db.Close() })

		Expect(db.QueryRow(ctx,
			`INSERT INTO orgs (name, display_name, admin_group_id) VALUES ('m34', 'M34', 'g') RETURNING id`,
		).Scan(&org)).To(Succeed())
		var cluster string
		Expect(db.QueryRow(ctx, `INSERT INTO clusters (name, org_id) VALUES ('m34', $1) RETURNING id`, org).Scan(&cluster)).To(Succeed())
		Expect(db.QueryRow(ctx,
			`INSERT INTO collectors (cluster_id, role) VALUES ($1, 'metrics') RETURNING id`, cluster,
		).Scan(&collector)).To(Succeed())
		Expect(db.QueryRow(ctx,
			`INSERT INTO git_credentials (org_id, name, kind, client_secret_enc) VALUES ($1, 'c', 'none', '\x00'::bytea) RETURNING id`, org,
		).Scan(&credential)).To(Succeed())
		okLink = insertLink(ctx, strp("ok"))
		errLink = insertLink(ctx, strp("error"))
		newLink = insertLink(ctx, nil)

		Expect(db.QueryRow(ctx, `SELECT now()`).Scan(&before)).To(Succeed())
		db.Close()
		Expect(store.MigrateUp(ctx, url)).To(Succeed())
		reopen(ctx)
	})

	It("backfills every existing link to the upgrade's moment, whatever its status", func(ctx context.Context) {
		for _, id := range []string{okLink, errLink, newLink} {
			t := lastOK(ctx, id)
			Expect(t.Valid).To(BeTrue(), id)
			Expect(t.Time).To(BeTemporally(">=", before), "not the link's old last_synced_at")
		}
	})

	It("stamps an older binary's write only when it moves the link to ok or records a new commit", func(ctx context.Context) {
		start := lastOK(ctx, errLink).Time

		oldUpdateRepoLinkSync(ctx, errLink, nil, "error")
		Expect(lastOK(ctx, errLink).Time).To(Equal(start), "an error sync does not stamp")
		oldUpdateRepoLinkSync(ctx, errLink, nil, "warning")
		Expect(lastOK(ctx, errLink).Time).To(Equal(start), "a warning sync does not stamp")

		oldUpdateRepoLinkSync(ctx, errLink, strp("c1"), "ok")
		stamped := lastOK(ctx, errLink).Time
		Expect(stamped).To(BeTemporally(">", start), "moving to ok stamps")

		oldUpdateRepoLinkSync(ctx, errLink, strp("c1"), "ok")
		Expect(lastOK(ctx, errLink).Time).To(Equal(stamped), "an unchanged-commit ok poll does not move the baseline")

		_, err := db.Exec(ctx, `UPDATE repo_links SET poll_interval_seconds = 60 WHERE id = $1`, errLink)
		Expect(err).NotTo(HaveOccurred())
		Expect(lastOK(ctx, errLink).Time).To(Equal(stamped), "an unrelated edit does not move the baseline")

		oldUpdateRepoLinkSync(ctx, errLink, strp("c2"), "ok")
		Expect(lastOK(ctx, errLink).Time).To(BeTemporally(">", stamped), "a new commit synced ok stamps")
	})

	It("leaves a new link unstamped until its first ok, and stamps an INSERT that is ok", func(ctx context.Context) {
		fresh := insertLink(ctx, nil)
		Expect(lastOK(ctx, fresh).Valid).To(BeFalse())
		Expect(lastOK(ctx, insertLink(ctx, strp("ok"))).Valid).To(BeTrue())
	})

	It("stamps the same instant as the created_at of pipelines the ok sync's own transaction inserts", func(ctx context.Context) {
		tx, err := db.Begin(ctx)
		Expect(err).NotTo(HaveOccurred())
		var created pgtype.Timestamptz
		Expect(tx.QueryRow(ctx,
			`INSERT INTO pipelines (org_id, name, contents, source, repo_link_id, git_path, enabled)
			 VALUES ($1, 'p', '// p', 'git', $2, 'p.alloy', true) RETURNING created_at`, org, newLink,
		).Scan(&created)).To(Succeed())
		_, err = tx.Exec(ctx, `UPDATE repo_links SET sync_status = 'ok', last_commit = 'c1' WHERE id = $1`, newLink)
		Expect(err).NotTo(HaveOccurred())
		Expect(tx.Commit(ctx)).To(Succeed())

		Expect(lastOK(ctx, newLink).Time).To(Equal(created.Time), "the sync's own creates are part of the baseline")
	})

	It("rolls back to 33 by dropping the column, trigger and function, and goes up again cleanly", func(ctx context.Context) {
		db.Close()
		Expect(store.MigrateTo(ctx, url, 33)).To(Succeed())
		reopen(ctx)
		var n int
		Expect(db.QueryRow(ctx,
			`SELECT count(*) FROM information_schema.columns WHERE table_name = 'repo_links' AND column_name = 'last_ok_at'`,
		).Scan(&n)).To(Succeed())
		Expect(n).To(BeZero())
		Expect(db.QueryRow(ctx, `SELECT count(*) FROM pg_proc WHERE proname = 'repo_links_stamp_last_ok'`).Scan(&n)).To(Succeed())
		Expect(n).To(BeZero())
		oldUpdateRepoLinkSync(ctx, okLink, strp("c1"), "ok")

		db.Close()
		Expect(store.MigrateUp(ctx, url)).To(Succeed())
		reopen(ctx)
		Expect(lastOK(ctx, okLink).Valid).To(BeTrue())
	})
})
