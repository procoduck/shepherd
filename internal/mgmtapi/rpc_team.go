package mgmtapi

import (
	"context"
	"errors"
	"log/slog"

	"connectrpc.com/connect"

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
	errTeamNameRequired    = errors.New("name is required")
	errTeamGroupIDRequired = errors.New("idp_group_id is required")
	errTeamNotFound        = errors.New("team not found")
	errTeamNameExists      = errors.New("team name already exists in this org")
)

func toTeamProto(t sqlc.Team) *mgmtv1.Team {
	return &mgmtv1.Team{
		Id:         t.ID.String(),
		OrgId:      t.OrgID.String(),
		Name:       t.Name,
		IdpGroupId: t.IdpGroupID,
		CreatedAt:  protoTimestamp(t.CreatedAt),
		UpdatedAt:  protoTimestamp(t.UpdatedAt),
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
	items := make([]*mgmtv1.Team, len(teams))
	for i := range teams {
		items[i] = toTeamProto(teams[i])
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
	if req.Msg.GetIdpGroupId() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errTeamGroupIDRequired)
	}
	t, err := s.store.Queries.CreateTeam(ctx, sqlc.CreateTeamParams{
		OrgID: orgID, Name: req.Msg.GetName(), IdpGroupID: req.Msg.GetIdpGroupId(),
	})
	if err != nil {
		if isUniqueViolation(err) {
			return nil, connect.NewError(connect.CodeAlreadyExists, errTeamNameExists)
		}
		s.logger.Error("create team", "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to create team"))
	}
	auditLog(ctx, s.store, actorFromCtx(ctx), orgID, "team.create", "team", t.ID.String())
	return connect.NewResponse(toTeamProto(t)), nil
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
