package auth

// Provider preset keys. These are the values oidc_settings.provider (0014)
// may hold and the keys the admin UI's provider picker sends back. A preset
// only supplies DEFAULTS for the fields an admin then confirms or edits —
// nothing reads the preset at login time, because every value it prefills is
// stored explicitly on the settings row. That keeps "what is this deployment
// actually configured to do" answerable from one row rather than from a row
// plus a table in the binary that may have shifted between releases.
const (
	ProviderEntra     = "entra"
	ProviderOkta      = "okta"
	ProviderGoogle    = "google"
	ProviderAuth0     = "auth0"
	ProviderKeycloak  = "keycloak"
	ProviderCognito   = "cognito"
	ProviderGitLab    = "gitlab"
	ProviderAuthentik = "authentik"
	ProviderOneLogin  = "onelogin"
	ProviderGeneric   = "generic"
)

// Preset describes one identity provider's conventional OIDC settings.
type Preset struct {
	// Key is the stable identifier stored in oidc_settings.provider.
	Key string
	// DisplayName is the default login-button label ("Continue with Okta").
	DisplayName string
	// IssuerTemplate shows the shape of the issuer URL, with {placeholders}
	// the admin fills in. It is documentation rendered in the UI, never
	// interpolated by the server — the admin submits a concrete issuer.
	IssuerTemplate string
	// IssuerHint is one sentence on where to find the issuer value.
	IssuerHint string
	// Scopes are the default requested scopes.
	Scopes []string
	// SubjectClaim/EmailClaim/NameClaim/GroupsClaim are the claim names this
	// provider conventionally emits.
	SubjectClaim string
	EmailClaim   string
	NameClaim    string
	GroupsClaim  string
	// SupportsGraphGroups is true only for Entra, the one provider with a
	// directory API Shepherd knows how to call when the groups claim is
	// absent (the >200-group "claim overage" case).
	SupportsGraphGroups bool
	// GroupsNote tells the admin what they must do IN THE IDP for the groups
	// claim to arrive at all. Every provider here needs some deliberate act —
	// a claim mapping, a scope, a group-claims toggle — and an admin who
	// skips it sees an empty groups list with no error, which is the single
	// most likely way this feature is misconfigured.
	GroupsNote string
}

