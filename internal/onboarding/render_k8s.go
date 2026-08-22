package onboarding

import (
	"fmt"
	"strings"
)

// renderK8s renders a container `env` fragment for a Kubernetes
// Deployment/Pod spec. No ADOT layer concept applies (that is Lambda-
// specific packaging) — a containerized app installs its own OpenTelemetry
// SDK/agent and reads these same env vars, so this always uses coreOTLPEnv,
// never lambdaEnv.
func renderK8s(base string, spec ConnectAppSpec) string {
	var sb strings.Builder
	_, _ = fmt.Fprintf(&sb, "# Fragment of a Deployment's pod template for %q.\n", spec.ServiceName)
	_, _ = fmt.Fprintf(&sb, "# Route: kind=%s segment=%s\n", spec.Route.Kind, spec.Route.RouteSegment)
	_, _ = fmt.Fprintf(&sb, "spec:\n")
	_, _ = fmt.Fprintf(&sb, "  containers:\n")
	_, _ = fmt.Fprintf(&sb, "    - name: %s\n", spec.ServiceName)
	_, _ = fmt.Fprintf(&sb, "      # ... image, ports, etc. unchanged ...\n")
	_, _ = fmt.Fprintf(&sb, "      env:\n")
	for _, v := range coreOTLPEnv(base, spec) {
		_, _ = fmt.Fprintf(&sb, "        - name: %s\n", v.Key)
		_, _ = fmt.Fprintf(&sb, "          value: %q\n", v.Value)
	}
	return sb.String()
}
