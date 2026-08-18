package agentapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"slices"
	"strings"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5/pgtype"
	"golang.org/x/sync/singleflight"

	collectorv1 "shepherd/gen/collector/v1"
	"shepherd/gen/collector/v1/collectorv1connect"
	"shepherd/internal/merge"
	"shepherd/internal/metrics"
	"shepherd/internal/store"
	"shepherd/internal/store/sqlc"
	"shepherd/internal/validate"
)

// validRoles is the set of allowed collector roles.
var validRoles = []string{"metrics", "logs", "singleton", "receiver"}

// sanitize replaces non-[a-z0-9_] characters with underscores and ensures
// the result starts with a letter (prepends "p" if needed).
var sanitizeRe = regexp.MustCompile(`[^a-z0-9_]`)

func sanitizeName(s string) string {
	r := sanitizeRe.ReplaceAllString(strings.ToLower(s), "_")
	if len(r) == 0 || (r[0] >= '0' && r[0] <= '9') {
		r = "p" + r
	}
	return r
}

// emptyHash is sha256hex(""). Exported for use in tests.
const EmptyHash = "e3b0c44298fc1c149afbf4c8996fb924" +
	"27ae41e4649b934ca495991b7852b855"

// emptyHash is an unexported alias kept for backward compat inside this package.
const emptyHash = EmptyHash

// Service implements collector.v1 CollectorService.
type Service struct {
	collectorv1connect.UnimplementedCollectorServiceHandler
	store     *store.Store
	validator *validate.Validator
	logger    *slog.Logger
	sf        singleflight.Group
}

// New creates a new agent API Service.
func New(st *store.Store, v *validate.Validator, logger *slog.Logger) *Service {
	return &Service{store: st, validator: v, logger: logger.With("component", "agentapi")}
}

// RegisterCollector handles collector registration.
func (s *Service) RegisterCollector(
	ctx context.Context,
	req *connect.Request[collectorv1.RegisterCollectorRequest],
) (*connect.Response[collectorv1.RegisterCollectorResponse], error) {
	attrs := effectiveAttrs(req.Msg.LocalAttributes, req.Msg.Attributes) //nolint:staticcheck // deprecated field used as fallback

	cluster, role, err := requireClusterRole(attrs)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}

	if err := s.upsertCollectorInstance(ctx, req.Msg.Id, req.Msg.Name, cluster, role, attrs); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	return connect.NewResponse(&collectorv1.RegisterCollectorResponse{}), nil
}

// GetConfig handles config polling by collectors.
func (s *Service) GetConfig(
	ctx context.Context,
	req *connect.Request[collectorv1.GetConfigRequest],
) (*connect.Response[collectorv1.GetConfigResponse], error) {
	attrs := effectiveAttrs(req.Msg.LocalAttributes, req.Msg.Attributes) //nolint:staticcheck // deprecated field used as fallback

	cluster, role, err := requireClusterRole(attrs)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}

	// Upsert instance (self-register on GetConfig too — §4.1).
	if err := s.upsertCollectorInstance(ctx, req.Msg.Id, req.Msg.Id, cluster, role, attrs); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	// Persist remote_config_status if provided.
	if req.Msg.RemoteConfigStatus != nil {
		statusStr := req.Msg.RemoteConfigStatus.Status.String()
		errMsg := req.Msg.RemoteConfigStatus.ErrorMessage
		if err := s.store.Queries.UpdateInstanceStatus(ctx, sqlc.UpdateInstanceStatusParams{
			ID:                 req.Msg.Id,
			RemoteConfigStatus: pgtype.Text{String: statusStr, Valid: true},
			RemoteConfigError:  pgtype.Text{String: errMsg, Valid: errMsg != ""},
		}); err != nil {
			s.logger.Warn("failed to update instance status", "instance_id", req.Msg.Id, "err", err)
		}
	}

	// Resolve collector and check if cluster is claimed.
	coll, err := s.store.Queries.GetCollectorByClusterAndRole(ctx, sqlc.GetCollectorByClusterAndRoleParams{
		Name: cluster,
		Role: role,
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("resolving collector: %w", err))
	}

	orgID, err := s.store.Queries.GetCollectorOrgID(ctx, coll.ID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("resolving org: %w", err))
	}

	// Unclaimed cluster: serve empty config.
	if !orgID.Valid {
		if req.Msg.Hash == emptyHash {
			metrics.GetConfigTotal.WithLabelValues("not_modified").Inc()
			return connect.NewResponse(&collectorv1.GetConfigResponse{
				NotModified: true,
				Hash:        emptyHash,
			}), nil
		}
		metrics.GetConfigTotal.WithLabelValues("served").Inc()
		return connect.NewResponse(&collectorv1.GetConfigResponse{
			Content:     "",
			Hash:        emptyHash,
			NotModified: false,
		}), nil
	}

	// Claimed: read from serve cache; recompute if dirty.
	cache, err := s.store.Queries.GetServeCache(ctx, coll.ID)
	if err != nil || cache.Dirty {
		// Cache missing or dirty — recompute now, once per collector at a time.
		result, recomputeErr, _ := s.sf.Do(coll.ID.String(), func() (any, error) {
			newContent, newHash, err := s.recomputeServeCache(ctx, coll, orgID)
			if err != nil {
				return nil, err
			}
			updated, err := s.store.Queries.UpsertServeCacheConditional(ctx, sqlc.UpsertServeCacheConditionalParams{
				CollectorID: coll.ID,
				Content:     newContent,
				Hash:        newHash,
			})
			if err != nil {
				return nil, err
			}
			return updated, nil
		})
		if recomputeErr != nil {
			metrics.ServeRecomputeFailuresTotal.Inc()
			s.logger.Warn("serve-cache recompute failed", "collector_id", coll.ID.String(), "err", recomputeErr)
			// Fall through: serve whatever is in cache (or empty if no cache yet).
		} else {
			cache = result.(sqlc.ServeCache)
		}
	}

	if req.Msg.Hash == cache.Hash {
		metrics.GetConfigTotal.WithLabelValues("not_modified").Inc()
		return connect.NewResponse(&collectorv1.GetConfigResponse{
			NotModified: true,
			Hash:        cache.Hash,
		}), nil
	}

	metrics.GetConfigTotal.WithLabelValues("served").Inc()
	return connect.NewResponse(&collectorv1.GetConfigResponse{
		Content:     cache.Content,
		Hash:        cache.Hash,
		NotModified: false,
	}), nil
}

