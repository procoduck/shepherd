package chartvalues_test

import "shepherd/internal/chartvalues"

// fixtureNames lists every golden fixture, shared by the golden comparison
// test, the schema-validation test, the helm-template test, and the
// (opt-in) generator. Chosen to cover every collector role alone (proving
// each role's remoteConfig block is independently correct) plus the
// multi-role combinations a real operator plausibly selects: every role
// together, the three non-singleton "always deployed" roles docs/spec.md's
// deployment context describes, and single-tenant vs multi-tenant (the
// Tenant field is the one optional, no-op-when-absent knob Spec has).
var fixtureNames = []string{
	"metrics-only",
	"logs-only",
	"singleton-only",
	"receiver-only",
	"all-roles",
	"no-singleton",
	"single-tenant",
}

// fixture returns the Spec for one named fixture.
func fixture(name string) chartvalues.Spec {
	switch name {
	case "metrics-only":
		return chartvalues.Spec{
			ClusterName: "prod-eu-1",
			ShepherdURL: "https://shepherd.example.com",
			Tenant:      "acme",
			Roles:       []string{"metrics"},
		}
	case "logs-only":
		return chartvalues.Spec{
			ClusterName: "prod-eu-1",
			ShepherdURL: "https://shepherd.example.com",
			Tenant:      "acme",
			Roles:       []string{"logs"},
		}
	case "singleton-only":
		return chartvalues.Spec{
			ClusterName: "prod-eu-1",
			ShepherdURL: "https://shepherd.example.com",
			Tenant:      "acme",
			Roles:       []string{"singleton"},
		}
	case "receiver-only":
		return chartvalues.Spec{
			ClusterName: "prod-eu-1",
			ShepherdURL: "https://shepherd.example.com",
			Tenant:      "acme",
			Roles:       []string{"receiver"},
		}
	case "all-roles":
		// Deliberately given in non-canonical input order — Render's output
		// order must not depend on it (see render_test.go's determinism
		// check).
		return chartvalues.Spec{
			ClusterName:   "prod-eu-1",
			ShepherdURL:   "https://shepherd.example.com/", // trailing slash: Render must normalize it away
			Tenant:        "acme",
			Roles:         []string{"receiver", "metrics", "singleton", "logs"},
			PollFrequency: "2m",
		}
	case "no-singleton":
		// docs/spec.md's deployment context: alloy-metrics/alloy-logs/
		// alloy-receiver deployed on every spoke cluster, alloy-singleton
		// reserved for self-monitoring and not always present.
		return chartvalues.Spec{
			ClusterName: "prod-us-2",
			ShepherdURL: "https://shepherd.example.com",
			Tenant:      "globex",
			Roles:       []string{"metrics", "logs", "receiver"},
		}
	case "single-tenant":
		// No Tenant set: a single-tenant operator has no reason to. Proves
		// extraAttributes.tenant is genuinely omitted, not rendered empty.
		return chartvalues.Spec{
			ClusterName: "dev-cluster",
			ShepherdURL: "https://shepherd.dev.internal",
			Roles:       []string{"metrics"},
		}
	default:
		panic("chartvalues_test: unknown fixture " + name)
	}
}