// Presets is the built-in provider catalogue, ordered for display: the
// providers most likely to be in front of an operator first, generic last.
//
//nolint:gochecknoglobals // static catalogue, read-only after init
var Presets = []Preset{
	{
		Key:                 ProviderEntra,
		DisplayName:         "Microsoft",
		IssuerTemplate:      "https://login.microsoftonline.com/{tenant-id}/v2.0",
		IssuerHint:          "Entra admin center → App registrations → your app → Endpoints → the OpenID Connect metadata document URL, minus the trailing /.well-known/openid-configuration.",
		Scopes:              []string{"openid", "profile", "email", "GroupMember.Read.All"},
		SubjectClaim:        "oid",
		EmailClaim:          "email",
		NameClaim:           "name",
		GroupsClaim:         "groups",
		SupportsGraphGroups: true,
		GroupsNote:          "Enable group claims on the app registration (Token configuration → Add groups claim). Entra OMITS the claim entirely once a user is in more than ~200 groups, so leave the Microsoft Graph lookup on unless you know your users are under that limit; it needs the GroupMember.Read.All delegated scope granted.",
	},
	{
		Key:            ProviderOkta,
		DisplayName:    "Okta",
		IssuerTemplate: "https://{your-org}.okta.com/oauth2/default",
		IssuerHint:     "Okta admin → Security → API → your authorization server → Issuer URI. Use the org URL with no /oauth2/... suffix only if you are using the org authorization server.",
		Scopes:         []string{"openid", "profile", "email", "groups"},
		SubjectClaim:   "sub",
		EmailClaim:     "email",
		NameClaim:      "name",
		GroupsClaim:    "groups",
		GroupsNote:     "Add a 'groups' claim to the ID token on your authorization server (Claims → Add Claim → value type Groups) and grant the groups scope, otherwise the claim never appears.",
	},
	{
		Key:            ProviderGoogle,
		DisplayName:    "Google",
		IssuerTemplate: "https://accounts.google.com",
		IssuerHint:     "Always exactly https://accounts.google.com for Google Workspace and consumer Google accounts.",
		Scopes:         []string{"openid", "profile", "email"},
		SubjectClaim:   "sub",
		EmailClaim:     "email",
		NameClaim:      "name",
		GroupsClaim:    "groups",
		GroupsNote:     "Google does NOT emit group membership in the ID token. Plain Google sign-in therefore gives every user an empty group list — no app admin, no org access. Use Google only if you are fronting it with an IdP that adds a groups claim (Okta, Auth0, Keycloak); otherwise keep the local admin account as your way in.",
	},
	{
		Key:            ProviderAuth0,
		DisplayName:    "Auth0",
		IssuerTemplate: "https://{your-tenant}.us.auth0.com/",
		IssuerHint:     "Auth0 dashboard → Applications → your app → Advanced Settings → Endpoints. Note Auth0 issuers include the trailing slash.",
		Scopes:         []string{"openid", "profile", "email"},
		SubjectClaim:   "sub",
		EmailClaim:     "email",
		NameClaim:      "name",
		GroupsClaim:    "https://shepherd/groups",
		GroupsNote:     "Auth0 has no built-in groups claim: add one with a Login Action (api.idToken.setCustomClaim). Custom claims MUST be namespaced with a URI you control, and the groups-claim field here must match that URI exactly.",
	},
	{
		Key:            ProviderKeycloak,
		DisplayName:    "Keycloak",
		IssuerTemplate: "https://{keycloak-host}/realms/{realm}",
		IssuerHint:     "Keycloak admin → Realm settings → Endpoints → OpenID Endpoint Configuration; the issuer is the URL up to and including /realms/{realm}.",
		Scopes:         []string{"openid", "profile", "email", "groups"},
		SubjectClaim:   "sub",
		EmailClaim:     "email",
		NameClaim:      "name",
		GroupsClaim:    "groups",
		GroupsNote:     "Add a 'groups' client scope backed by a Group Membership mapper with 'Add to ID token' on. Keycloak's mapper emits full paths (/platform/admins) by default; either turn 'Full group path' off or enter the full paths here.",
	},
	{
		Key:            ProviderCognito,
		DisplayName:    "AWS Cognito",
		IssuerTemplate: "https://cognito-idp.{region}.amazonaws.com/{user-pool-id}",
		IssuerHint:     "The issuer is built from the user pool's region and ID — not the hosted-UI domain, which is a different host.",
		Scopes:         []string{"openid", "profile", "email"},
		SubjectClaim:   "sub",
		EmailClaim:     "email",
		NameClaim:      "name",
		GroupsClaim:    "cognito:groups",
		GroupsNote:     "Cognito emits group membership as 'cognito:groups' automatically for users in a user-pool group — no mapping needed, but the claim name is not 'groups'.",
	},
	{
		Key:            ProviderGitLab,
		DisplayName:    "GitLab",
		IssuerTemplate: "https://gitlab.com",
		IssuerHint:     "https://gitlab.com for GitLab.com, or your self-managed instance root URL.",
		Scopes:         []string{"openid", "profile", "email"},
		SubjectClaim:   "sub",
		EmailClaim:     "email",
		NameClaim:      "name",
		GroupsClaim:    "groups_direct",
		GroupsNote:     "GitLab emits 'groups_direct' (direct memberships only, as full paths like acme/platform). Nested/inherited memberships are NOT included, so enter the group the user is a direct member of.",
	},
	{
		Key:            ProviderAuthentik,
		DisplayName:    "authentik",
		IssuerTemplate: "https://{authentik-host}/application/o/{app-slug}/",
		IssuerHint:     "authentik admin → Applications → Providers → your provider → OpenID Configuration Issuer.",
		Scopes:         []string{"openid", "profile", "email"},
		SubjectClaim:   "sub",
		EmailClaim:     "email",
		NameClaim:      "name",
		GroupsClaim:    "groups",
		GroupsNote:     "Add the built-in 'authentik default OAuth Mapping: OpenID 'profile'' scope, or a custom property mapping returning {\"groups\": [g.name for g in request.user.ak_groups.all()]}, to the provider's scopes.",
	},
	{
		Key:            ProviderOneLogin,
		DisplayName:    "OneLogin",
		IssuerTemplate: "https://{your-org}.onelogin.com/oidc/2",
		IssuerHint:     "OneLogin admin → Applications → your app → SSO → Issuer URL.",
		Scopes:         []string{"openid", "profile", "email", "groups"},
		SubjectClaim:   "sub",
		EmailClaim:     "email",
		NameClaim:      "name",
		GroupsClaim:    "groups",
		GroupsNote:     "Set the app's Parameters → Groups to the role or group attribute you want, and include the groups scope.",
	},
	{
		Key:            ProviderGeneric,
		DisplayName:    "SSO",
		IssuerTemplate: "https://{issuer-host}",
		IssuerHint:     "Any spec-compliant issuer: the URL that serves {issuer}/.well-known/openid-configuration.",
		Scopes:         []string{"openid", "profile", "email"},
		SubjectClaim:   "sub",
		EmailClaim:     "email",
		NameClaim:      "name",
		GroupsClaim:    "groups",
		GroupsNote:     "Whatever claim your provider puts group membership in. Its values are matched against the app-admin group list here and against each org's admin/reader group — so they must be the same strings your IdP emits.",
	},
}

// PresetByKey returns the preset for key, or the generic preset when key is
// unknown. It never returns nil: an unrecognized provider string (an older
// row, a hand-edited value) still needs sane claim defaults rather than a
// panic or an empty form.
func PresetByKey(key string) Preset {
	for i := range Presets {
		if Presets[i].Key == key {
			return Presets[i]
		}
	}
	return Presets[len(Presets)-1]
}
