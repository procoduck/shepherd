package store_test

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/store"
)

// Migration 0012_teams_service_accounts (docs/gateway-tier-plan.md §4 W10):
// teams, pipeline ownership, service accounts and the audit_log column that
// carries the second half of a delegated action.
//
// 0009 and 0010 each ship a migration-level suite; 0012 originally did not,
// which review flagged. The constraints below all worked when written — the
// point of pinning them is that a later edit to a CHECK or an FK action
// would otherwise change enforcement with nothing going red. The FK action
// in particular is a deliberate choice in the OPPOSITE direction from
// 0008's RESTRICT, and a silent flip either way is a data-loss or a
// stuck-delete bug depending on which way it goes.
var _ = Describe("Migration: 0012_teams_service_accounts", Label("integration"), func() {
	var (
		db    *pgxpool.Pool
		orgID string
	)

	BeforeEach(func(ctx context.Context) {
		url := sharedPG.IsolatedDB(ctx, GinkgoTB())
		Expect(store.MigrateUp(ctx, url)).To(Succeed())

		var err error
		db, err = pgxpool.New(ctx, url)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(db.Close)

		Expect(db.QueryRow(ctx,
			`INSERT INTO orgs (name, display_name, admin_group_id) VALUES ('w10-mig', 'W10 Migration', 'g') RETURNING id`,
		).Scan(&orgID)).To(Succeed())
	})

	It("refuses a service-account capability outside the propose/apply pair", func(ctx context.Context) {
		_, err := db.Exec(ctx,
			`INSERT INTO service_accounts (org_id, name, capability, token_hash, created_by)
			 VALUES ($1, 'bad-cap', 'super-admin', '\x00', 'admin@example.com')`, orgID)
		Expect(err).To(HaveOccurred(),
			"capability is the difference between a token that can propose and one that can apply; "+
				"the database must not accept a third value the Go code has no policy for")
	})

	It("accepts both capabilities the Go constants name", func(ctx context.Context) {
		for _, capability := range []string{"propose", "apply"} {
			_, err := db.Exec(ctx,
				`INSERT INTO service_accounts (org_id, name, capability, token_hash, created_by)
				 VALUES ($1, $2, $3, '\x00', 'admin@example.com')`, orgID, "sa-"+capability, capability)
			Expect(err).NotTo(HaveOccurred(), "capability %q must be accepted — the CHECK has drifted "+
				"from internal/mgmtapi's capabilityPropose/capabilityApply constants", capability)
		}
	})

	It("keeps one team name and one IdP group unique within an org", func(ctx context.Context) {
		_, err := db.Exec(ctx,
			`INSERT INTO teams (org_id, name, idp_group_id) VALUES ($1, 'platform', 'group-a')`, orgID)
		Expect(err).NotTo(HaveOccurred())

		_, err = db.Exec(ctx,
			`INSERT INTO teams (org_id, name, idp_group_id) VALUES ($1, 'platform', 'group-b')`, orgID)
		Expect(err).To(HaveOccurred(), "two teams with the same name in one org would make ownership ambiguous")

		_, err = db.Exec(ctx,
			`INSERT INTO teams (org_id, name, idp_group_id) VALUES ($1, 'other', 'group-a')`, orgID)
		Expect(err).To(HaveOccurred(),
			"two teams mapped to the same IdP group in one org would give one group membership two "+
				"different ownership answers")
	})

	// The FK action here is the one worth pinning. Deleting a team must not
	// delete the pipelines it owned (data loss for something the team merely
	// administered) and must not be blocked by them (a team that can never be
	// removed once it owns anything). SET NULL demotes the pipeline to
	// unowned, which internal/auth.AuthorizeOwnership treats as admin-only —
	// a safe resting state, not an open one.
	It("demotes an owned pipeline to unowned when its team is deleted, rather than deleting or blocking", func(ctx context.Context) {
		var teamID string
		Expect(db.QueryRow(ctx,
			`INSERT INTO teams (org_id, name, idp_group_id) VALUES ($1, 'owners', 'group-owners') RETURNING id`,
			orgID).Scan(&teamID)).To(Succeed())

		var pipelineID string
		Expect(db.QueryRow(ctx,
			`INSERT INTO pipelines (org_id, name, contents, matchers, enabled, source, wizard_state,
			                        created_by, updated_by, owner_team_id)
			 VALUES ($1, 'owned', '// x', '[]', true, 'ui', '{}', 't', 't', $2) RETURNING id`,
			orgID, teamID).Scan(&pipelineID)).To(Succeed())

		_, err := db.Exec(ctx, `DELETE FROM teams WHERE id = $1`, teamID)
		Expect(err).NotTo(HaveOccurred(), "deleting a team must not be blocked by the pipelines it owns")

		var ownerTeam *string
		Expect(db.QueryRow(ctx,
			`SELECT owner_team_id FROM pipelines WHERE id = $1`, pipelineID).Scan(&ownerTeam)).To(Succeed(),
			"the pipeline was deleted along with its team — ownership is an administrative label, "+
				"not the pipeline's reason to exist")
		Expect(ownerTeam).To(BeNil(), "owner_team_id should be NULL after the owning team is deleted")
	})

	It("carries on_behalf_of on audit_log for the second half of a delegated action", func(ctx context.Context) {
		var exists bool
		Expect(db.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM information_schema.columns
			                WHERE table_name = 'audit_log' AND column_name = 'on_behalf_of')`,
		).Scan(&exists)).To(Succeed())
		Expect(exists).To(BeTrue(), "G13's delegated half has nowhere to be recorded")
	})
})
