package chartvalues

import (
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	"shepherd/internal/signals"
)

// validRoles is internal/signals.Policies' key set, sorted. Reusing that
// table rather than re-declaring "metrics, logs, singleton, receiver" here
// means a role this codebase adds or removes is a one-place change: the same
// lesson docs/gateway-tier-plan.md §10 records for internal/beacon's
// AppendBaseline (share the implementation so "stays in sync" is a property
// of the code, not of two people remembering to edit two lists) applied to a
// validation table instead of a render function.
func validRoles() []string {
	roles := make([]string, 0, len(signals.Policies))
	for r := range signals.Policies {
		roles = append(roles, r)
	}
	sort.Strings(roles)
	return roles
}

// collectorName is the k8s-monitoring chart's own naming convention for a
// role's Alloy collector, confirmed against docs/spec.md's already-verified
// deployment context AND the vendored chart's own test fixtures
// (testdata/values.schema.json's source repo pins collector examples under
// exactly this "alloy-<role>" key). Getting this name wrong would not fail
// `helm template` — collectors is an open map — it would silently produce a
// values file that layers onto a DIFFERENT (unmanaged) collector than the
// one the operator's own values already define under this name, which is
// the "renders but does not attach" failure class in chart-values form.
func collectorName(role string) string { return "alloy-" + role }

// DefaultPollFrequency matches internal/beacon.NewBaselineConfig's own
// ScrapeInterval default: both are "how often does a spoke-cluster Alloy
// talk to Shepherd" cadences, and picking the same number avoids a values
// file whose comments have to explain why two very similar-looking settings
// disagree for no product reason.
const DefaultPollFrequency = "60s"

// Spec is the guided-form input this package's Commit-equivalent (Render)
// turns into a values layer. Every field is validated explicitly by
// Validate — mirroring internal/receiver/config.go's documented convention —
// so a field nobody thought about cannot reach a generated file silently.
type Spec struct {
	// ClusterName identifies this cluster to Shepherd; becomes the chart's
	// top-level cluster.name, which the chart itself stamps onto every
	// collector's remotecfg attributes as "cluster" (see render.go's doc
	// comment on why this package never sets that key itself). Required.
	ClusterName string
	// ShepherdURL is this Shepherd's base URL, e.g.
	// "https://shepherd.example.com" — no path: Alloy's remotecfg block
	// speaks the Connect protocol at the server root (docs/spec.md §1), the
	// same shape internal/beacon's RemoteWriteURL resolves against
	// config.ServerConfig.BaseURL. Required; must parse as an absolute
	// http(s) URL.
	ShepherdURL string
	// Tenant, if set, becomes extraAttributes.tenant on every rendered
	// collector — an org/tenant identity a pipeline's matchers can key on,
	// the same role "tenant" already plays throughout docs/spec.md's
	// matcher model. Optional: a single-tenant operator has no reason to
	// set it.
	Tenant string
	// Roles selects which of the four collector roles
	// (internal/signals.Policies' key set) to wire remoteConfig for. At
	// least one required; unknown roles are refused rather than silently
	// dropped.
	Roles []string
	// PollFrequency is every rendered collector's remoteConfig.pollFrequency,
	// e.g. "60s". Defaults to DefaultPollFrequency when empty. Must be a
	// positive time.ParseDuration-parseable string — Alloy's remotecfg block
	// rejects anything else at startup, and refusing it here turns that
	// failure into a guided-form validation error instead of a broken pod.
	PollFrequency string
}

// bounded mirrors internal/gateway/segment.go's charset discipline for
// anything that ends up quoted into generated config: printable ASCII,
// no control characters, no characters that could break out of a YAML
// double-quoted scalar or an Alloy string literal downstream.
func bounded(s string, maxLen int) bool {
	if s == "" || len(s) > maxLen {
		return false
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f || r > 0x7e {
			return false
		}
		if r == '"' || r == '\\' {
			return false
		}
	}
	return true
}

const (
	maxClusterNameLen = 253 // Kubernetes label-value-ish bound; generous, not arbitrary
	maxTenantLen      = 253
)

// Validate checks spec for every condition Render depends on, so a bad
// guided-form submission is refused with a specific, actionable reason
// rather than producing a values file that fails later — either silently
// (a typo'd role that matches no real collector) or loudly but far away
// (Alloy refusing a malformed pollFrequency at pod startup).
func Validate(spec Spec) error {
	var problems []string

	if !bounded(spec.ClusterName, maxClusterNameLen) {
		problems = append(problems, "ClusterName must be 1-253 printable ASCII characters with no quote or backslash")
	}

	if spec.ShepherdURL == "" {
		problems = append(problems, "ShepherdURL is required")
	} else if u, err := url.Parse(spec.ShepherdURL); err != nil {
		problems = append(problems, fmt.Sprintf("ShepherdURL %q does not parse as a URL: %v", spec.ShepherdURL, err))
	} else if u.Scheme != "http" && u.Scheme != "https" {
		problems = append(problems, fmt.Sprintf("ShepherdURL %q must be an absolute http(s) URL", spec.ShepherdURL))
	} else if u.Host == "" {
		problems = append(problems, fmt.Sprintf("ShepherdURL %q has no host", spec.ShepherdURL))
	}

	if spec.Tenant != "" && !bounded(spec.Tenant, maxTenantLen) {
		problems = append(problems, "Tenant must be 1-253 printable ASCII characters with no quote or backslash")
	}

	if len(spec.Roles) == 0 {
		problems = append(problems, "at least one Role is required")
	} else {
		valid := validRoles()
		validSet := make(map[string]bool, len(valid))
		for _, r := range valid {
			validSet[r] = true
		}
		seen := make(map[string]bool, len(spec.Roles))
		for _, r := range spec.Roles {
			if !validSet[r] {
				problems = append(problems, fmt.Sprintf(
					"Role %q is not a recognized collector role; must be one of: %s", r, strings.Join(valid, ", ")))
				continue
			}
			if seen[r] {
				problems = append(problems, fmt.Sprintf("Role %q is repeated", r))
			}
			seen[r] = true
		}
	}

	poll := spec.PollFrequency
	if poll == "" {
		poll = DefaultPollFrequency
	}
	if d, err := time.ParseDuration(poll); err != nil {
		problems = append(problems, fmt.Sprintf("PollFrequency %q is not a valid duration: %v", poll, err))
	} else if d <= 0 {
		problems = append(problems, fmt.Sprintf("PollFrequency %q must be positive", poll))
	}

	if len(problems) > 0 {
		return fmt.Errorf("chartvalues: invalid Spec: %s", strings.Join(problems, "; "))
	}
	return nil
}
