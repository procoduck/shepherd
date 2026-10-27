package mgmtapi

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	mgmtv1 "shepherd/gen/shepherd/mgmt/v1"
	"shepherd/internal/store/sqlc"
	"shepherd/internal/tenancy"
	"shepherd/internal/validate"
)

// An org's admin-allowed components (0047, #421). Strict tenant mode accepts
// only components the schema overlay classifies allowed or checked; an app
// admin may allow one more for one org — one classified refused, or one no
// shipped schema classifies. That lifts the component refusal only: every
// other strict check still applies, and the UI and audit log show it as
// admin-allowed and unreviewed. It mirrors the import allowlist (#390):
// changes take the exclusive ownership lock, which every pipeline write's
// in-transaction lint (tenancy.LintLocked) takes shared, so an entry cannot
// disappear between a save's check and its commit; removing an entry an
// enabled pipeline of a strict org uses is refused, naming the pipelines.
// Both changes mark the org's served configs dirty and recompute them, so
// the serve-time check's exclusions follow.

// orgStrictComponentDetail is the audit detail of a change.
type orgStrictComponentDetail struct {
	Component string `json:"component"`
	// Classification is the shipped classification ("refused", or
	// "unclassified"); Reviewed is always false: an admin-allowed component
	// has not been reviewed for strict mode.
	Classification string `json:"classification"`
	Reviewed       bool   `json:"reviewed"`
}

// componentClassification is component's shipped strict classification as
// the API reports it ("refused", or "" for none) and its reason.
func componentClassification(component string) (class, reason string) {
	c, ok := validate.ClassifyComponent(component)
	if !ok {
		return "", ""
	}
	return string(c.Class), c.Reason
}

func strictComponentDetail(component string) orgStrictComponentDetail {
	class, _ := componentClassification(component)
	if class == "" {
		class = "unclassified"
	}
	return orgStrictComponentDetail{Component: component, Classification: class}
}

// ListOrgStrictComponents lists an org's admin-allowed components.
func (s *AdminService) ListOrgStrictComponents(ctx context.Context, req *connect.Request[mgmtv1.ListOrgStrictComponentsRequest]) (*connect.Response[mgmtv1.ListOrgStrictComponentsResponse], error) {
	orgID, err := scanUUID(req.Msg.GetOrgId())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid org id"))
	}
	if _, err := s.store.Queries.GetOrgByID(ctx, orgID); err != nil {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("org not found"))
	}
	rows, err := s.store.Queries.ListOrgStrictComponents(ctx, orgID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to list allowed components"))
	}
	items := make([]*mgmtv1.OrgStrictComponent, len(rows))
	for i := range rows {
		items[i] = strictComponentProto(rows[i])
	}
	return connect.NewResponse(&mgmtv1.ListOrgStrictComponentsResponse{Items: items}), nil
}

// AddOrgStrictComponent allows a component for an org.
func (s *AdminService) AddOrgStrictComponent(ctx context.Context, req *connect.Request[mgmtv1.AddOrgStrictComponentRequest]) (*connect.Response[mgmtv1.OrgStrictComponent], error) {
	if err := requireWriteAuthorized(ctx); err != nil {
		return nil, err
	}
	orgID, err := scanUUID(req.Msg.GetOrgId())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid org id"))
	}
	component := req.Msg.GetComponent()
	if err := validate.CheckAllowableComponent(component); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	var out *mgmtv1.OrgStrictComponent
	err = s.inImportRepoTx(ctx, orgID, func(q *sqlc.Queries, org sqlc.Org) error {
		row, err := q.AddOrgStrictComponent(ctx, sqlc.AddOrgStrictComponentParams{
			OrgID: orgID, Component: component, CreatedBy: actorFromCtx(ctx),
		})
		if isUniqueViolation(err) {
			return connect.NewError(connect.CodeAlreadyExists, fmt.Errorf("org %q already allows %s", org.Name, component))
		}
		if err != nil {
			return connect.NewError(connect.CodeInternal, errors.New("failed to allow the component"))
		}
		if err := auditInsert(ctx, q, actorFromCtx(ctx), "user", orgID, "org.strict_component.add", "org", orgID.String(),
			strictComponentDetail(component)); err != nil {
			return connect.NewError(connect.CodeInternal, errors.New("failed to record the change"))
		}
		if err := q.MarkServeCacheDirtyByOrg(ctx, orgID); err != nil {
			return connect.NewError(connect.CodeInternal, errors.New("failed to mark the org's served configs for recompute"))
		}
		out = strictComponentProto(row)
		return nil
	})
	if err != nil {
		return nil, err
	}
	if s.recompute != nil {
		go s.recompute(context.Background(), orgID) //nolint:contextcheck,gosec // G118+contextcheck: intentional detached context, as SetOrgStrictTenants
	}
	return connect.NewResponse(out), nil
}

