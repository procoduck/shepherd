package mgmtapi

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"google.golang.org/protobuf/types/known/timestamppb"

	mgmtv1 "shepherd/gen/shepherd/mgmt/v1"
	"shepherd/gen/shepherd/mgmt/v1/mgmtv1connect"
	"shepherd/internal/auth"
	"shepherd/internal/store"
	"shepherd/internal/store/sqlc"
)

// UserService implements local user management (see user.proto). It has no
// bearing on federated identities: an OIDC user has no row here and their
// access is still decided by their token's groups.
type UserService struct {
	store  *store.Store
	users  *auth.UserStore
	logger *slog.Logger
}

// NewUserService constructs a UserService.
func NewUserService(st *store.Store, users *auth.UserStore, logger *slog.Logger) *UserService {
	return &UserService{store: st, users: users, logger: logger}
}

var _ mgmtv1connect.UserServiceHandler = (*UserService)(nil)

var errUsersUnavailable = errors.New("local user management is not available in this deployment")

func (s *UserService) available() error {
	if s.users == nil || s.store == nil {
		return connect.NewError(connect.CodeUnavailable, errUsersUnavailable)
	}
	return nil
}

// toUserProto projects a users row onto the wire. There is no password_hash
// field on the message, so the hash cannot reach a caller even by mistake.
func toUserProto(u sqlc.User, orgs []*mgmtv1.OrgMembership) *mgmtv1.User {
	out := &mgmtv1.User{
		Id:                 u.ID.String(),
		Login:              u.Login,
		Email:              u.Email,
		DisplayName:        u.DisplayName,
		IsAppAdmin:         u.IsAppAdmin,
		MustChangePassword: u.MustChangePassword,
		Disabled:           u.Disabled,
		CreatedAt:          protoTimestamp(u.CreatedAt),
		UpdatedAt:          protoTimestamp(u.UpdatedAt),
		Orgs:               orgs,
	}
	if u.LastLoginAt.Valid {
		out.LastLoginAt = timestamppb.New(u.LastLoginAt.Time)
	}
	return out
}

// ListUsers returns every local account with its org memberships.
func (s *UserService) ListUsers(ctx context.Context, _ *connect.Request[mgmtv1.ListUsersRequest]) (*connect.Response[mgmtv1.ListUsersResponse], error) {
	if err := s.available(); err != nil {
		return nil, err
	}
	rows, err := s.store.Queries.ListUsers(ctx)
	if err != nil {
		s.logger.Error("listing users", "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to list users"))
	}
	items := make([]*mgmtv1.User, 0, len(rows))
	for i := range rows {
		orgs, orgErr := s.membershipsFor(ctx, rows[i].ID)
		if orgErr != nil {
			// A membership lookup failure must not hide the account itself:
			// an admin needs to see that the user exists even when the join
			// fails, not have them silently vanish from the list.
			s.logger.Warn("listing org memberships", "err", orgErr, "user_id", rows[i].ID.String())
		}
		items = append(items, toUserProto(rows[i], orgs))
	}
	return connect.NewResponse(&mgmtv1.ListUsersResponse{Items: items, Total: int32(len(items))}), nil //nolint:gosec // user counts never approach int32 overflow
}

func (s *UserService) membershipsFor(ctx context.Context, userID pgtype.UUID) ([]*mgmtv1.OrgMembership, error) {
	rows, err := s.store.Queries.ListOrgMembershipsForUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	out := make([]*mgmtv1.OrgMembership, 0, len(rows))
	for _, r := range rows {
		out = append(out, &mgmtv1.OrgMembership{
			Id: r.OrgID.String(), Name: r.OrgName, DisplayName: r.OrgDisplayName, Role: r.Role,
		})
	}
	return out, nil
}

// CreateUser adds a local account.
func (s *UserService) CreateUser(ctx context.Context, req *connect.Request[mgmtv1.CreateUserRequest]) (*connect.Response[mgmtv1.User], error) {
	if err := requireWriteAuthorized(ctx); err != nil {
		return nil, err
	}
	if err := s.available(); err != nil {
		return nil, err
	}
	m := req.Msg
	login := strings.TrimSpace(m.GetLogin())
	if login == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("login is required"))
	}
	if err := auth.ValidatePassword(m.GetPassword()); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	hash, err := auth.HashPassword(m.GetPassword())
	if err != nil {
		s.logger.Error("hashing password", "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to create user"))
	}
	u, err := s.store.Queries.CreateUser(ctx, sqlc.CreateUserParams{
		Login:              login,
		Email:              strings.TrimSpace(m.GetEmail()),
		DisplayName:        strings.TrimSpace(m.GetDisplayName()),
		PasswordHash:       hash,
		IsAppAdmin:         m.GetIsAppAdmin(),
		MustChangePassword: m.GetMustChangePassword(),
	})
	if err != nil {
		if isUniqueViolation(err) {
			return nil, connect.NewError(connect.CodeAlreadyExists, errors.New("a user with that login already exists"))
		}
		s.logger.Error("creating user", "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to create user"))
	}
	auditLogDetail(ctx, s.store, auth.ActorFromCtx(ctx), "user", pgtype.UUID{}, "user.create", "user", u.ID.String(),
		map[string]any{"login": u.Login, "is_app_admin": u.IsAppAdmin})
	return connect.NewResponse(toUserProto(u, nil)), nil
}

