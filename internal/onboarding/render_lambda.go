package onboarding

import (
	"fmt"
	"strings"
)

// adotLambdaDocsURL is AWS's own published table of ADOT Lambda layer ARNs
// by region/runtime/architecture — confirmed 2026-08-22 to be that table,
// not assumed from memory. D5's corollary requires linking this instead of
// hardcoding an ARN (they drift per release and are not pinned/freshness-
// checked the way ALLOY_VERSION is).
const adotLambdaDocsURL = "https://aws-otel.github.io/docs/getting-started/lambda"

// renderLambda renders a JSON snippet in the shape
// `aws lambda update-function-configuration --environment file://env.json`
// accepts: the most direct "Lambda env vars" surface, independent of which
// IaC tool (or none) manages the function itself.
func renderLambda(base string, spec ConnectAppSpec) string {
	var sb strings.Builder
	_, _ = fmt.Fprintf(&sb, "# aws lambda update-function-configuration --function-name <fn> \\\n")
	_, _ = fmt.Fprintf(&sb, "#   --environment file://env.json\n")
	if !spec.IncludeADOTLayer {
		_, _ = fmt.Fprintf(&sb, "#\n")
		_, _ = fmt.Fprintf(&sb, "# No ADOT layer attached (ConnectAppSpec.IncludeADOTLayer was false) — add\n")
		_, _ = fmt.Fprintf(&sb, "# one via --layers to get auto-instrumentation; look the ARN up for your\n")
		_, _ = fmt.Fprintf(&sb, "# region/runtime/architecture at %s\n", adotLambdaDocsURL)
		_, _ = fmt.Fprintf(&sb, "# (Shepherd does not hardcode this ARN — it drifts per release).\n")
	}
	_, _ = fmt.Fprintf(&sb, "# env.json:\n")
	_, _ = fmt.Fprintf(&sb, "{\n")
	_, _ = fmt.Fprintf(&sb, "  \"Variables\": {\n")
	vars := lambdaEnv(base, spec)
	for i, v := range vars {
		comma := ","
		if i == len(vars)-1 {
			comma = ""
		}
		_, _ = fmt.Fprintf(&sb, "    %q: %q%s\n", v.Key, v.Value, comma)
	}
	_, _ = fmt.Fprintf(&sb, "  }\n")
	_, _ = fmt.Fprintf(&sb, "}\n")
	return sb.String()
}
