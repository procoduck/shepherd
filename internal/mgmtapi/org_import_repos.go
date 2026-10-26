package mgmtapi

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5/pgtype"
	"google.golang.org/protobuf/types/known/timestamppb"

	mgmtv1 "shepherd/gen/shepherd/mgmt/v1"
	"shepherd/internal/pinnedimport"
	"shepherd/internal/store/sqlc"
	"shepherd/internal/tenancy"
)

// An org's allowed import.git repositories (0043, #390): in strict tenant
// mode its pipelines may import.git only from these, pinned to a full commit
// SHA, and the fetched module is linted (internal/pinnedimport). App admin
// only. Changes take the exclusive ownership lock, which every pipeline
// write's in-transaction lint (tenancy.LintLocked) takes shared, so an entry
// cannot disappear between a save's check and its commit.

// orgImportRepoDetail is the audit detail of an allowlist change. The
// credential is named by id only, never by its contents.
type orgImportRepoDetail struct {
	Repository   string `json:"repository"`
	Normalized   string `json:"normalized"`
	CredentialID string `json:"credential_id,omitempty"`
}

// ListOrgImportRepositories lists an org's allowed import repositories.
func (s *AdminService) ListOrgImportRepositories(ctx context.Context, req *connect.Request[mgmtv1.ListOrgImportRepositoriesRequest]) (*connect.Response[mgmtv1.ListOrgImportRepositoriesResponse], error) {
	orgID, err := scanUUID(req.Msg.GetOrgId())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid org id"))
	}
	if _, err := s.store.Queries.GetOrgByID(ctx, orgID); err != nil {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("org not found"))
	}
	rows, err := s.store.Queries.ListOrgImportRepositories(ctx, orgID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to list import repositories"))
	}
	items, err := s.importRepoProtos(ctx, s.store.Queries, rows)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&mgmtv1.ListOrgImportRepositoriesResponse{Items: items}), nil
}

// AddOrgImportRepository puts a repository on an org's allowlist.
func (s *AdminService) AddOrgImportRepository(ctx context.Context, req *connect.Request[mgmtv1.AddOrgImportRepositoryRequest]) (*connect.Response[mgmtv1.OrgImportRepository], error) {
	if err := requireWriteAuthorized(ctx); err != nil {
		return nil, err
	}
	orgID, err := scanUUID(req.Msg.GetOrgId())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid org id"))
	}
	repository := req.Msg.GetRepository()
	key, err := pinnedimport.NormalizeRepository(repository)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	var credID pgtype.UUID
	if c := req.Msg.GetCredentialId(); c != "" {
		if credID, err = scanUUID(c); err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid credential id"))
		}
	}
	var out *mgmtv1.OrgImportRepository
	err = s.inImportRepoTx(ctx, orgID, func(q *sqlc.Queries, org sqlc.Org) error {
		if credID.Valid {
			cred, err := q.GetGitCredentialByID(ctx, credID)
			if err != nil || cred.OrgID != orgID {
				return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("org %q has no such git credential", org.Name))
			}
		}
		row, err := q.AddOrgImportRepository(ctx, sqlc.AddOrgImportRepositoryParams{
			OrgID: orgID, Repository: repository, RepositoryKey: key, CredentialID: credID, CreatedBy: actorFromCtx(ctx),
		})
		if isUniqueViolation(err) {
			return connect.NewError(connect.CodeAlreadyExists, fmt.Errorf(
				"org %q already allows %q (as %s)", org.Name, repository, key))
		}
		if err != nil {
			return connect.NewError(connect.CodeInternal, errors.New("failed to add the import repository"))
		}
		if err := auditInsert(ctx, q, actorFromCtx(ctx), "user", orgID, "org.import_repository.add", "org", orgID.String(),
			orgImportRepoDetail{Repository: repository, Normalized: key, CredentialID: uuidOrEmpty(credID)}); err != nil {
			return connect.NewError(connect.CodeInternal, errors.New("failed to record the change"))
		}
		items, err := s.importRepoProtos(ctx, q, []sqlc.OrgImportRepository{row})
		if err != nil {
			return err
		}
		out = items[0]
		return nil
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(out), nil
}

