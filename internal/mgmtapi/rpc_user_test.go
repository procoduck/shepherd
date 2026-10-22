package mgmtapi_test

import (
	"context"
	"log/slog"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5/pgtype"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	mgmtv1 "shepherd/gen/shepherd/mgmt/v1"
	"shepherd/internal/auth"
	"shepherd/internal/config"
	"shepherd/internal/mgmtapi"
	"shepherd/internal/store"
	"shepherd/internal/store/sqlc"
)

// UpdateUser previously hand-rolled its own pgx.ErrNoRows -> CodeNotFound /
// else CodeInternal mapping instead of calling mapError (rpc_errors.go) for
// the non-ErrNoRows branch, same shape as loadOwnedDestination's pattern
// (rpc_destination.go). This spec pins that: a real, non-ErrNoRows store
// failure must come back as CodeInternal with mapError's exact message
// shape (internal error, not a raw or bespoke message), the same guarantee
// rpc_errors_test.go's TestMapError pins for mapError itself and
// rpc_destination_test.go's "maps a real lookup failure ... to Internal"
// spec pins for loadOwnedDestination.
var _ = Describe("shepherd.mgmt.v1.UserService UpdateUser error mapping", Label("integration"), func() {
	var (
		ctx    context.Context
		cancel context.CancelFunc
		st     *store.Store
		svc    *mgmtapi.UserService
	)

	BeforeEach(func() {
		ctx, cancel = context.WithCancel(context.Background())
		dbURL := sharedPG.IsolatedDB(ctx, GinkgoTB())

		var err error
		st, err = store.New(ctx, &config.DatabaseConfig{URL: dbURL, MaxConns: 5}, slog.Default())
		Expect(err).NotTo(HaveOccurred())

		svc = mgmtapi.NewUserService(st, auth.NewUserStore(st, slog.Default()), slog.Default())
	})

	AfterEach(func() {
		st.Close()
		cancel()
	})

	It("maps a real store failure on UpdateUser to Internal with mapError's message shape, not a false Not Found", func() {
		u, err := st.Queries.CreateUser(ctx, sqlc.CreateUserParams{
			Login: "update-fault-user", Email: "update-fault@example.com", DisplayName: "Update Fault",
			PasswordHash: "hash", IsAppAdmin: true, MustChangePassword: false,
		})
		Expect(err).NotTo(HaveOccurred())

		// Hold an ACCESS EXCLUSIVE lock on users from a separate connection so
		// UpdateUser's UPDATE blocks on it, then cancel that backend -- forcing
		// a real, non-ErrNoRows failure deterministically, without a
		// fault-injection seam in store.go. Mirrors
		// rpc_destination_test.go's "maps a real lookup failure ... to
		// Internal" spec.
		lockConn, err := st.Pool().Acquire(ctx)
		Expect(err).NotTo(HaveOccurred())
		defer lockConn.Release()
		lockTx, err := lockConn.Begin(ctx)
		Expect(err).NotTo(HaveOccurred())
		defer func() { _ = lockTx.Rollback(ctx) }() //nolint:errcheck // best-effort cleanup; explicit Rollback below is the real one
		_, err = lockTx.Exec(ctx, `LOCK TABLE users IN ACCESS EXCLUSIVE MODE`)
		Expect(err).NotTo(HaveOccurred())

		type result struct {
			resp *connect.Response[mgmtv1.User]
			err  error
		}
		resultCh := make(chan result, 1)
		go func() {
			defer GinkgoRecover()
			// IsAppAdmin: true, Disabled: false keeps guardLastAdmin's "not
			// removing admin rights" fast path (no query, no interference
			// with the held lock) so only UpdateUser's own UPDATE blocks.
			resp, updateErr := svc.UpdateUser(ctx, connect.NewRequest(&mgmtv1.UpdateUserRequest{
				Id: u.ID.String(), Email: "new@example.com", DisplayName: "New Name",
				IsAppAdmin: true, Disabled: false,
			}))
			resultCh <- result{resp, updateErr}
		}()

		var pid int
		Eventually(func() error {
			return st.Pool().QueryRow(ctx,
				`SELECT pid FROM pg_stat_activity
				 WHERE datname = current_database() AND wait_event_type = 'Lock' AND query ILIKE '%UpdateUser%'`,
			).Scan(&pid)
		}, "5s", "20ms").Should(Succeed(), "UpdateUser's query never blocked on the held table lock")
		_, err = st.Pool().Exec(ctx, `SELECT pg_cancel_backend($1)`, pid)
		Expect(err).NotTo(HaveOccurred())

		res := <-resultCh
		Expect(res.resp).To(BeNil())
		Expect(connect.CodeOf(res.err)).To(Equal(connect.CodeInternal), "a real update failure must not be reported as not_found")
		Expect(res.err.Error()).To(Equal("internal: internal error"), "mapError must not leak the raw error to the client")

		Expect(lockTx.Rollback(ctx)).To(Succeed())
	})
})

