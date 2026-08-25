package mgmtapi

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5/pgtype"

	mgmtv1 "shepherd/gen/shepherd/mgmt/v1"
	"shepherd/gen/shepherd/mgmt/v1/mgmtv1connect"
	"shepherd/internal/store"
	"shepherd/internal/store/sqlc"
)

// TeamService implements mgmtv1connect.TeamServiceHandler: team
// definitions (W10, docs/gateway-tier-plan.md §4) — an org-scoped binding
// of a name to an IdP group. See team.proto for the authorization model.
type TeamService struct {
	store  *store.Store
	logger *slog.Logger
}

// NewTeamService constructs a TeamService.
func NewTeamService(st *store.Store, logger *slog.Logger) *TeamService {
	return &TeamService{store: st, logger: logger}
}

var _ mgmtv1connect.TeamServiceHandler = (*TeamService)(nil)

var (
	errTeamNameRequired = errors.New("name is required")
	errTeamNotFound     = errors.New("team not found")
	errTeamNameExists   = errors.New("team name already exists in this org")
	errTeamGroupExists  = errors.New("another team in this org is already bound to that group")
	errTeamUserNotFound = errors.New("user not found")
	errTeamUserRequired = errors.New("user_id is required")
	errNotTeamMember    = errors.New("user is not a member of this team")
)

func toTeamProto(t sqlc.Team, memberCount int32) *mgmtv1.Team {
	return &mgmtv1.Team{
		Id:          t.ID.String(),
		OrgId:       t.OrgID.String(),
		Name:        t.Name,
		IdpGroupId:  t.IdpGroupID.String,
		MemberCount: memberCount,
		CreatedAt:   protoTimestamp(t.CreatedAt),
		UpdatedAt:   protoTimestamp(t.UpdatedAt),
	}
}

// ListTeams lists teams in an org.
func (s *TeamService) ListTeams(ctx context.Context, req *connect.Request[mgmtv1.ListTeamsRequest]) (*connect.Response[mgmtv1.ListTeamsResponse], error) {
	orgID, err := scanUUID(req.Msg.GetOrgId())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errOrgIDInvalid)
	}
	teams, err := s.store.Queries.ListTeamsByOrg(ctx, orgID)
	if err != nil {
		s.logger.Error("list teams", "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to list teams"))
	}
	// One grouped count for the whole page rather than a query per row: the
	// list is the only place membership source is visible at a glance, so it
	// must not get slower as an org grows teams.
	ids := make([]pgtype.UUID, len(teams))
	for i := range teams {
		ids[i] = teams[i].ID
	}
	counts := map[string]int32{}
	if len(ids) > 0 {
		rows, cErr := s.store.Queries.CountTeamMembersByTeam(ctx, ids)
		if cErr != nil {
			s.logger.Error("count team members", "err", cErr)
			return nil, connect.NewError(connect.CodeInternal, errors.New("failed to list teams"))
		}
		for _, r := range rows {
			counts[r.TeamID.String()] = int32(r.MemberCount) //nolint:gosec // a team's member count never approaches int32 overflow
		}
	}
	items := make([]*mgmtv1.Team, len(teams))
	for i := range teams {
		items[i] = toTeamProto(teams[i], counts[teams[i].ID.String()])
	}
	return connect.NewResponse(&mgmtv1.ListTeamsResponse{Items: items, Total: int32(len(items))}), nil //nolint:gosec // team counts never approach int32 overflow
}

// CreateTeam creates a team, binding a name to an IdP group within an org.
func (s *TeamService) CreateTeam(ctx context.Context, req *connect.Request[mgmtv1.CreateTeamRequest]) (*connect.Response[mgmtv1.Team], error) {
	if err := requireWriteAuthorized(ctx); err != nil {
		return nil, err
	}
	orgID, err := scanUUID(req.Msg.GetOrgId())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errOrgIDInvalid)
	}
	if req.Msg.GetName() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errTeamNameRequired)
	}
	// idp_group_id is optional: a team may be backed by a group, by explicit
	// members, or by both. Empty is stored as NULL, never '' — '' would read
	// as a real group ID that no session can match.
	group := strings.TrimSpace(req.Msg.GetIdpGroupId())
	t, err := s.store.Queries.CreateTeam(ctx, sqlc.CreateTeamParams{
		OrgID: orgID, Name: req.Msg.GetName(),
		IdpGroupID: pgtype.Text{String: group, Valid: group != ""},
	})
	if err != nil {
		if isUniqueViolation(err) {
			// Two unique constraints can fire; naming the wrong one sends an
			// admin off to rename a team when the real collision is a group
			// already bound to a different team in this org.
			if group != "" && strings.Contains(err.Error(), "idp_group_id") {
				return nil, connect.NewError(connect.CodeAlreadyExists, errTeamGroupExists)
			}
			return nil, connect.NewError(connect.CodeAlreadyExists, errTeamNameExists)
		}
		s.logger.Error("create team", "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to create team"))
	}
	auditLog(ctx, s.store, actorFromCtx(ctx), orgID, "team.create", "team", t.ID.String())
	return connect.NewResponse(toTeamProto(t, 0)), nil
}

// loadOwnedTeam fetches a team by id and enforces it belongs to orgIDStr —
// see loadOwnedDestination's identical doc comment for why (rpc_destination.go).
func (s *TeamService) loadOwnedTeam(ctx context.Context, orgIDStr, idStr string) (sqlc.Team, error) {
	id, err := scanUUID(idStr)
	if err != nil {
		return sqlc.Team{}, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid team id"))
	}
	orgID, err := scanUUID(orgIDStr)
	if err != nil {
		return sqlc.Team{}, connect.NewError(connect.CodeInvalidArgument, errOrgIDInvalid)
	}
	t, err := s.store.Queries.GetTeamByID(ctx, id)
	if err != nil || t.OrgID != orgID {
		return sqlc.Team{}, connect.NewError(connect.CodeNotFound, errTeamNotFound)
	}
	return t, nil
}