// RemoveOrgStrictComponent takes a component off an org's list, refused
// while the strict tenant rule applies to the org and an enabled pipeline
// uses it.
func (s *AdminService) RemoveOrgStrictComponent(ctx context.Context, req *connect.Request[mgmtv1.RemoveOrgStrictComponentRequest]) (*connect.Response[mgmtv1.RemoveOrgStrictComponentResponse], error) {
	if err := requireWriteAuthorized(ctx); err != nil {
		return nil, err
	}
	orgID, err := scanUUID(req.Msg.GetOrgId())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid org id"))
	}
	id, err := scanUUID(req.Msg.GetId())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid allowed component id"))
	}
	err = s.inImportRepoTx(ctx, orgID, func(q *sqlc.Queries, org sqlc.Org) error {
		row, err := q.GetOrgStrictComponent(ctx, id)
		if err != nil || row.OrgID != orgID {
			return connect.NewError(connect.CodeNotFound, errors.New("allowed component not found"))
		}
		if org.StrictTenants || s.requireOwned {
			p, err := tenancy.LoadOrg(ctx, q, org, s.requireOwned)
			if err != nil {
				return connect.NewError(connect.CodeInternal, errors.New("failed to check what uses the component"))
			}
			users, err := tenancy.StrictComponentUsers(ctx, q, p, row.Component)
			if err != nil {
				return busyOr(err, connect.NewError(connect.CodeInternal, errors.New("failed to check what uses the component")))
			}
			if len(users) > 0 {
				return connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf(
					"%s is still used by enabled pipeline(s) of org %q, which strict tenant mode would then refuse: %s; "+
						"edit or disable them first", row.Component, org.Name, strings.Join(users, "; ")))
			}
		}
		if _, err := q.DeleteOrgStrictComponent(ctx, sqlc.DeleteOrgStrictComponentParams{ID: id, OrgID: orgID}); err != nil {
			return connect.NewError(connect.CodeInternal, errors.New("failed to remove the allowed component"))
		}
		if err := auditInsert(ctx, q, actorFromCtx(ctx), "user", orgID, "org.strict_component.remove", "org", orgID.String(),
			strictComponentDetail(row.Component)); err != nil {
			return connect.NewError(connect.CodeInternal, errors.New("failed to record the change"))
		}
		if err := q.MarkServeCacheDirtyByOrg(ctx, orgID); err != nil {
			return connect.NewError(connect.CodeInternal, errors.New("failed to mark the org's served configs for recompute"))
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if s.recompute != nil {
		go s.recompute(context.Background(), orgID) //nolint:contextcheck,gosec // G118+contextcheck: intentional detached context, as SetOrgStrictTenants
	}
	return connect.NewResponse(&mgmtv1.RemoveOrgStrictComponentResponse{}), nil
}

func strictComponentProto(r sqlc.OrgStrictComponent) *mgmtv1.OrgStrictComponent {
	class, reason := componentClassification(r.Component)
	return &mgmtv1.OrgStrictComponent{
		Id: r.ID.String(), OrgId: r.OrgID.String(), Component: r.Component, Classification: class, Reason: reason,
		CreatedBy: r.CreatedBy, CreatedAt: timestamppb.New(r.CreatedAt.Time),
	}
}