// SetOrgMember takes the Role enum (step 7a, #361). ROLE_UNSPECIFIED never
// grants, so it is refused rather than stored or defaulted.
var _ = Describe("shepherd.mgmt.v1.UserService SetOrgMember role", Label("integration"), func() {
	var (
		ctx    context.Context
		cancel context.CancelFunc
		st     *store.Store
		svc    *mgmtapi.UserService
		orgID  string
		userID string
	)

	BeforeEach(func() {
		ctx, cancel = context.WithCancel(context.Background())
		dbURL := sharedPG.IsolatedDB(ctx, GinkgoTB())
		var err error
		st, err = store.New(ctx, &config.DatabaseConfig{URL: dbURL, MaxConns: 5}, slog.Default())
		Expect(err).NotTo(HaveOccurred())
		svc = mgmtapi.NewUserService(st, auth.NewUserStore(st, slog.Default()), slog.Default())

		org, err := st.Queries.CreateOrg(ctx, sqlc.CreateOrgParams{Name: "set-member-org", DisplayName: "Set Member", AdminGroupID: "set-member-admin"})
		Expect(err).NotTo(HaveOccurred())
		orgID = org.ID.String()
		u, err := st.Queries.CreateUser(ctx, sqlc.CreateUserParams{
			Login: "set-member-user", Email: "set-member@example.com", DisplayName: "Set Member", PasswordHash: "hash",
		})
		Expect(err).NotTo(HaveOccurred())
		userID = u.ID.String()
	})

	AfterEach(func() {
		st.Close()
		cancel()
	})

	It("refuses ROLE_UNSPECIFIED with InvalidArgument and stores nothing", func() {
		_, err := svc.SetOrgMember(ctx, connect.NewRequest(&mgmtv1.SetOrgMemberRequest{OrgId: orgID, UserId: userID}))
		Expect(connect.CodeOf(err)).To(Equal(connect.CodeInvalidArgument))

		members, err := svc.ListOrgMembers(ctx, connect.NewRequest(&mgmtv1.ListOrgMembersRequest{OrgId: orgID}))
		Expect(err).NotTo(HaveOccurred())
		Expect(members.Msg.GetItems()).To(BeEmpty(), "an unspecified role must not create a membership")
	})

	It("refuses a number outside the Role enum", func() {
		_, err := svc.SetOrgMember(ctx, connect.NewRequest(&mgmtv1.SetOrgMemberRequest{OrgId: orgID, UserId: userID, Role: mgmtv1.Role(42)}))
		Expect(connect.CodeOf(err)).To(Equal(connect.CodeInvalidArgument))
	})

	It("stores ROLE_EDITOR as editor and reports it back as the enum", func() {
		resp, err := svc.SetOrgMember(ctx, connect.NewRequest(&mgmtv1.SetOrgMemberRequest{OrgId: orgID, UserId: userID, Role: mgmtv1.Role_ROLE_EDITOR}))
		Expect(err).NotTo(HaveOccurred())
		Expect(resp.Msg.GetRole()).To(Equal(mgmtv1.Role_ROLE_EDITOR))

		var oid, uid pgtype.UUID
		Expect(oid.Scan(orgID)).To(Succeed())
		Expect(uid.Scan(userID)).To(Succeed())
		stored, err := st.Queries.GetOrgMemberRole(ctx, sqlc.GetOrgMemberRoleParams{OrgID: oid, UserID: uid})
		Expect(err).NotTo(HaveOccurred())
		Expect(stored).To(Equal("editor"))

		members, err := svc.ListOrgMembers(ctx, connect.NewRequest(&mgmtv1.ListOrgMembersRequest{OrgId: orgID}))
		Expect(err).NotTo(HaveOccurred())
		Expect(members.Msg.GetItems()).To(HaveLen(1))
		Expect(members.Msg.GetItems()[0].GetRole()).To(Equal(mgmtv1.Role_ROLE_EDITOR))
	})
})