// DeleteTeam deletes a team. Pipelines it owned are demoted to unowned
// (ON DELETE SET NULL, 0012_teams_service_accounts.up.sql) rather than
// deleted or blocked — see that migration's comment for why this points
// the opposite direction from destination_bindings' RESTRICT precedent.
func (s *TeamService) DeleteTeam(ctx context.Context, req *connect.Request[mgmtv1.DeleteTeamRequest]) (*connect.Response[mgmtv1.DeleteTeamResponse], error) {
	if err := requireWriteAuthorized(ctx); err != nil {
		return nil, err
	}
	t, err := s.loadOwnedTeam(ctx, req.Msg.GetOrgId(), req.Msg.GetId())
	if err != nil {
		return nil, err
	}
	if err := s.store.Queries.DeleteTeam(ctx, t.ID); err != nil {
		s.logger.Error("delete team", "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to delete team"))
	}
	auditLog(ctx, s.store, actorFromCtx(ctx), t.OrgID, "team.delete", "team", t.ID.String())
	return connect.NewResponse(&mgmtv1.DeleteTeamResponse{}), nil
}

// ListTeamMembers returns the team's explicit (local user) members. Members
// who belong via the team's IdP group are not returned — see team.proto's
// note on why there is no roster to read for them.
func (s *TeamService) ListTeamMembers(ctx context.Context, req *connect.Request[mgmtv1.ListTeamMembersRequest]) (*connect.Response[mgmtv1.ListTeamMembersResponse], error) {
	t, err := s.loadOwnedTeam(ctx, req.Msg.GetOrgId(), req.Msg.GetTeamId())
	if err != nil {
		return nil, err
	}
	rows, err := s.store.Queries.ListTeamMembers(ctx, t.ID)
	if err != nil {
		s.logger.Error("list team members", "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to list team members"))
	}
	items := make([]*mgmtv1.TeamMember, len(rows))
	for i, r := range rows {
		items[i] = &mgmtv1.TeamMember{
			UserId:      r.ID.String(),
			Login:       r.Login,
			Email:       r.Email,
			DisplayName: r.DisplayName,
			Disabled:    r.Disabled,
			AddedAt:     protoTimestamp(r.AddedAt),
		}
	}
	return connect.NewResponse(&mgmtv1.ListTeamMembersResponse{Items: items, Total: int32(len(items))}), nil //nolint:gosec // a team's member count never approaches int32 overflow
}

// AddTeamMember adds a local user to a team. The user must already exist —
// membership never creates an account, so a typo'd id is a not-found rather
// than a silently orphaned row.
func (s *TeamService) AddTeamMember(ctx context.Context, req *connect.Request[mgmtv1.AddTeamMemberRequest]) (*connect.Response[mgmtv1.AddTeamMemberResponse], error) {
	if err := requireWriteAuthorized(ctx); err != nil {
		return nil, err
	}
	t, err := s.loadOwnedTeam(ctx, req.Msg.GetOrgId(), req.Msg.GetTeamId())
	if err != nil {
		return nil, err
	}
	if req.Msg.GetUserId() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errTeamUserRequired)
	}
	userID, err := scanUUID(req.Msg.GetUserId())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errTeamUserRequired)
	}
	if _, err := s.store.Queries.GetUserByID(ctx, userID); err != nil {
		return nil, connect.NewError(connect.CodeNotFound, errTeamUserNotFound)
	}
	// Idempotent (ON CONFLICT DO NOTHING): adding someone twice is the same
	// state as adding them once, and the UI can retry a failed request
	// without having to ask whether the first one landed.
	if err := s.store.Queries.AddTeamMember(ctx, sqlc.AddTeamMemberParams{TeamID: t.ID, UserID: userID}); err != nil {
		s.logger.Error("add team member", "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to add team member"))
	}
	auditLog(ctx, s.store, actorFromCtx(ctx), t.OrgID, "team.member.add", "team", t.ID.String()+"/"+userID.String())
	return connect.NewResponse(&mgmtv1.AddTeamMemberResponse{}), nil
}

// RemoveTeamMember removes a local user from a team. Pipelines the team owns
// are untouched — this revokes one person's scoped write, not the ownership.
func (s *TeamService) RemoveTeamMember(ctx context.Context, req *connect.Request[mgmtv1.RemoveTeamMemberRequest]) (*connect.Response[mgmtv1.RemoveTeamMemberResponse], error) {
	if err := requireWriteAuthorized(ctx); err != nil {
		return nil, err
	}
	t, err := s.loadOwnedTeam(ctx, req.Msg.GetOrgId(), req.Msg.GetTeamId())
	if err != nil {
		return nil, err
	}
	userID, err := scanUUID(req.Msg.GetUserId())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errTeamUserRequired)
	}
	// Unlike Add, this is NOT idempotent: removing someone who is not a
	// member means the caller is looking at stale state, and reporting
	// success would let a UI show "removed" for a membership that some other
	// admin's change had already altered.
	n, err := s.store.Queries.RemoveTeamMember(ctx, sqlc.RemoveTeamMemberParams{TeamID: t.ID, UserID: userID})
	if err != nil {
		s.logger.Error("remove team member", "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to remove team member"))
	}
	if n == 0 {
		return nil, connect.NewError(connect.CodeNotFound, errNotTeamMember)
	}
	auditLog(ctx, s.store, actorFromCtx(ctx), t.OrgID, "team.member.remove", "team", t.ID.String()+"/"+userID.String())
	return connect.NewResponse(&mgmtv1.RemoveTeamMemberResponse{}), nil
}