// RemoveOrgImportRepository takes a repository off an org's allowlist,
// refused while the strict tenant rule applies to the org and an enabled
// pipeline imports from it.
func (s *AdminService) RemoveOrgImportRepository(ctx context.Context, req *connect.Request[mgmtv1.RemoveOrgImportRepositoryRequest]) (*connect.Response[mgmtv1.RemoveOrgImportRepositoryResponse], error) {
	if err := requireWriteAuthorized(ctx); err != nil {
		return nil, err
	}
	orgID, err := scanUUID(req.Msg.GetOrgId())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid org id"))
	}
	id, err := scanUUID(req.Msg.GetId())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid import repository id"))
	}
	err = s.inImportRepoTx(ctx, orgID, func(q *sqlc.Queries, org sqlc.Org) error {
		row, err := q.GetOrgImportRepository(ctx, id)
		if err != nil || row.OrgID != orgID {
			return connect.NewError(connect.CodeNotFound, errors.New("import repository not found"))
		}
		if org.StrictTenants || s.requireOwned {
			repos, err := q.ListOrgImportRepositories(ctx, orgID)
			if err != nil {
				return connect.NewError(connect.CodeInternal, errors.New("failed to check what imports from the repository"))
			}
			users, err := tenancy.ImportRepositoryUsers(ctx, q, orgID, row.RepositoryKey, repos)
			if err != nil {
				return busyOr(err, connect.NewError(connect.CodeInternal, errors.New("failed to check what imports from the repository")))
			}
			if len(users) > 0 {
				return connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf(
					"%q is still imported by enabled pipeline(s) of org %q, which strict tenant mode would then refuse: %s; "+
						"edit or disable them first", row.Repository, org.Name, strings.Join(users, "; ")))
			}
		}
		if _, err := q.DeleteOrgImportRepository(ctx, sqlc.DeleteOrgImportRepositoryParams{ID: id, OrgID: orgID}); err != nil {
			return connect.NewError(connect.CodeInternal, errors.New("failed to remove the import repository"))
		}
		if err := auditInsert(ctx, q, actorFromCtx(ctx), "user", orgID, "org.import_repository.remove", "org", orgID.String(),
			orgImportRepoDetail{Repository: row.Repository, Normalized: row.RepositoryKey, CredentialID: uuidOrEmpty(row.CredentialID)}); err != nil {
			return connect.NewError(connect.CodeInternal, errors.New("failed to record the change"))
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&mgmtv1.RemoveOrgImportRepositoryResponse{}), nil
}

// inImportRepoTx runs change in a transaction holding the exclusive
// ownership lock, with the org loaded.
func (s *AdminService) inImportRepoTx(ctx context.Context, orgID pgtype.UUID, change func(q *sqlc.Queries, org sqlc.Org) error) error {
	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return connect.NewError(connect.CodeInternal, errors.New("failed to change import repositories"))
	}
	defer func() { _ = tx.Rollback(ctx) }() //nolint:errcheck // no-op once committed
	q := s.store.Queries.WithTx(tx)
	if err := lockOrgTenantsExclusive(ctx, q); err != nil {
		return err
	}
	org, err := q.GetOrgByID(ctx, orgID)
	if err != nil {
		return connect.NewError(connect.CodeNotFound, errors.New("org not found"))
	}
	if err := change(q, org); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return connect.NewError(connect.CodeInternal, errors.New("failed to change import repositories"))
	}
	return nil
}

// importRepoProtos builds rows' protos, naming each one's credential.
func (s *AdminService) importRepoProtos(ctx context.Context, q *sqlc.Queries, rows []sqlc.OrgImportRepository) ([]*mgmtv1.OrgImportRepository, error) {
	out := make([]*mgmtv1.OrgImportRepository, len(rows))
	for i := range rows {
		r := rows[i]
		p := &mgmtv1.OrgImportRepository{
			Id: r.ID.String(), OrgId: r.OrgID.String(), Repository: r.Repository, Normalized: r.RepositoryKey,
			CreatedBy: r.CreatedBy, CreatedAt: timestamppb.New(r.CreatedAt.Time),
		}
		if r.CredentialID.Valid {
			p.CredentialId = r.CredentialID.String()
			cred, err := q.GetGitCredentialByID(ctx, r.CredentialID)
			if err != nil {
				return nil, connect.NewError(connect.CodeInternal, errors.New("failed to read the import repository's credential"))
			}
			p.CredentialName = cred.Name
		}
		out[i] = p
	}
	return out, nil
}

func uuidOrEmpty(id pgtype.UUID) string {
	if !id.Valid {
		return ""
	}
	return id.String()
}
