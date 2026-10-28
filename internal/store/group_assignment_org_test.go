package store_test

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/config"
	"shepherd/internal/store"
	"shepherd/internal/store/sqlc"
)

// A group assignment is bound to the org that granted it (0053), and every
// query that changes a cluster's org removes, in the same statement, the
// grants on its collectors that the new org did not make. These specs drive
// the queries every caller (AdminService ClaimCluster/UnclaimCluster, agent
// auto-claim, the dev seed) goes through.
var _ = Describe("group assignments across cluster org changes", Label("integration"), func() {
	var (
		ctx        context.Context
		cancel     context.CancelFunc
		st         *store.Store
		orgA, orgB pgtype.UUID
		cluster    sqlc.Cluster
		collector  sqlc.Collector
	)

	BeforeEach(func() {
		ctx, cancel = context.WithCancel(context.Background())
		dbURL := sharedPG.IsolatedDB(ctx, GinkgoTB())
		Expect(store.MigrateUp(ctx, dbURL)).To(Succeed())
		var err error
		st, err = store.New(ctx, &config.DatabaseConfig{URL: dbURL, MaxConns: 5})
		Expect(err).NotTo(HaveOccurred())

		a, err := st.Queries.CreateOrg(ctx, sqlc.CreateOrgParams{Name: "ga-a", DisplayName: "A", AdminGroupID: "a-admin"})
		Expect(err).NotTo(HaveOccurred())
		b, err := st.Queries.CreateOrg(ctx, sqlc.CreateOrgParams{Name: "ga-b", DisplayName: "B", AdminGroupID: "b-admin"})
		Expect(err).NotTo(HaveOccurred())
		orgA, orgB = a.ID, b.ID

		cluster, err = st.Queries.UpsertCluster(ctx, "ga-cluster")
		Expect(err).NotTo(HaveOccurred())
		Expect(st.Queries.ClaimCluster(ctx, sqlc.ClaimClusterParams{ID: cluster.ID, OrgID: orgA})).To(Succeed())
		collector, err = st.Queries.UpsertCollector(ctx, sqlc.UpsertCollectorParams{ClusterID: cluster.ID, Role: "metrics"})
		Expect(err).NotTo(HaveOccurred())
		_, err = st.Queries.CreateGroupAssignment(ctx, sqlc.CreateGroupAssignmentParams{
			CollectorID: collector.ID, GroupID: "grp-a", GroupDisplayName: "A readers", OrgID: orgA,
		})
		Expect(err).NotTo(HaveOccurred())
	})

	AfterEach(func() {
		st.Close()
		cancel()
	})

	grantsIn := func(org pgtype.UUID) []pgtype.UUID {
		ids, err := st.Queries.ListCollectorIDsByGroupMembershipInOrg(ctx, sqlc.ListCollectorIDsByGroupMembershipInOrgParams{
			GroupIds: []string{"grp-a"}, OrgID: org,
		})
		Expect(err).NotTo(HaveOccurred())
		return ids
	}
	rowCount := func() int {
		rows, err := st.Queries.ListGroupAssignmentsByCollector(ctx, sqlc.ListGroupAssignmentsByCollectorParams{CollectorID: collector.ID, OrgID: orgA})
		Expect(err).NotTo(HaveOccurred())
		rowsB, err := st.Queries.ListGroupAssignmentsByCollector(ctx, sqlc.ListGroupAssignmentsByCollectorParams{CollectorID: collector.ID, OrgID: orgB})
		Expect(err).NotTo(HaveOccurred())
		return len(rows) + len(rowsB)
	}

	It("lets an org be deleted while it still holds a grant on a cluster another org owns", func() {
		// A v0.21 pod's claim moves the cluster without removing org A's grant.
		_, err := st.Pool().Exec(ctx, "UPDATE clusters SET org_id = $1 WHERE id = $2", orgB, cluster.ID) // RAW-SQL-OK: simulates an older binary's claim
		Expect(err).NotTo(HaveOccurred())
		Expect(st.Queries.DeleteOrg(ctx, orgA)).To(Succeed())
		Expect(rowCount()).To(Equal(0))
	})

	It("grants within the org that made the grant", func() {
		Expect(grantsIn(orgA)).To(ConsistOf(collector.ID))
		Expect(grantsIn(orgB)).To(BeEmpty())
	})

	It("ClaimCluster into another org removes the previous org's grants", func() {
		Expect(st.Queries.ClaimCluster(ctx, sqlc.ClaimClusterParams{ID: cluster.ID, OrgID: orgB})).To(Succeed())
		Expect(grantsIn(orgB)).To(BeEmpty())
		Expect(rowCount()).To(BeZero())
	})

	It("ClaimCluster into the same org keeps its grants", func() {
		Expect(st.Queries.ClaimCluster(ctx, sqlc.ClaimClusterParams{ID: cluster.ID, OrgID: orgA})).To(Succeed())
		Expect(grantsIn(orgA)).To(ConsistOf(collector.ID))
	})

	It("UnclaimCluster removes every grant, so a later claim inherits none", func() {
		Expect(st.Queries.UnclaimCluster(ctx, cluster.ID)).To(Succeed())
		Expect(rowCount()).To(BeZero())
		claimed, err := st.Queries.ClaimClusterForOrg(ctx, sqlc.ClaimClusterForOrgParams{ID: cluster.ID, OrgID: orgB})
		Expect(err).NotTo(HaveOccurred())
		Expect(claimed).To(Equal(orgB))
		Expect(grantsIn(orgB)).To(BeEmpty())
	})

	It("a refused ClaimClusterForOrg leaves the owner's grants alone", func() {
		_, err := st.Queries.ClaimClusterForOrg(ctx, sqlc.ClaimClusterForOrgParams{ID: cluster.ID, OrgID: orgB})
		Expect(err).To(MatchError(pgx.ErrNoRows))
		Expect(grantsIn(orgA)).To(ConsistOf(collector.ID))
	})

	It("a confirming ClaimClusterForOrg keeps the owner's grants", func() {
		_, err := st.Queries.ClaimClusterForOrg(ctx, sqlc.ClaimClusterForOrgParams{ID: cluster.ID, OrgID: orgA})
		Expect(err).NotTo(HaveOccurred())
		Expect(grantsIn(orgA)).To(ConsistOf(collector.ID))
	})

	It("CreateGroupAssignment writes nothing for a collector outside the named org", func() {
		_, err := st.Queries.CreateGroupAssignment(ctx, sqlc.CreateGroupAssignmentParams{
			CollectorID: collector.ID, GroupID: "grp-b", GroupDisplayName: "B readers", OrgID: orgB,
		})
		Expect(err).To(MatchError(pgx.ErrNoRows))
		Expect(rowCount()).To(Equal(1))
	})
})