// UnregisterCollector marks an instance as unregistered.
func (s *Service) UnregisterCollector(
	ctx context.Context,
	req *connect.Request[collectorv1.UnregisterCollectorRequest],
) (*connect.Response[collectorv1.UnregisterCollectorResponse], error) {
	if err := s.store.Queries.UnregisterInstance(ctx, req.Msg.Id); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&collectorv1.UnregisterCollectorResponse{}), nil
}

// upsertCollectorInstance upserts cluster → collector → instance rows.
func (s *Service) upsertCollectorInstance(
	ctx context.Context,
	instanceID, name, cluster, role string,
	attrs map[string]string,
) error {
	cl, err := s.store.Queries.UpsertCluster(ctx, cluster)
	if err != nil {
		return fmt.Errorf("upserting cluster %q: %w", cluster, err)
	}

	coll, err := s.store.Queries.UpsertCollector(ctx, sqlc.UpsertCollectorParams{
		ClusterID: cl.ID,
		Role:      role,
	})
	if err != nil {
		return fmt.Errorf("upserting collector: %w", err)
	}

	attrsJSON, err := json.Marshal(attrs)
	if err != nil {
		return fmt.Errorf("marshaling attributes: %w", err)
	}

	alloyVersion := pgtype.Text{}
	if v, ok := attrs["collector.version"]; ok {
		alloyVersion = pgtype.Text{String: v, Valid: true}
	}
	osVal := pgtype.Text{}
	if v, ok := attrs["collector.os"]; ok {
		osVal = pgtype.Text{String: v, Valid: true}
	}

	if _, err := s.store.Queries.UpsertCollectorInstance(ctx, sqlc.UpsertCollectorInstanceParams{
		ID:              instanceID,
		CollectorID:     coll.ID,
		Name:            name,
		LocalAttributes: attrsJSON,
		AlloyVersion:    alloyVersion,
		Os:              osVal,
	}); err != nil {
		return fmt.Errorf("upserting instance: %w", err)
	}

	return nil
}

// effectiveAttrs returns local_attributes, falling back to deprecated attributes if empty.
func effectiveAttrs(local, deprecated map[string]string) map[string]string {
	if len(local) > 0 {
		return local
	}
	return deprecated
}

// requireClusterRole extracts and validates cluster+role from attributes.
func requireClusterRole(attrs map[string]string) (cluster, role string, err error) {
	cluster = attrs["cluster"]
	role = attrs["role"]
	var missing []string
	if cluster == "" {
		missing = append(missing, "cluster")
	}
	if role == "" {
		missing = append(missing, "role")
	} else if !slices.Contains(validRoles, role) {
		return "", "", fmt.Errorf("role %q must be one of: %s", role, strings.Join(validRoles, "|"))
	}
	if len(missing) > 0 {
		return "", "", fmt.Errorf("missing required attributes: %s", strings.Join(missing, ", "))
	}
	return cluster, role, nil
}

// hashContent computes hex(sha256(content)).
func hashContent(content string) string {
	h := sha256.Sum256([]byte(content))
	return hex.EncodeToString(h[:])
}

// recomputeServeCache assembles and validates the merged config for a single collector.
// It returns (content, hash, error). On error the caller should serve the previous cached value.
func (s *Service) recomputeServeCache(ctx context.Context, coll sqlc.Collector, orgID pgtype.UUID) (string, string, error) {
	enabledPipelines, err := s.store.Queries.ListEnabledPipelinesByOrg(ctx, orgID)
	if err != nil {
		return "", "", fmt.Errorf("listing pipelines: %w", err)
	}

	var mergePipelines []merge.Pipeline
	for i := range enabledPipelines {
		ep := enabledPipelines[i]
		var m []string
		if jsonErr := json.Unmarshal(ep.Matchers, &m); jsonErr != nil {
			continue
		}
		mergePipelines = append(mergePipelines, merge.Pipeline{
			ID:       ep.ID.String(),
			Name:     ep.Name,
			Contents: ep.Contents,
			Matchers: m,
			Source:   ep.Source,
		})
	}

	cluster, clusterErr := s.store.Queries.GetClusterByID(ctx, coll.ClusterID)
	clusterName := ""
	if clusterErr == nil {
		clusterName = cluster.Name
	}

	cl := merge.CollectorLabels{
		CollectorID: coll.ID.String(),
		Labels:      map[string]string{"role": coll.Role, "cluster": clusterName},
	}

	result, err := merge.Assemble(coll.ID.String(), clusterName+"/"+coll.Role, cl, mergePipelines, "prod", "")
	if err != nil {
		return "", "", fmt.Errorf("assembling config: %w", err)
	}

	// Stage 1 validation on the merged output.
	if s.validator != nil {
		r1 := validate.Stage1(result.Content)
		if !r1.Valid {
			return "", "", fmt.Errorf("merged config failed stage-1 validation")
		}
	}

	return result.Content, result.Hash, nil
}
