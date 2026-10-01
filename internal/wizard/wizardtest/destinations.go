package wizardtest

import "shepherd/internal/wizard"

// Destinations is the org destination set every wizard package's goldens and
// specs render against: the `*_dest_name` values they use resolve here, as
// internal/mgmtapi's WizardService resolves them against the org's rows. One
// destination per auth mode, so each wizard can carry a Secret-mode golden
// without inventing its own fixture.
func Destinations() wizard.Destinations {
	return wizard.Destinations{
		"prom-prod": {
			Name: "prom-prod", Type: "prometheus", AuthMode: wizard.AuthNone,
			URL: "https://mimir.prod.example.com/api/v1/push",
		},
		"prom-staging": {
			Name: "prom-staging", Type: "prometheus", AuthMode: wizard.AuthNone,
			URL: "https://mimir.staging.example.com/api/v1/push",
		},
		"loki-prod": {
			Name: "loki-prod", Type: "loki", AuthMode: wizard.AuthNone,
			URL: "https://loki.prod.example.com/loki/api/v1/push",
		},
		"loki-staging": {
			Name: "loki-staging", Type: "loki", AuthMode: wizard.AuthNone,
			URL: "https://loki.staging.example.com/loki/api/v1/push",
		},
		"prom-basic": { //nolint:gosec // G101: names a Secret, holds no credential
			Name: "prom-basic", Type: "prometheus", AuthMode: wizard.AuthBasicSecret,
			URL:             "https://mimir.example.com/api/v1/push",
			SecretNamespace: "monitoring", SecretName: "mimir-credentials",
		},
		"loki-oauth": { //nolint:gosec // G101: names a Secret, holds no credential
			Name: "loki-oauth", Type: "loki", AuthMode: wizard.AuthOAuth2Secret,
			URL:             "https://loki.example.com/loki/api/v1/push",
			SecretNamespace: "monitoring", SecretName: "loki-oauth",
			OAuth2Scopes: []string{"api://loki/.default"},
		},
	}
}