// UpdateUser changes profile fields and flags. Passwords go through
// ResetUserPassword so a hash is never carried alongside a general update.
func (s *UserService) UpdateUser(ctx context.Context, req *connect.Request[mgmtv1.UpdateUserRequest]) (*connect.Response[mgmtv1.User], error) {
	if err := requireWriteAuthorized(ctx); err != nil {
		return nil, err
	}
	if err := s.available(); err != nil {
		return nil, err
	}
	id, ok := parseUUID(req.Msg.GetId())
	if !ok {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid user id"))
	}
	if err := s.guardLastAdmin(ctx, id, req.Msg.GetIsAppAdmin(), req.Msg.GetDisabled()); err != nil {
		return nil, err
	}
	u, err := s.store.Queries.UpdateUser(ctx, sqlc.UpdateUserParams{
		ID:          id,
		Email:       strings.TrimSpace(req.Msg.GetEmail()),
		DisplayName: strings.TrimSpace(req.Msg.GetDisplayName()),
		IsAppAdmin:  req.Msg.GetIsAppAdmin(),
		Disabled:    req.Msg.GetDisabled(),
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, connect.NewError(connect.CodeNotFound, errors.New("no such user"))
		}
		s.logger.Error("updating user", "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to update user"))
	}
	auditLogDetail(ctx, s.store, auth.ActorFromCtx(ctx), "user", pgtype.UUID{}, "user.update", "user", u.ID.String(),
		map[string]any{"login": u.Login, "is_app_admin": u.IsAppAdmin, "disabled": u.Disabled})
	orgs, _ := s.membershipsFor(ctx, u.ID) //nolint:errcheck // best-effort enrichment
	return connect.NewResponse(toUserProto(u, orgs)), nil
}

// DeleteUser removes an account. sessions and org_members cascade.
func (s *UserService) DeleteUser(ctx context.Context, req *connect.Request[mgmtv1.DeleteUserRequest]) (*connect.Response[mgmtv1.DeleteUserResponse], error) {
	if err := requireWriteAuthorized(ctx); err != nil {
		return nil, err
	}
	if err := s.available(); err != nil {
		return nil, err
	}
	id, ok := parseUUID(req.Msg.GetId())
	if !ok {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid user id"))
	}
	// Deleting is "no longer an admin" taken to its limit, so it needs the same
	// guard as demoting.
	if err := s.guardLastAdmin(ctx, id, false, true); err != nil {
		return nil, err
	}
	if err := s.store.Queries.DeleteUser(ctx, id); err != nil {
		s.logger.Error("deleting user", "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to delete user"))
	}
	auditLogDetail(ctx, s.store, auth.ActorFromCtx(ctx), "user", pgtype.UUID{}, "user.delete", "user", id.String(), nil)
	return connect.NewResponse(&mgmtv1.DeleteUserResponse{}), nil
}

// guardLastAdmin refuses a change that would leave the deployment with no way
// in.
//
// Demoting or disabling the last enabled app admin locks everyone out of user
// management permanently — there is no recovery path short of editing the
// database by hand, because the account that could undo it is the one just
// removed. The check costs one query and prevents an unrecoverable state.
func (s *UserService) guardLastAdmin(ctx context.Context, id pgtype.UUID, stillAdmin, disabled bool) error {
	if stillAdmin && !disabled {
		return nil // not removing admin rights
	}
	rows, err := s.store.Queries.ListUsers(ctx)
	if err != nil {
		// Fail closed: if we cannot prove another admin exists, do not allow
		// the change that might remove the last one.
		return connect.NewError(connect.CodeInternal, errors.New("could not verify another administrator exists"))
	}
	for i := range rows {
		if rows[i].ID == id {
			continue
		}
		if rows[i].IsAppAdmin && !rows[i].Disabled {
			return nil
		}
	}
	return connect.NewError(connect.CodeFailedPrecondition,
		errors.New("this is the last enabled administrator; promote another account first or you will lock yourself out"))
}

