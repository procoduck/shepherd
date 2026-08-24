package mgmtapi

import (
	"context"
	"errors"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5/pgtype"
	"google.golang.org/protobuf/types/known/timestamppb"

	mgmtv1 "shepherd/gen/shepherd/mgmt/v1"
	"shepherd/internal/auth"
)

// errOIDCUnavailable is returned when the AdminService was constructed
// without the auth handler that owns OIDC configuration. That happens only in
// test wirings that mount the RPC surface without a server (MountRPC's
// WithOIDCSettings option is always supplied in production, see
// internal/server/server.go), so CodeUnavailable — "this deployment does not
// offer it" — is the honest answer rather than an internal error.
var errOIDCUnavailable = errors.New("OIDC settings are not available in this deployment")

// GetOidcSettings returns the effective single sign-on configuration.
//
// It answers for a chart-managed deployment too, rather than pretending there
// is nothing configured: an admin needs to see which provider their cluster
// trusts, and `editable: false` plus a status message is a better answer than
// an empty form that invites them to create a second one.
func (s *AdminService) GetOidcSettings(ctx context.Context, _ *connect.Request[mgmtv1.GetOidcSettingsRequest]) (*connect.Response[mgmtv1.OidcSettings], error) {
	if s.oidc == nil {
		return nil, connect.NewError(connect.CodeUnavailable, errOIDCUnavailable)
	}
	settings, err := s.oidc.EffectiveSettings(ctx)
	if err != nil {
		s.logger.Error("loading OIDC settings", "err", err)
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(s.toOidcSettingsProto(settings)), nil
}

// UpdateOidcSettings stores and activates a configuration.
func (s *AdminService) UpdateOidcSettings(ctx context.Context, req *connect.Request[mgmtv1.UpdateOidcSettingsRequest]) (*connect.Response[mgmtv1.OidcSettings], error) {
	if err := requireWriteAuthorized(ctx); err != nil {
		return nil, err
	}
	if s.oidc == nil {
		return nil, connect.NewError(connect.CodeUnavailable, errOIDCUnavailable)
	}
	m := req.Msg
	candidate := &auth.Settings{
		Enabled:        m.GetEnabled(),
		Provider:       m.GetProvider(),
		DisplayName:    m.GetDisplayName(),
		Issuer:         m.GetIssuer(),
		ClientID:       m.GetClientId(),
		ClientSecret:   m.GetClientSecret(),
		RedirectURL:    m.GetRedirectUrl(),
		Scopes:         m.GetScopes(),
		SubjectClaim:   m.GetSubjectClaim(),
		EmailClaim:     m.GetEmailClaim(),
		NameClaim:      m.GetNameClaim(),
		GroupsClaim:    m.GetGroupsClaim(),
		AppAdminGroups: m.GetAppAdminGroups(),
		UseGraphGroups: m.GetUseGraphGroups(),
		GraphBaseURL:   m.GetGraphBaseUrl(),
	}
	if candidate.ClientSecret == "" {
		// Empty means "keep the stored secret". The API never returns the
		// secret, so a form that round-tripped what it was given would send a
		// blank back and erase it on every save of an unrelated field.
		secret, err := s.storedClientSecret(ctx)
		if err != nil {
			return nil, err
		}
		candidate.ClientSecret = secret
	}
	saved, err := s.oidc.SaveSettings(ctx, candidate, auth.ActorFromCtx(ctx))
	if err != nil {
		if saved == nil {
			// Audited even though nothing changed. A refused attempt to
			// re-point the identity provider — on a chart-managed cluster, or a
			// run of failing saves against an issuer that will not discover —
			// is exactly the sequence an incident review needs to see, and
			// auditing only successes would leave it invisible.
			s.auditOIDCDenied(ctx, "oidc_settings.update_denied", candidate, err)
			return nil, oidcSettingsError(err)
		}
		// Stored but not activated: the write happened, so report the row that
		// exists along with the reason sign-in is not live yet.
		s.logger.Error("activating saved OIDC settings", "err", err)
		resp := s.toOidcSettingsProto(saved)
		resp.StatusMessage = err.Error()
		s.auditOIDC(ctx, "oidc_settings.update", saved)
		return connect.NewResponse(resp), nil
	}
	s.auditOIDC(ctx, "oidc_settings.update", saved)
	return connect.NewResponse(s.toOidcSettingsProto(saved)), nil
}

// TestOidcSettings probes a candidate configuration without storing it.
func (s *AdminService) TestOidcSettings(ctx context.Context, req *connect.Request[mgmtv1.TestOidcSettingsRequest]) (*connect.Response[mgmtv1.TestOidcSettingsResponse], error) {
	if s.oidc == nil {
		return nil, connect.NewError(connect.CodeUnavailable, errOIDCUnavailable)
	}
	m := req.Msg
	candidate := &auth.Settings{
		Provider:     m.GetProvider(),
		Issuer:       m.GetIssuer(),
		ClientID:     m.GetClientId(),
		ClientSecret: m.GetClientSecret(),
		Scopes:       m.GetScopes(),
	}
	// Audited even though it stores nothing: this is the one procedure that
	// makes the SERVER fetch a URL the caller chose, and "who probed what"
	// is exactly the question an incident review asks about that shape.
	s.auditOIDC(ctx, "oidc_settings.test", &auth.Settings{Provider: candidate.Provider, Issuer: candidate.Issuer})
	result, err := s.oidc.TestSettings(ctx, candidate)
	if err != nil {
		// A failed probe is the expected outcome of a typo, not an RPC error:
		// returning it in the body lets the form show the reason inline
		// instead of as a generic request failure.
		return connect.NewResponse(&mgmtv1.TestOidcSettingsResponse{Ok: false, Message: err.Error()}), nil //nolint:nilerr // a failed probe is this RPC's RESULT, not a transport failure: the form renders the reason inline
	}
	return connect.NewResponse(&mgmtv1.TestOidcSettingsResponse{
		Ok:                    true,
		Message:               "Discovery succeeded.",
		Issuer:                result.Issuer,
		AuthorizationEndpoint: result.AuthorizationEndpoint,
		TokenEndpoint:         result.TokenEndpoint,
		JwksUri:               result.JWKSURI,
		SupportedScopes:       result.SupportedScopes,
		MissingScopes:         result.MissingScopes,
		SupportsPkce:          result.SupportsPKCE,
		IssuerMismatch:        result.IssuerMismatch,
	}), nil
}

// DeleteOidcSettings removes the stored configuration and turns OIDC off.
func (s *AdminService) DeleteOidcSettings(ctx context.Context, _ *connect.Request[mgmtv1.DeleteOidcSettingsRequest]) (*connect.Response[mgmtv1.DeleteOidcSettingsResponse], error) {
	if err := requireWriteAuthorized(ctx); err != nil {
		return nil, err
	}
	if s.oidc == nil {
		return nil, connect.NewError(connect.CodeUnavailable, errOIDCUnavailable)
	}
	if err := s.oidc.DeleteSettings(ctx); err != nil {
		s.auditOIDCDenied(ctx, "oidc_settings.delete_denied", nil, err)
		return nil, oidcSettingsError(err)
	}
	s.auditOIDC(ctx, "oidc_settings.delete", nil)
	return connect.NewResponse(&mgmtv1.DeleteOidcSettingsResponse{}), nil
}

// ListOidcProviderPresets returns the built-in provider catalogue.
func (s *AdminService) ListOidcProviderPresets(_ context.Context, _ *connect.Request[mgmtv1.ListOidcProviderPresetsRequest]) (*connect.Response[mgmtv1.ListOidcProviderPresetsResponse], error) {
	items := make([]*mgmtv1.OidcProviderPreset, 0, len(auth.Presets))
	for i := range auth.Presets {
		p := &auth.Presets[i]
		items = append(items, &mgmtv1.OidcProviderPreset{
			Key:                 p.Key,
			DisplayName:         p.DisplayName,
			IssuerTemplate:      p.IssuerTemplate,
			IssuerHint:          p.IssuerHint,
			Scopes:              p.Scopes,
			SubjectClaim:        p.SubjectClaim,
			EmailClaim:          p.EmailClaim,
			NameClaim:           p.NameClaim,
			GroupsClaim:         p.GroupsClaim,
			SupportsGraphGroups: p.SupportsGraphGroups,
			GroupsNote:          p.GroupsNote,
		})
	}
	return connect.NewResponse(&mgmtv1.ListOidcProviderPresetsResponse{Items: items}), nil
}

// auditOIDC records a change to the identity-provider configuration — the
// single highest-leverage write in the product, since it decides who can hold
// an app-admin session at all. The detail carries only what identifies the
// provider; the client secret is never part of it.
func (s *AdminService) auditOIDC(ctx context.Context, action string, settings *auth.Settings) {
	detail := map[string]any{}
	if settings != nil {
		detail["provider"] = settings.Provider
		detail["issuer"] = settings.Issuer
		detail["enabled"] = settings.Enabled
		detail["app_admin_groups"] = len(settings.AppAdminGroups)
	}
	auditLogDetail(ctx, s.store, auth.ActorFromCtx(ctx), "user", pgtype.UUID{}, action, "oidc_settings", "", detail)
}

// auditOIDCDenied records a change to the identity-provider configuration that
// did NOT happen, and why. cause is a validation message or a (sanitized)
// discovery failure — never a secret; the client secret has no path into any
// error this package produces.
func (s *AdminService) auditOIDCDenied(ctx context.Context, action string, attempted *auth.Settings, cause error) {
	detail := map[string]any{"error": cause.Error()}
	if attempted != nil {
		detail["provider"] = attempted.Provider
		detail["issuer"] = attempted.Issuer
		detail["enabled"] = attempted.Enabled
	}
	auditLogDetail(ctx, s.store, auth.ActorFromCtx(ctx), "user", pgtype.UUID{}, action, "oidc_settings", "", detail)
}

// storedClientSecret reads the currently stored secret so a save that omits
// it can carry it forward. It is never returned to a caller.
func (s *AdminService) storedClientSecret(ctx context.Context) (string, error) {
	current, err := s.oidc.EffectiveSettings(ctx)
	if err != nil {
		return "", connect.NewError(connect.CodeInternal, err)
	}
	if current == nil || current.ClientSecret == "" {
		return "", connect.NewError(connect.CodeInvalidArgument, errors.New("client secret is required — there is no stored secret to keep"))
	}
	return current.ClientSecret, nil
}

// toOidcSettingsProto projects the resolved settings onto the wire message.
// The client secret is deliberately absent; only its presence is reported.
func (s *AdminService) toOidcSettingsProto(in *auth.Settings) *mgmtv1.OidcSettings {
	out := &mgmtv1.OidcSettings{
		Source:        auth.SourceDatabase,
		Editable:      !s.oidc.HelmManaged(),
		Active:        s.oidc.OIDCEnabled(),
		StatusMessage: s.oidc.StatusMessage(in),
	}
	if in == nil {
		// Nothing configured yet. The generic preset's defaults give the form
		// something coherent to open with rather than a blank claim set the
		// admin would have to know to fill in.
		preset := auth.PresetByKey(auth.ProviderGeneric)
		out.Provider = preset.Key
		out.Scopes = preset.Scopes
		out.SubjectClaim = preset.SubjectClaim
		out.EmailClaim = preset.EmailClaim
		out.NameClaim = preset.NameClaim
		out.GroupsClaim = preset.GroupsClaim
		out.GraphBaseUrl = "https://graph.microsoft.com"
		return out
	}
	out.Configured = in.Issuer != ""
	out.Enabled = in.Enabled
	out.Source = in.Source
	out.Provider = in.Provider
	out.DisplayName = in.DisplayName
	out.Issuer = in.Issuer
	out.ClientId = in.ClientID
	out.ClientSecretSet = in.ClientSecret != ""
	out.RedirectUrl = in.RedirectURL
	out.Scopes = in.Scopes
	out.SubjectClaim = in.SubjectClaim
	out.EmailClaim = in.EmailClaim
	out.NameClaim = in.NameClaim
	out.GroupsClaim = in.GroupsClaim
	out.AppAdminGroups = in.AppAdminGroups
	out.UseGraphGroups = in.UseGraphGroups
	out.GraphBaseUrl = in.GraphBaseURL
	out.UpdatedBy = in.UpdatedBy
	if !in.UpdatedAt.IsZero() {
		out.UpdatedAt = timestamppb.New(in.UpdatedAt)
	}
	return out
}

// oidcSettingsError maps the auth package's sentinels onto Connect codes. A
// validation message is InvalidArgument so the settings form can render it
// against the field the admin got wrong, rather than as an opaque failure.
func oidcSettingsError(err error) error {
	switch {
	case errors.Is(err, auth.ErrHelmManaged):
		return connect.NewError(connect.CodeFailedPrecondition, err)
	case errors.Is(err, auth.ErrEncryptionUnavailable), errors.Is(err, auth.ErrNoSettings):
		return connect.NewError(connect.CodeFailedPrecondition, err)
	default:
		return connect.NewError(connect.CodeInvalidArgument, err)
	}
}