// ResetUserPassword sets another user's password, always requiring them to
// change it at next sign-in.
func (s *UserService) ResetUserPassword(ctx context.Context, req *connect.Request[mgmtv1.ResetUserPasswordRequest]) (*connect.Response[mgmtv1.ResetUserPasswordResponse], error) {
	if err := requireWriteAuthorized(ctx); err != nil {
		return nil, err
	}
	if err := s.available(); err != nil {
		return nil, err
	}
	id, ok := parseUUID(req.Msg.GetId())
	if !ok {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid user id"))
	}
	if err := auth.ValidatePassword(req.Msg.GetNewPassword()); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	hash, err := auth.HashPassword(req.Msg.GetNewPassword())
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to reset password"))
	}
	// must_change_password is always true here: an administrator necessarily
	// knows the value they just set, so it is a handover token, not a password.
	if err := s.store.Queries.SetUserPassword(ctx, sqlc.SetUserPasswordParams{
		ID: id, PasswordHash: hash, MustChangePassword: true,
	}); err != nil {
		s.logger.Error("resetting password", "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to reset password"))
	}
	auditLogDetail(ctx, s.store, auth.ActorFromCtx(ctx), "user", pgtype.UUID{}, "user.password_reset", "user", id.String(), nil)
	return connect.NewResponse(&mgmtv1.ResetUserPasswordResponse{}), nil
}

// ListOrgMembers lists the local users with a role in one org.
func (s *UserService) ListOrgMembers(ctx context.Context, req *connect.Request[mgmtv1.ListOrgMembersRequest]) (*connect.Response[mgmtv1.ListOrgMembersResponse], error) {
	if err := s.available(); err != nil {
		return nil, err
	}
	orgID, ok := parseUUID(req.Msg.GetOrgId())
	if !ok {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid org id"))
	}
	rows, err := s.store.Queries.ListOrgMembers(ctx, orgID)
	if err != nil {
		s.logger.Error("listing org members", "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to list org members"))
	}
	items := make([]*mgmtv1.OrgMember, 0, len(rows))
	for i := range rows {
		r := &rows[i]
		items = append(items, &mgmtv1.OrgMember{
			OrgId: r.OrgID.String(), UserId: r.UserID.String(), Login: r.Login,
			Email: r.Email, DisplayName: r.DisplayName, Role: r.Role, Disabled: r.Disabled,
		})
	}
	return connect.NewResponse(&mgmtv1.ListOrgMembersResponse{Items: items, Total: int32(len(items))}), nil //nolint:gosec // member counts never approach int32 overflow
}

// SetOrgMember adds a user to an org or changes their role there.
func (s *UserService) SetOrgMember(ctx context.Context, req *connect.Request[mgmtv1.SetOrgMemberRequest]) (*connect.Response[mgmtv1.OrgMember], error) {
	if err := requireWriteAuthorized(ctx); err != nil {
		return nil, err
	}
	if err := s.available(); err != nil {
		return nil, err
	}
	orgID, ok := parseUUID(req.Msg.GetOrgId())
	if !ok {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid org id"))
	}
	userID, ok := parseUUID(req.Msg.GetUserId())
	if !ok {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid user id"))
	}
	role := strings.TrimSpace(req.Msg.GetRole())
	switch role {
	case auth.OrgRoleAdmin, auth.OrgRoleEditor, auth.OrgRoleViewer:
	default:
		return nil, connect.NewError(connect.CodeInvalidArgument,
			errors.New(`role must be "admin", "editor" or "viewer"`))
	}
	if _, err := s.store.Queries.UpsertOrgMember(ctx, sqlc.UpsertOrgMemberParams{
		OrgID: orgID, UserID: userID, Role: role,
	}); err != nil {
		s.logger.Error("setting org member", "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to set org member"))
	}
	auditLogDetail(ctx, s.store, auth.ActorFromCtx(ctx), "user", orgID, "org.member_set", "user", userID.String(),
		map[string]any{"role": role})
	return connect.NewResponse(&mgmtv1.OrgMember{OrgId: orgID.String(), UserId: userID.String(), Role: role}), nil
}

// RemoveOrgMember revokes a user's role in an org.
func (s *UserService) RemoveOrgMember(ctx context.Context, req *connect.Request[mgmtv1.RemoveOrgMemberRequest]) (*connect.Response[mgmtv1.RemoveOrgMemberResponse], error) {
	if err := requireWriteAuthorized(ctx); err != nil {
		return nil, err
	}
	if err := s.available(); err != nil {
		return nil, err
	}
	orgID, ok := parseUUID(req.Msg.GetOrgId())
	if !ok {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid org id"))
	}
	userID, ok := parseUUID(req.Msg.GetUserId())
	if !ok {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid user id"))
	}
	if err := s.store.Queries.DeleteOrgMember(ctx, sqlc.DeleteOrgMemberParams{OrgID: orgID, UserID: userID}); err != nil {
		s.logger.Error("removing org member", "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to remove org member"))
	}
	auditLogDetail(ctx, s.store, auth.ActorFromCtx(ctx), "user", orgID, "org.member_remove", "user", userID.String(), nil)
	return connect.NewResponse(&mgmtv1.RemoveOrgMemberResponse{}), nil
}
